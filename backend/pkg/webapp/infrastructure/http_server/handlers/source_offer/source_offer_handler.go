// Package source_offer publishes the corresponding source of the recovery
// image next to the image itself. The image aggregates GPL-licensed software
// and every install hands it to devices over HTTP boot and inside the SD
// provisioning bundle, so the install that distributes it owes the source to
// whoever receives it (GPLv2 section 3). Serving the bundle from the same host
// is the "equivalent access" form of that offer: the recipient fetches the
// source where they fetched the binary, with no account and no token.
//
// What is served is a directory of files placed by hand — the source bundle,
// the toolchain snapshot and a SHA256SUMS beside them. The server never writes
// here, never looks inside an archive and never decides what is in the
// directory; an operator who puts a file there has published it. That is also
// why the route reads no tenant data and carries no authorization action.
package source_offer

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

// checksumsFile is the digest list placed beside the archives; its own line
// in the listing carries no digest.
const checksumsFile = "SHA256SUMS"

// noOfferBody is the whole answer of an install that publishes nothing. Plain
// text on purpose: the route is read by a person with curl, not by the SPA.
const noOfferBody = "This install serves no corresponding-source offer.\n"

// cacheControl is a public hour: the files are immutable release artefacts,
// and a listing that lags a hand placement by an hour costs nothing.
const cacheControl = "public, max-age=3600"

// Handler serves the source offer directory.
type Handler struct {
	dir    string
	logger *slog.Logger
}

// NewHandler constructs the handler. An empty directory in the config is not
// an error: it is the install stating that it publishes no offer.
func NewHandler(cfg *config.Config, logger *slog.Logger) *Handler {
	if cfg == nil {
		panic("source_offer: cfg is required")
	}
	return &Handler{dir: cfg.Provisioning.SourceOfferDir, logger: logger.With("handler", "source_offer")}
}

// RegisterRoutes wires the two public routes. No JWT and no authorization
// action: the files are a public offer by construction (see the package
// comment), and the route touches no account, node or image row. Being
// outside jwtMiddleware it escapes the per-account limiter, so it carries the
// same per-IP envelope every other tokenless root route does (sustained
// 1 req/2s, burst 10): the archives are hundreds of megabytes and a listing
// is cheap, so the limiter is there to keep the route from being free load,
// never to meter a legitimate download — which is ONE request, resumable by
// Range.
func (h *Handler) RegisterRoutes(e *echo.Echo) {
	limiter := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(0.5), Burst: 10, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return c.String(http.StatusTooManyRequests, "rate limit exceeded\n")
		},
	})
	e.GET("/source/", h.ListFiles, limiter)
	e.GET("/source/:name", h.ServeFile, limiter)
}

// entry is one file of the offer as the listing reports it.
type entry struct {
	Name   string
	Size   int64
	SHA256 string // empty when SHA256SUMS does not name the file
}

// ListFiles lists the published files
// @Summary Corresponding-source offer: list the published files
// @Description Plain-text listing of the corresponding-source bundle for the recovery image this install distributes (GPLv2 section 3, equivalent access). One line per file: name, size in bytes and the SHA-256 digest read from the `SHA256SUMS` placed beside the files (`-` when the file is not listed there). Public and tokenless on purpose: the offer is owed to whoever received the image. An install that publishes no offer answers 404 with a plain-text body.
// @Tags server
// @Produce plain
// @Success 200 {string} string "one file per line: name, size, sha256"
// @Failure 404 {string} string "this install serves no corresponding-source offer"
// @Failure 429 {string} string "rate limit exceeded"
// @Router /source/ [get]
func (h *Handler) ListFiles(c echo.Context) error {
	entries, ok, err := h.list()
	if err != nil {
		h.logger.ErrorContext(c.Request().Context(), "source offer: listing failed", "error", err)
		return c.String(http.StatusInternalServerError, "the source offer could not be read\n")
	}
	if !ok {
		return noOffer(c)
	}

	var b strings.Builder
	b.WriteString("Corresponding source for the recovery image this install serves (GPLv2 section 3).\n")
	b.WriteString("Fetch a file as /source/<name>. Columns: name, size in bytes, sha256 (- when not listed in SHA256SUMS).\n\n")
	for _, e := range entries {
		digest := e.SHA256
		if digest == "" {
			digest = "-"
		}
		fmt.Fprintf(&b, "%s\t%d\t%s\n", e.Name, e.Size, digest)
	}
	setPublicHeaders(c)
	return c.String(http.StatusOK, b.String())
}

// ServeFile serves one published file
// @Summary Corresponding-source offer: download one file
// @Description Serves one file of the corresponding-source offer as an inert attachment (`application/octet-stream`, `nosniff`), with Range requests supported so a multi-hundred-megabyte archive can be resumed. The name is a bare file name: a path separator or `..` is refused as not found, and only a regular file in the offer directory is served. Public and tokenless, as the listing is.
// @Tags server
// @Produce octet-stream
// @Param name path string true "file name as shown by the listing"
// @Success 200 {file} file "the file"
// @Success 206 {file} file "the requested byte range"
// @Failure 404 {string} string "no such file, or this install serves no corresponding-source offer"
// @Failure 429 {string} string "rate limit exceeded"
// @Router /source/{name} [get]
func (h *Handler) ServeFile(c echo.Context) error {
	if h.dir == "" {
		return noOffer(c)
	}
	name := c.Param("name")
	if !safeName(name) {
		return c.String(http.StatusNotFound, "no such file\n")
	}
	// Lstat, not Stat: a symlink is refused rather than followed, so the
	// directory can only publish what was copied INTO it. Nothing legitimate
	// is a link here — the archives are placed by hand.
	full := filepath.Join(h.dir, name)
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return c.String(http.StatusNotFound, "no such file\n")
	}
	f, err := os.Open(full)
	if err != nil {
		h.logger.ErrorContext(c.Request().Context(), "source offer: open failed", "name", name, "error", err)
		return c.String(http.StatusInternalServerError, "the file could not be read\n")
	}
	// Read-only descriptor: a close error has nothing left to report.
	defer func() { _ = f.Close() }()

	setPublicHeaders(c)
	hdr := c.Response().Header()
	// The type is fixed BEFORE ServeContent, which otherwise sniffs it from
	// the extension and the first bytes; an inert attachment is what a
	// browser must see, whatever the file is called.
	hdr.Set(echo.HeaderContentType, echo.MIMEOctetStream)
	hdr.Set(echo.HeaderContentDisposition, mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(c.Response(), c.Request(), name, info.ModTime(), f)
	return nil
}

// noOffer is the one answer of an install with no directory configured. 404,
// not 204 or an empty listing: the path does not exist here, and a short
// sentence says why, which is what a person running curl needs.
func noOffer(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderXContentTypeOptions, "nosniff")
	return c.String(http.StatusNotFound, noOfferBody)
}

// setPublicHeaders marks a successful answer cacheable and inert.
func setPublicHeaders(c echo.Context) {
	hdr := c.Response().Header()
	hdr.Set("Cache-Control", cacheControl)
	hdr.Set(echo.HeaderXContentTypeOptions, "nosniff")
	// The same sandbox /cdn applies: the SPA's own CSP is a header on the
	// frontend container and never covers a backend root route.
	hdr.Set(echo.HeaderContentSecurityPolicy, "sandbox")
}

// safeName accepts a bare file name and nothing else: no separator of either
// kind (the request path is not cleaned before it reaches the parameter), no
// `..`, nothing hidden, and nothing a Content-Disposition cannot carry
// verbatim. The directory is the whole namespace of the route.
func safeName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '"' {
			return false
		}
	}
	return true
}

// list reads the directory and the digests beside it. The second result is
// false when the install publishes no offer: no directory configured, or the
// configured one absent — an operator who has not placed the files yet has
// not published anything, and the route says so rather than listing nothing.
func (h *Handler) list() ([]entry, bool, error) {
	if h.dir == "" {
		return nil, false, nil
	}
	dirEntries, err := os.ReadDir(h.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	digests := h.readChecksums()
	var out []entry
	for _, de := range dirEntries {
		// Type() is the lstat type: a symlink is skipped here for the same
		// reason ServeFile refuses it.
		if !de.Type().IsRegular() || !safeName(de.Name()) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, entry{Name: de.Name(), Size: info.Size(), SHA256: digests[de.Name()]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, true, nil
}

// readChecksums parses the `sha256sum` output format (`<hex>  <name>`, a `*`
// before a binary-mode name) placed beside the files. A missing or malformed
// file yields no digests, never an error: the listing is still true without
// the column, and the file itself stays downloadable for the reader to check.
func (h *Handler) readChecksums() map[string]string {
	out := map[string]string{}
	f, err := os.Open(filepath.Join(h.dir, checksumsFile))
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if safeName(name) {
			out[name] = strings.ToLower(fields[0])
		}
	}
	return out
}
