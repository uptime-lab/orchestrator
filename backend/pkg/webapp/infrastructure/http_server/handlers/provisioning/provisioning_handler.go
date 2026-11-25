package provisioning

import (
	"archive/zip"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	master_token_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/master_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// AdoptionFileName is the file the agent reads its adoption config from when the
// SD/USB boot partition is mounted (see the agent's S99agent init script).
const AdoptionFileName = "node-adoption.txt"

// defaultProvisioningTokenTTL bounds the lifetime of a master token minted for
// an SD/USB provisioning bundle when the caller did not specify an expiry.
const defaultProvisioningTokenTTL = 7 * 24 * time.Hour

// ProvisioningHandler serves device-provisioning bundles.
type ProvisioningHandler struct {
	masterTokenService *master_token_service.Service
	cfg                *config.Config
	logger             *slog.Logger
}

// NewProvisioningHandler creates a new provisioning handler.
func NewProvisioningHandler(masterTokenService *master_token_service.Service, cfg *config.Config, logger *slog.Logger) *ProvisioningHandler {
	if masterTokenService == nil {
		panic("provisioning handler requires a master token service")
	}
	if cfg == nil {
		panic("provisioning handler requires a config")
	}
	return &ProvisioningHandler{
		masterTokenService: masterTokenService,
		cfg:                cfg,
		logger:             logger.With("handler", "provisioning"),
	}
}

// RegisterRoutes registers provisioning routes.
func (h *ProvisioningHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	group := e.Group("/api/v1/provisioning")
	group.Use(authMiddleware)

	// Minting an SD boot bundle creates a master token: super_admin only.
	group.POST("/sd", h.DownloadSDImage, middleware.RequireAction(enforcer, authz.ActionProvisioningSD))
}

// DownloadSDImage generates and streams a ZIP bundle for provisioning a
// factory-clean device from an SD card or USB drive.
// @Summary Download SD/USB provisioning bundle
// @Description Creates a new master token and streams a ZIP containing the boot-partition
// @Description files plus a node-adoption.txt bound to that token. Unzip its contents onto a
// @Description FAT32 SD card or USB drive; the device boots the bootstrap image, the agent
// @Description reads the adoption file and registers the node for approval. The token value is
// @Description embedded only in the download and is not retrievable afterwards.
// @Tags provisioning
// @Accept json
// @Produce application/zip
// @Security ApiKeyAuth
// @Param master_token body master_token_create.MasterTokenCreateCommand false "Master token parameters (name, default credentials, ssh key, expiry)"
// @Success 200 {file} binary "ZIP provisioning bundle"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/provisioning/sd [post]
func (h *ProvisioningHandler) DownloadSDImage(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Validate the payload directory up front, before creating a token, so a
	// misconfigured server doesn't leave dangling master tokens behind.
	payloadDir := h.cfg.Provisioning.SDPayloadDir
	if payloadDir == "" {
		h.logger.Error("SD provisioning payload directory is not configured")
		return results.SendInternalServerError(c, "SD provisioning is not configured on this server", fmt.Errorf("empty PROVISIONING_SD_PAYLOAD_DIR"))
	}
	files, err := CollectPayloadFiles(payloadDir)
	if err != nil {
		h.logger.Error("failed to read SD provisioning payload directory", "error", err, "dir", payloadDir)
		return results.SendInternalServerError(c, "SD provisioning payload is unavailable", err)
	}
	if len(files) == 0 {
		h.logger.Error("SD provisioning payload directory is empty", "dir", payloadDir)
		return results.SendInternalServerError(c, "SD provisioning payload is empty", fmt.Errorf("no files in %s", payloadDir))
	}

	var cmd master_token_create.MasterTokenCreateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}
	if strings.TrimSpace(cmd.Name) == "" {
		cmd.Name = "sd-provisioning"
	}
	// Bound the lifetime of provisioning tokens: an SD/USB bundle grants
	// account-wide node registration, so a leaked card must not stay valid
	// forever. Default to a limited window unless the caller set one explicitly.
	if cmd.ExpiresAt == nil {
		expiry := time.Now().Add(defaultProvisioningTokenTTL)
		cmd.ExpiresAt = &expiry
	}
	cmd.ID = uuid.New()
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	token, err := h.masterTokenService.Create(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to create master token for provisioning", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to create master token", err)
	}

	adoptionFile, err := h.masterTokenService.GetAdoptionInfo(
		c.Request().Context(),
		accountID,
		token.ID,
		token.Token, // raw token value, only available at creation time
		h.platformURL(c),
		h.grpcEndpoint(),
	)
	if err != nil {
		h.logger.Error("failed to generate adoption file", "error", err, "token_id", token.ID)
		return results.SendInternalServerError(c, "failed to generate adoption file", err)
	}

	// Stream the ZIP directly to the response.
	c.Response().Header().Set(echo.HeaderContentType, "application/zip")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="orchestrator-sd-provisioning.zip"`)
	c.Response().WriteHeader(200)

	if err := WriteSDBundle(c.Response(), payloadDir, files, adoptionFile); err != nil {
		// Headers are already sent, so we can only abort the stream and log.
		h.logger.Error("failed to stream provisioning bundle", "error", err)
		return err
	}

	h.logger.Info("SD provisioning bundle generated", "token_id", token.ID, "account_id", accountID, "files", len(files))
	return nil
}

// platformURL returns the HTTP platform URL to embed in the adoption file,
// preferring the configured advertisement URL and falling back to the request host.
func (h *ProvisioningHandler) platformURL(c echo.Context) string {
	if h.cfg.Server.AdvertisementURL != "" {
		return h.cfg.Server.AdvertisementURL
	}
	// c.Scheme() honors X-Forwarded-Proto (Request().TLS is nil behind the
	// TLS-terminating proxy).
	return c.Scheme() + "://" + c.Request().Host
}

// grpcEndpoint returns the gRPC endpoint to embed in the adoption file,
// preferring the configured advertisement URL and falling back to host:port.
func (h *ProvisioningHandler) grpcEndpoint() string {
	if h.cfg.GRPC.AdvertisementURL != "" {
		return h.cfg.GRPC.AdvertisementURL
	}
	return net.JoinHostPort(h.cfg.GRPC.Host, fmt.Sprintf("%d", h.cfg.GRPC.Port))
}

// WriteSDBundle streams a provisioning ZIP into w: every regular file under
// payloadDir (verbatim) plus a generated AdoptionFileName entry holding
// adoptionFileContent. Shared by the master-token bundle and the per-node
// bundle so both produce an identical, agent-readable boot layout.
func WriteSDBundle(w io.Writer, payloadDir string, files []string, adoptionFileContent string) error {
	zw := zip.NewWriter(w)
	for _, f := range files {
		if err := writeFileToZip(zw, payloadDir, f); err != nil {
			_ = zw.Close()
			return fmt.Errorf("add payload file %s: %w", f, err)
		}
	}
	aw, err := zw.Create(AdoptionFileName)
	if err != nil {
		_ = zw.Close()
		return fmt.Errorf("add adoption file: %w", err)
	}
	if _, err := aw.Write([]byte(adoptionFileContent)); err != nil {
		_ = zw.Close()
		return fmt.Errorf("write adoption file: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finalize bundle: %w", err)
	}
	return nil
}

// CollectPayloadFiles returns the relative paths of all regular files under dir.
func CollectPayloadFiles(dir string) ([]string, error) {
	var rel []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		r, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = append(rel, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rel, nil
}

// writeFileToZip copies dir/relPath into the zip under relPath (forward slashes).
func writeFileToZip(zw *zip.Writer, dir, relPath string) error {
	src, err := os.Open(filepath.Join(dir, relPath))
	if err != nil {
		return err
	}
	defer src.Close()

	w, err := zw.Create(filepath.ToSlash(relPath))
	if err != nil {
		return err
	}
	_, err = io.Copy(w, src)
	return err
}
