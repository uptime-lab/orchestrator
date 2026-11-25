package cdn

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/agentpkg"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
)

// CDNHandler handles CDN file serving requests.
//
// Its error bodies are DELIBERATELY the bare `{"error": "..."}` map rather
// than the results envelope every API route answers with: these are public,
// tokenless endpoints whose consumers are a boot ROM, the agent's package
// fetch and curl — none of which read a success/message/timestamp envelope —
// and the @Failure annotations below describe that bare shape on purpose.
type CDNHandler struct {
	storageClient storage.StorageClient
	logger        *slog.Logger
	cfg           *config.Config
}

// NewCDNHandler creates a new CDN handler
func NewCDNHandler(storageClient storage.StorageClient, cfg *config.Config, logger *slog.Logger) *CDNHandler {
	return &CDNHandler{
		storageClient: storageClient,
		logger:        logger.With("handler", "cdn"),
		cfg:           cfg,
	}
}

var (
	// semver (optionally with v prefix and simple pre-release)
	semverRegex       = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	allowedArchs      = map[string]bool{"amd64": true, "arm64": true}
	allowedPkgTypes   = map[string]bool{"deb": true, "rpm": true, "tar.gz": true}
	pkgTypeExtensions = map[string]struct {
		Ext         string
		ContentType string
	}{
		"deb":    {Ext: "deb", ContentType: "application/vnd.debian.binary-package"},
		"rpm":    {Ext: "rpm", ContentType: "application/x-rpm"},
		"tar.gz": {Ext: "tar.gz", ContentType: "application/gzip"},
	}
)

// ServeFile serves a file from storage
// @Summary Serve file from CDN
// @Description Serves a file from the platform's storage backend (local filesystem or S3-compatible object storage). The path parameter captures everything after /cdn/
// @Tags cdn
// @Accept json
// @Produce application/octet-stream
// @Param path path string true "File path (everything after /cdn/)"
// @Success 200 {file} file "File content"
// @Failure 400 {object} map[string]string "Bad request"
// @Failure 404 {object} map[string]string "File not found"
// @Failure 500 {object} map[string]string "Internal server error"
// @Router /cdn/{path} [get]
func (h *CDNHandler) ServeFile(c echo.Context) error {
	// Only backends this CDN can proxy — see storage.ServedThroughCDN.
	if !storage.ServedThroughCDN(h.storageClient.GetStorageType()) {
		h.logger.Error("CDN endpoint cannot proxy this storage backend", "storage_type", h.storageClient.GetStorageType())
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "CDN cannot serve the configured storage backend"})
	}

	// Extract file path from URL (everything after /cdn/)
	filePath := c.Param("*")
	if filePath == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "file path is required"})
	}

	// Remove leading slash if present
	filePath = strings.TrimPrefix(filePath, "/")

	// Verify the HMAC signature so /cdn is a time-bounded capability rather than
	// a permanent public link. URLs are minted by the image service's GetSignedURL.
	signingKey := cdnsign.DeriveKey(h.cfg.CDNSigningSecret())
	if err := cdnsign.Verify(
		filePath,
		c.QueryParam(cdnsign.ParamExpires),
		c.QueryParam(cdnsign.ParamSignature),
		signingKey,
	); err != nil {
		h.logger.Warn("rejected unsigned or expired CDN request", "path", filePath)
		return c.JSON(http.StatusForbidden, map[string]string{"error": "invalid or expired URL"})
	}

	// Get file info first to set proper headers
	fileInfo, err := h.storageClient.GetFileInfo(c.Request().Context(), filePath)
	if err != nil {
		h.logger.Error("failed to get file info", "error", err, "path", filePath)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to get file info"})
	}

	if fileInfo == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "file not found"})
	}

	// Retrieve file from storage
	reader, err := h.storageClient.Retrieve(c.Request().Context(), filePath)
	if err != nil {
		h.logger.Error("failed to retrieve file", "error", err, "path", filePath)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to retrieve file"})
	}
	defer reader.Close()

	// Everything this route serves is a tenant-uploaded object delivered from
	// the APPLICATION's origin (the reverse proxy routes /cdn/ to the backend
	// under the same host as the SPA), so the response is forced inert
	// (octet-stream, nosniff, sandbox CSP) whatever the object claims to be:
	// a stored Content-Type is client-influenced and must never be reflected.
	// The proxy in front is not relied on for these headers, which is why
	// nothing below is conditional on the object.
	c.Response().Header().Set("Content-Type", "application/octet-stream")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	c.Response().Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")

	c.Response().Header().Set("Content-Length", strconv.FormatInt(fileInfo.Size, 10))
	// Never storable by a shared cache: the URL is a time-bounded capability to
	// one account's object, and a proxy keeping the body would go on serving it
	// after the signature expired. The agent packages below are public by
	// design and keep their shared lifetime.
	c.Response().Header().Set("Cache-Control", "private, no-store")
	c.Response().Header().Set("ETag", fileInfo.ETag)

	// Always an attachment. The default name is the object's own base name
	// (a UUID plus extension); an explicit ?filename= wins because it is how
	// an image download lands under the name it was uploaded with. That
	// parameter is not signed — see cdnsign.ParamFileName — so it is sanitized
	// here, at the point it enters a header, and an unusable value falls back
	// to the default rather than failing the transfer. A caller who tampers
	// with it only relabels bytes their capability already entitles them to.
	// A content_disposition in object METADATA is deliberately not consulted:
	// it is written by the same upload path as the type was.
	name := cdnsign.SanitizeFileName(path.Base(filePath))
	if requested := c.QueryParam(cdnsign.ParamFileName); requested != "" {
		if safe := cdnsign.SanitizeFileName(requested); safe != "" {
			name = safe
		}
	}
	if name == "" {
		name = "download"
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)

	// Stream file content to response
	c.Response().Status = http.StatusOK
	if _, err := io.Copy(c.Response().Writer, reader); err != nil {
		h.logger.Error("failed to stream file", "error", err, "path", filePath)
		return err
	}

	return nil
}

// ServeAgentPackage serves agent binaries via a stable CDN-style URL:
//
//	GET /cdn/agent/:version/:arch/:pkgType
//
// It maps to files stored in a flat structure:
//
//	{AgentCDNBasePath}/orchestrator-agent_{version}_linux_{arch}.{ext}
//
// Note: "latest" is treated as a literal version string, not resolved to the highest version.
// A request for /cdn/agent/latest/arm64/deb will look for orchestrator-agent_latest_linux_arm64.deb exactly.
//
// @Summary Download agent package
// @Description Serves orchestrator agent packages (deb, rpm, tar.gz) by version, architecture, and package type. Files are stored in a flat structure with the pattern: orchestrator-agent_{version}_linux_{arch}.{ext}. The "latest" version is treated as a literal string, not resolved to the highest version.
// @Tags cdn
// @Accept json
// @Produce application/octet-stream
// @Param version path string true "Version (semantic version like 1.2.3 or 'latest')" example(1.2.3)
// @Param arch path string true "Architecture" Enums(amd64, arm64) example(arm64)
// @Param pkgType path string true "Package type" Enums(deb, rpm, tar.gz) example(deb)
// @Success 200 {file} file "Agent package file"
// @Failure 400 {object} map[string]string "Bad request (invalid version, arch, or package type)"
// @Failure 404 {object} map[string]string "Agent package not found"
// @Failure 500 {object} map[string]string "Internal server error"
// @Router /cdn/agent/{version}/{arch}/{pkgType} [get]
func (h *CDNHandler) ServeAgentPackage(c echo.Context) error {
	return h.serveAgentStylePackage(c, agentpkg.AgentPackage)
}

// ServeWatcherPackage serves the telemetry-only Watcher/watcher agent
// from the same shelf and with the same rules as the full agent:
//
//	GET /cdn/watcher/:version/:arch/:pkgType
//	  -> {AgentCDNBasePath}/orchestrator-watcher_{version}_linux_{arch}.{ext}
//
// A separate route rather than a query flag so the recovery agent's URL
// construction stays a plain sprintf, and so /cdn/agent-versions (which the
// one-click update trusts) keeps listing the FULL agent only.
//
// @Summary Download Watcher (watcher) agent package
// @Description Serves the telemetry-only watcher agent packages by version, architecture, and package type. Same flat naming as the full agent with the orchestrator-watcher prefix; "latest" is a literal.
// @Tags cdn
// @Accept json
// @Produce application/octet-stream
// @Param version path string true "Version (semantic version like 1.2.3 or 'latest')" example(1.2.3)
// @Param arch path string true "Architecture" Enums(amd64, arm64) example(arm64)
// @Param pkgType path string true "Package type" Enums(deb, rpm, tar.gz) example(deb)
// @Success 200 {file} file "Watcher package file"
// @Failure 400 {object} map[string]string "Bad request (invalid version, arch, or package type)"
// @Failure 404 {object} map[string]string "Watcher package not found"
// @Failure 500 {object} map[string]string "Internal server error"
// @Router /cdn/watcher/{version}/{arch}/{pkgType} [get]
func (h *CDNHandler) ServeWatcherPackage(c echo.Context) error {
	return h.serveAgentStylePackage(c, agentpkg.WatcherPackage)
}

func (h *CDNHandler) serveAgentStylePackage(c echo.Context, packageBase string) error {
	// Only support local storage for now (consistent with generic CDN)
	if !storage.ServedThroughCDN(h.storageClient.GetStorageType()) {
		h.logger.Error("agent CDN endpoint cannot proxy this storage backend", "storage_type", h.storageClient.GetStorageType())
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "agent CDN cannot serve the configured storage backend"})
	}

	version := c.Param("version")
	arch := c.Param("arch")
	pkgType := c.Param("pkgType")

	// A ".sig" suffix requests the detached artifact signature next to the package
	// (e.g. /cdn/agent/latest/arm64/deb.sig -> orchestrator-agent_..._arm64.deb.sig),
	// which the agent and install.sh verify before installing as root.
	wantSig := strings.HasSuffix(pkgType, ".sig")
	pkgType = strings.TrimSuffix(pkgType, ".sig")

	// Validate arch
	if !allowedArchs[arch] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid arch; allowed: amd64, arm64"})
	}

	// Validate pkgType
	if !allowedPkgTypes[pkgType] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid package type; allowed: deb, rpm, tar.gz"})
	}

	// Validate version: semantic or literal "latest" (no resolution, exact match only)
	if version == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "version is required"})
	}
	if version != "latest" && !semverRegex.MatchString(version) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid version; must be semantic version or 'latest'"})
	}

	// Map pkgType to extension and content type
	typeInfo, ok := pkgTypeExtensions[pkgType]
	if !ok {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "unsupported package type"})
	}

	// Build filename: orchestrator-agent_{version}_linux_{arch}.{ext}
	// "latest" is used as-is, no version resolution or calculation
	// (agentpkg owns the name, so the release import writes exactly this key)
	fileName := agentpkg.FileName(packageBase, version, arch, typeInfo.Ext)
	if wantSig {
		fileName += agentpkg.SignatureSuffix
	}

	// Build relative storage path: flat structure in AgentCDNBasePath
	relativePath := agentpkg.ObjectKey(h.cfg.Storage.AgentCDNBasePath, fileName)

	// Get file info
	fileInfo, err := h.storageClient.GetFileInfo(c.Request().Context(), relativePath)
	if err != nil {
		h.logger.Error("failed to get agent package info", "error", err, "path", relativePath)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to get agent package info"})
	}
	if fileInfo == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "agent package not found"})
	}

	// Retrieve file
	reader, err := h.storageClient.Retrieve(c.Request().Context(), relativePath)
	if err != nil {
		h.logger.Error("failed to retrieve agent package", "error", err, "path", relativePath)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to retrieve agent package"})
	}
	defer reader.Close()

	// Set headers
	contentType := typeInfo.ContentType
	if wantSig {
		contentType = "application/octet-stream" // detached signature
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Response().Header().Set("Content-Type", contentType)
	c.Response().Header().Set("Content-Length", strconv.FormatInt(fileInfo.Size, 10))
	c.Response().Header().Set("Cache-Control", "public, max-age=3600")
	c.Response().Header().Set("ETag", fileInfo.ETag)

	// Content-Disposition: attachment; filename="orchestrator-agent_{version}_linux_{arch}.{ext}"
	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))

	// Stream file
	c.Response().Status = http.StatusOK
	if _, err := io.Copy(c.Response().Writer, reader); err != nil {
		h.logger.Error("failed to stream agent package", "error", err, "path", relativePath)
		return err
	}

	return nil
}

// ListAgentVersions returns the agent package versions available in the agent CDN
// base path, so the UI can offer a dropdown instead of a free-text version (which
// let operators pick a non-existent version like the bootstrap "0.0.0").
//
// @Summary List available agent versions
// @Description Lists agent package versions available for install, and the concrete version the `latest` alias installs. `latest_by_arch` answers that per architecture (arm64, amd64): the version recorded when a release import moved that architecture's `latest`, else the newest version listed for it. `latest` is the arm64 answer (omitted when unknown). Compare a node against its architecture's answer, not against the top of the list, which can be a version imported without moving `latest`, or one imported for the other architecture only.
// @Tags cdn
// @Produce json
// @Success 200 {object} AgentVersionsResponse "Available versions and what latest installs"
// @Router /cdn/agent-versions [get]
func (h *CDNHandler) ListAgentVersions(c echo.Context) error {
	lister, ok := h.storageClient.(interface {
		ListAgentVersions(basePath string) ([]string, error)
	})
	if !ok {
		// Non-local storage: no listing; offer the always-present "latest".
		return c.JSON(http.StatusOK, AgentVersionsResponse{Versions: []string{agentpkg.LatestVersion}})
	}
	base := strings.Trim(h.cfg.Storage.AgentCDNBasePath, "/")
	versions, err := lister.ListAgentVersions(base)
	if err != nil {
		h.logger.Error("failed to list agent versions", "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list agent versions"})
	}
	if len(versions) == 0 {
		versions = []string{agentpkg.LatestVersion}
	}
	resp := AgentVersionsResponse{Versions: versions}
	if byArchLister, ok := h.storageClient.(interface {
		ListAgentVersionsByArch(basePath string) (map[string][]string, error)
	}); ok {
		byArch, err := byArchLister.ListAgentVersionsByArch(base)
		if err != nil {
			h.logger.Error("failed to list agent versions by architecture", "error", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list agent versions"})
		}
		resp.LatestByArch = agentpkg.ResolveLatestByArch(c.Request().Context(), h.storageClient, base, byArch)
		resp.Latest = resp.LatestByArch[agentpkg.DefaultArch]
	} else {
		resp.Latest = agentpkg.ResolveLatest(c.Request().Context(), h.storageClient, base, versions)
	}
	return c.JSON(http.StatusOK, resp)
}

// AgentVersionsResponse is the body of GET /cdn/agent-versions.
type AgentVersionsResponse struct {
	// Versions lists what can be installed, `latest` first when present, then
	// newest first.
	Versions []string `json:"versions"`
	// Latest is the concrete version the `latest` alias installs on arm64 —
	// the fleet's architecture, and the meaning the field has always had for
	// the SPA.
	Latest string `json:"latest,omitempty"`
	// LatestByArch is that answer per architecture: a release can move one
	// architecture's `latest` and not the other's.
	LatestByArch map[string]string `json:"latest_by_arch,omitempty"`
}
