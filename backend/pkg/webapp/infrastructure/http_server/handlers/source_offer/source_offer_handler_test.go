package source_offer

import (
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

func newServer(t *testing.T, dir string) *echo.Echo {
	t.Helper()
	cfg := &config.Config{}
	cfg.Provisioning.SourceOfferDir = dir
	e := echo.New()
	NewHandler(cfg, slog.New(slog.DiscardHandler)).RegisterRoutes(e)
	return e
}

func get(e *echo.Echo, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// checksumsBody is sha256sum output naming one of the two archives, plus a
// line that is not a digest and must be ignored.
const checksumsBody = "abababababababababababababababababababababababababababababababab  recovery-image-source-base.tar\nnot a digest line\n"

// offerDir builds a directory with two archives, a SHA256SUMS naming one of
// them, a hidden file, a symlink and a subdirectory — the last three must
// never be listed or served.
func offerDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("recovery-image-source-base.tar", strings.Repeat("s", 1000))
	write("toolchain-src.tar.xz", "tc")
	write(".hidden", "x")
	write(checksumsFile, checksumsBody)
	if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestNoDirectoryConfiguredIs404Text(t *testing.T) {
	e := newServer(t, "")
	for _, target := range []string{"/source/", "/source/anything.tar"} {
		rec := get(e, target, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", target, rec.Code)
		}
		if !strings.HasPrefix(rec.Header().Get(echo.HeaderContentType), "text/plain") {
			t.Fatalf("%s: content type %q, want text/plain", target, rec.Header().Get(echo.HeaderContentType))
		}
		if rec.Body.String() != noOfferBody {
			t.Fatalf("%s: body %q, want the no-offer sentence", target, rec.Body.String())
		}
	}
}

func TestAbsentDirectoryIs404Text(t *testing.T) {
	e := newServer(t, filepath.Join(t.TempDir(), "never-created"))
	rec := get(e, "/source/", nil)
	if rec.Code != http.StatusNotFound || rec.Body.String() != noOfferBody {
		t.Fatalf("status %d body %q, want 404 with the no-offer sentence", rec.Code, rec.Body.String())
	}
}

func TestListingShape(t *testing.T) {
	e := newServer(t, offerDir(t))
	rec := get(e, "/source/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != cacheControl {
		t.Fatalf("Cache-Control %q", got)
	}
	if got := rec.Header().Get(echo.HeaderXContentTypeOptions); got != "nosniff" {
		t.Fatalf("nosniff missing: %q", got)
	}
	if !strings.HasPrefix(rec.Header().Get(echo.HeaderContentType), "text/plain") {
		t.Fatalf("content type %q, want text/plain", rec.Header().Get(echo.HeaderContentType))
	}
	body := rec.Body.String()
	lines := strings.Split(strings.TrimSpace(body), "\n")
	// Two header lines, a blank line, then one tab-separated line per regular
	// file, sorted by name; the digest comes from SHA256SUMS or is `-`.
	if len(lines) < 3 || lines[2] != "" {
		t.Fatalf("listing header is not two lines and a blank:\n%s", body)
	}
	entries := lines[3:]
	want := []string{
		fmt.Sprintf("%s\t%d\t-", checksumsFile, len(checksumsBody)),
		"recovery-image-source-base.tar\t1000\t" + strings.Repeat("ab", 32),
		"toolchain-src.tar.xz\t2\t-",
	}
	if len(entries) != len(want) {
		t.Fatalf("want %d entries, got %d:\n%s", len(want), len(entries), body)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, entries[i], want[i])
		}
	}
	for _, absent := range []string{".hidden", "link", "sub"} {
		if strings.Contains(body, absent) {
			t.Errorf("listing names %q, which must not be published:\n%s", absent, body)
		}
	}
}

func TestFileIsServedAsInertAttachmentWithRanges(t *testing.T) {
	e := newServer(t, offerDir(t))
	rec := get(e, "/source/recovery-image-source-base.tar", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	// The disposition is parsed, not compared as text: a bare token and a
	// quoted string are the same header to a browser.
	if disp, params, err := mime.ParseMediaType(h.Get(echo.HeaderContentDisposition)); err != nil ||
		disp != "attachment" || params["filename"] != "recovery-image-source-base.tar" {
		t.Errorf("Content-Disposition = %q (parse error %v), want an attachment named after the file",
			h.Get(echo.HeaderContentDisposition), err)
	}
	checks := map[string]string{
		echo.HeaderContentType:           echo.MIMEOctetStream,
		echo.HeaderXContentTypeOptions:   "nosniff",
		echo.HeaderContentSecurityPolicy: "sandbox",
		"Cache-Control":                  cacheControl,
		"Accept-Ranges":                  "bytes",
		"Content-Length":                 "1000",
	}
	for k, want := range checks {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if rec.Body.Len() != 1000 {
		t.Fatalf("body length %d, want 1000", rec.Body.Len())
	}

	part := get(e, "/source/recovery-image-source-base.tar", map[string]string{"Range": "bytes=10-19"})
	if part.Code != http.StatusPartialContent {
		t.Fatalf("range request: status %d, want 206", part.Code)
	}
	if part.Body.Len() != 10 {
		t.Fatalf("range body length %d, want 10", part.Body.Len())
	}
	if got := part.Header().Get(echo.HeaderContentType); got != echo.MIMEOctetStream {
		t.Fatalf("range content type %q", got)
	}
}

func TestTraversalAndNonRegularAreRefused(t *testing.T) {
	dir := offerDir(t)
	// A real file one level up, which a traversal would reach.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newServer(t, dir)
	for _, target := range []string{
		"/source/..%2Foutside.txt",
		"/source/..%5Coutside.txt",
		"/source/%2E%2E",
		"/source/a..b",
		"/source/.hidden",
		"/source/link",
		"/source/sub",
		"/source/missing.tar",
		"/source/bad%22name",
	} {
		rec := get(e, target, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: served the file outside the directory", target)
		}
	}
	// A literal `..` segment reaches the router uncleaned; whatever it
	// matches, it must not serve anything.
	rec := get(e, "/source/../outside.txt", nil)
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("literal traversal served the outside file (status %d)", rec.Code)
	}
}

func TestSafeName(t *testing.T) {
	for name, want := range map[string]bool{
		"recovery-image-source-buildroot-2026.02.3+br2-0000000+20260101.tar": true,
		"SHA256SUMS": true,
		"a b.tar":    true,
		"":           false,
		".":          false,
		"..":         false,
		".git":       false,
		"a/b":        false,
		`a\b`:        false,
		"a..b":       false,
		"a\"b":       false,
		"a\nb":       false,
	} {
		if got := safeName(name); got != want {
			t.Errorf("safeName(%q) = %v, want %v", name, got, want)
		}
	}
}
