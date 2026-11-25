package ssh_key

import (
	"errors"
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_generate"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/ssh_key_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/ssh_key_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	ssh_key_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/ssh_key"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// SSHKeyHandler handles SSH key-related HTTP requests
type SSHKeyHandler struct {
	sshKeyService *ssh_key_service.Service
	cfg           *config.Config
	logger        *slog.Logger
}

// NewSSHKeyHandler creates a new SSH key handler
func NewSSHKeyHandler(sshKeyService *ssh_key_service.Service, cfg *config.Config, logger *slog.Logger) *SSHKeyHandler {
	return &SSHKeyHandler{
		sshKeyService: sshKeyService,
		cfg:           cfg,
		logger:        logger.With("handler", "ssh_key"),
	}
}

// RegisterRoutes registers SSH key routes
func (h *SSHKeyHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	sshKeys := e.Group("/api/v1/ssh-keys")
	sshKeys.Use(authMiddleware)

	// See the note on ActionImageRead in the image handler: enforcing the read
	// action is free today and only stays free while there are no OU-scoped
	// bindings.
	read := middleware.RequireAction(enforcer, authz.ActionSSHKeyRead)
	manage := middleware.RequireAction(enforcer, authz.ActionSSHKeyManage)
	sshKeys.POST("", h.Create, manage)
	sshKeys.POST("/generate", h.Generate, manage)
	sshKeys.GET("/:id", h.Get, read)
	sshKeys.GET("/:id/public-key", h.DownloadPublicKey, read)
	sshKeys.GET("", h.List, read)
	sshKeys.PUT("/:id", h.Update, manage)
	sshKeys.DELETE("/:id", h.Delete, manage)
}

// Create creates a new SSH key
// @Summary Create SSH key
// @Description Creates a new SSH public key
// @Tags ssh-keys
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param ssh_key body ssh_key_create.SSHKeyCreateCommand true "SSH key creation data"
// @Success 201 {object} results.APIResponse{data=results.SSHKeyResult} "SSH key created successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/ssh-keys [post]
func (h *SSHKeyHandler) Create(c echo.Context) error {
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

	var cmd ssh_key_create.SSHKeyCreateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context and generate ID
	cmd.ID = uuid.New()
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	key, err := h.sshKeyService.Create(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to create SSH key", err, "account_id", accountID)
		if errors.Is(err, app_errors.ErrInvalidSSHKeyFormat) {
			return results.SendValidationError(c, err.Error())
		}
		return results.SendInternalServerError(c, "failed to create SSH key", err)
	}

	keyResult := results.NewSSHKeyResult(key)
	return results.SendCreatedResponse(c, keyResult, "SSH key created successfully")
}

// Generate generates a new server-side connection keypair (ed25519)
// @Summary Generate SSH connection keypair
// @Description Generates a new ed25519 keypair server-side. The public part is stored (and deployed to nodes) like any SSH key; the private part is encrypted at rest and used by the platform for outbound SSH authentication (SSH-adopt). The private key is never returned — only the public part is downloadable.
// @Tags ssh-keys
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param ssh_key body ssh_key_generate.SSHKeyGenerateCommand true "Connection keypair parameters (alias, tags)"
// @Success 201 {object} results.APIResponse{data=results.SSHKeyResult} "Connection keypair created"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/ssh-keys/generate [post]
func (h *SSHKeyHandler) Generate(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var cmd ssh_key_generate.SSHKeyGenerateCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.ID = uuid.New()
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	key, err := h.sshKeyService.GenerateConnectionKey(c.Request().Context(), cmd)
	if err != nil {
		h.logger.Error("failed to generate SSH connection key", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to generate SSH key", err)
	}

	keyResult := results.NewSSHKeyResult(key)
	return results.SendCreatedResponse(c, keyResult, "SSH connection keypair created")
}

// DownloadPublicKey returns the public key text (authorized_keys line)
// @Summary Download SSH public key
// @Description Returns the public key text (authorized_keys line) so it can be placed into a device's authorized_keys — handy for pre-installing a connection key's public part on devices with password auth disabled.
// @Tags ssh-keys
// @Produce text/plain
// @Security ApiKeyAuth
// @Param id path string true "SSH key ID"
// @Success 200 {string} string "Public key content"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "SSH key not found"
// @Router /api/v1/ssh-keys/{id}/public-key [get]
func (h *SSHKeyHandler) DownloadPublicKey(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	keyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid SSH key ID")
	}

	pub, err := h.sshKeyService.GetPublicKey(c.Request().Context(), accountID, keyID)
	if err != nil {
		if errors.Is(err, app_errors.ErrSSHKeyNotFound) {
			return results.SendNotFoundError(c, "SSH key not found")
		}
		return results.SendInternalServerError(c, "failed to load SSH key", err)
	}

	c.Response().Header().Set("Content-Type", "text/plain")
	c.Response().Header().Set("Content-Disposition", "attachment; filename=ssh-key-"+keyID.String()+".pub")
	return c.String(200, pub+"\n")
}

// Get retrieves an SSH key by ID
// @Summary Get SSH key by ID
// @Description Retrieves SSH key details by ID (without key content)
// @Tags ssh-keys
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "SSH Key ID"
// @Success 200 {object} results.APIResponse{data=results.SSHKeyResult} "SSH key retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "SSH key not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/ssh-keys/{id} [get]
func (h *SSHKeyHandler) Get(c echo.Context) error {
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

	// Parse SSH key ID from URL parameter
	keyIDStr := c.Param("id")
	keyID, err := uuid.Parse(keyIDStr)
	if err != nil {
		h.logger.Warn("invalid SSH key ID", "error", err, "key_id", keyIDStr)
		return results.SendValidationError(c, "invalid SSH key ID")
	}

	query := ssh_key_get.SSHKeyGetQuery{
		ID:          keyID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	key, err := h.sshKeyService.GetByID(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to get SSH key", "error", err, "key_id", keyID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrSSHKeyNotFound) {
			return results.SendNotFoundError(c, "SSH key not found")
		}
		return results.SendInternalServerError(c, "failed to get SSH key", err)
	}

	keyResult := results.NewSSHKeyResult(key)
	return results.SendOKResponse(c, keyResult, "SSH key retrieved successfully")
}

// List retrieves all SSH keys for the account
// @Summary List SSH keys
// @Description Retrieves a list of all SSH keys for the authenticated account
// @Tags ssh-keys
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.SSHKeyListResult} "SSH keys retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/ssh-keys [get]
func (h *SSHKeyHandler) List(c echo.Context) error {
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

	query := ssh_key_list.SSHKeyListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	keys, err := h.sshKeyService.GetAllByAccountID(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to list SSH keys", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list SSH keys", err)
	}

	// Convert to results
	keyResults := make([]results.SSHKeyResult, len(keys))
	for i, key := range keys {
		keyResults[i] = results.NewSSHKeyResult(&key)
	}

	listResult := results.NewSSHKeyListResult(keyResults)
	return results.SendOKResponse(c, listResult, "SSH keys retrieved successfully")
}

// Update updates an existing SSH key
// @Summary Update SSH key
// @Description Updates SSH key information (alias, key content, or default status). The key content of a generated connection keypair (has_private_key) cannot be replaced: 400 ssh_key_has_private_key.
// @Tags ssh-keys
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "SSH Key ID"
// @Param ssh_key body ssh_key_update.SSHKeyUpdateCommand true "SSH key update data"
// @Success 200 {object} results.APIResponse{data=results.SSHKeyResult} "SSH key updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "SSH key not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/ssh-keys/{id} [put]
func (h *SSHKeyHandler) Update(c echo.Context) error {
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

	// Parse SSH key ID from URL parameter
	keyIDStr := c.Param("id")
	keyID, err := uuid.Parse(keyIDStr)
	if err != nil {
		h.logger.Warn("invalid SSH key ID", "error", err, "key_id", keyIDStr)
		return results.SendValidationError(c, "invalid SSH key ID")
	}

	var cmd ssh_key_update.SSHKeyUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context and SSH key ID
	cmd.ID = keyID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	key, err := h.sshKeyService.Update(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to update SSH key", err, "key_id", keyID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrSSHKeyNotFound) {
			return results.SendNotFoundError(c, "SSH key not found")
		}
		if errors.Is(err, app_errors.ErrInvalidSSHKeyFormat) {
			return results.SendValidationError(c, err.Error())
		}
		return results.SendInternalServerError(c, "failed to update SSH key", err)
	}

	keyResult := results.NewSSHKeyResult(key)
	return results.SendOKResponse(c, keyResult, "SSH key updated successfully")
}

// Delete deletes an SSH key
// @Summary Delete SSH key
// @Description Deletes an SSH key by ID
// @Tags ssh-keys
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "SSH Key ID"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "SSH key not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/ssh-keys/{id} [delete]
func (h *SSHKeyHandler) Delete(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	actorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse SSH key ID from URL parameter
	keyIDStr := c.Param("id")
	keyID, err := uuid.Parse(keyIDStr)
	if err != nil {
		h.logger.Warn("invalid SSH key ID", "error", err, "key_id", keyIDStr)
		return results.SendValidationError(c, "invalid SSH key ID")
	}

	err = h.sshKeyService.Delete(c.Request().Context(), accountID, actorUserID, keyID)
	if err != nil {
		h.logger.Error("failed to delete SSH key", "error", err, "key_id", keyID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrSSHKeyNotFound) {
			return results.SendNotFoundError(c, "SSH key not found")
		}
		return results.SendInternalServerError(c, "failed to delete SSH key", err)
	}

	return results.SendNoContentResponse(c)
}
