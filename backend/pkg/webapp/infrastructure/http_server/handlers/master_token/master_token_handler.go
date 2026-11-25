package master_token

import (
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/master_token_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/master_token_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	master_token_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/master_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// MasterTokenHandler handles master token-related HTTP requests
type MasterTokenHandler struct {
	masterTokenService *master_token_service.Service
	cfg                *config.Config
	logger             *slog.Logger
}

// NewMasterTokenHandler creates a new master token handler
func NewMasterTokenHandler(masterTokenService *master_token_service.Service, cfg *config.Config, logger *slog.Logger) *MasterTokenHandler {
	return &MasterTokenHandler{
		masterTokenService: masterTokenService,
		cfg:                cfg,
		logger:             logger.With("handler", "master_token"),
	}
}

// RegisterRoutes registers master token routes
func (h *MasterTokenHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	masterTokens := e.Group("/api/v1/master-tokens")
	masterTokens.Use(authMiddleware)

	// Master tokens are account-wide adoption credentials: super_admin only.
	// The GETs carry the same action as the mutations — the list and detail
	// responses describe credential material, not tenant inventory.
	manage := middleware.RequireAction(enforcer, authz.ActionMasterTokenManage)
	masterTokens.POST("", h.Create, manage)
	masterTokens.GET("/:id", h.Get, manage)
	masterTokens.GET("", h.List, manage)
	masterTokens.PUT("/:id", h.Update, manage)
	masterTokens.DELETE("/:id", h.Delete, manage)
}

// Create creates a new master token
// @Summary Create master token
// @Description Creates a new master token for agent authentication. Tokens expire 7 days after creation unless expires_at is set explicitly; the computed expiry is returned in the response.
// @Tags master-tokens
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param master_token body master_token_create.MasterTokenCreateCommand true "Master token creation data"
// @Success 201 {object} results.APIResponse{data=results.MasterTokenResult} "Master token created successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/master-tokens [post]
func (h *MasterTokenHandler) Create(c echo.Context) error {
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

	var cmd master_token_create.MasterTokenCreateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context and generate ID
	cmd.ID = uuid.New()
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	token, err := h.masterTokenService.Create(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to create master token", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to create master token", err)
	}

	// Generate adoption file content
	// Get platform URL - use advertisement URL if configured, otherwise use request host
	var platformURL string
	if h.cfg.Server.AdvertisementURL != "" {
		platformURL = h.cfg.Server.AdvertisementURL
	} else {
		// c.Scheme() honors X-Forwarded-Proto — behind the TLS-terminating
		// proxy Request().TLS is nil and would mislabel the origin as http.
		platformURL = c.Scheme() + "://" + c.Request().Host
	}

	// Construct gRPC endpoint - use advertisement URL if configured, otherwise use host:port
	var grpcEndpoint string
	if h.cfg.GRPC.AdvertisementURL != "" {
		grpcEndpoint = h.cfg.GRPC.AdvertisementURL
	} else {
		grpcEndpoint = net.JoinHostPort(h.cfg.GRPC.Host, fmt.Sprintf("%d", h.cfg.GRPC.Port))
	}

	// Generate adoption file with the actual token value (only available at creation time)
	adoptionFile, err := h.masterTokenService.GetAdoptionInfo(
		c.Request().Context(),
		accountID,
		token.ID,
		token.Token, // The actual token value
		platformURL,
		grpcEndpoint,
	)
	if err != nil {
		h.logger.Warn("failed to generate adoption file", "error", err, "token_id", token.ID)
		// Don't fail the request, just log the warning
	}

	tokenResult := results.NewMasterTokenResult(token)
	tokenResult.AdoptionFile = adoptionFile
	return results.SendCreatedResponse(c, tokenResult, "Master token created successfully. Save the token now - it won't be shown again.")
}

// Get retrieves a master token by ID
// @Summary Get master token by ID
// @Description Retrieves master token details by ID (without the actual token value)
// @Tags master-tokens
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Master Token ID"
// @Success 200 {object} results.APIResponse{data=results.MasterTokenResult} "Master token retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Master token not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/master-tokens/{id} [get]
func (h *MasterTokenHandler) Get(c echo.Context) error {
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

	// Parse token ID from URL parameter
	tokenIDStr := c.Param("id")
	tokenID, err := uuid.Parse(tokenIDStr)
	if err != nil {
		h.logger.Warn("invalid master token ID", "error", err, "token_id", tokenIDStr)
		return results.SendValidationError(c, "invalid master token ID")
	}

	query := master_token_get.MasterTokenGetQuery{
		ID:          tokenID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	token, err := h.masterTokenService.GetByID(c.Request().Context(), query)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to get master token", err, "token_id", tokenID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrMasterTokenNotFound) {
			return results.SendNotFoundError(c, "master token not found")
		}
		return results.SendInternalServerError(c, "failed to get master token", err)
	}

	tokenResult := results.NewMasterTokenResult(token)
	tokenResult.Token = "" // Never return token value on get
	return results.SendOKResponse(c, tokenResult, "Master token retrieved successfully")
}

// List retrieves all master tokens for the account
// @Summary List master tokens
// @Description Retrieves a list of all master tokens for the authenticated account
// @Tags master-tokens
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.MasterTokenListResult} "Master tokens retrieved successfully"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/master-tokens [get]
func (h *MasterTokenHandler) List(c echo.Context) error {
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

	query := master_token_list.MasterTokenListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	tokens, err := h.masterTokenService.GetAllByAccountID(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to list master tokens", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list master tokens", err)
	}

	listResult := results.NewMasterTokenListResult(tokens)
	return results.SendOKResponse(c, listResult, "Master tokens retrieved successfully")
}

// Update updates an existing master token
// @Summary Update master token
// @Description Updates master token information (name, is_active, expires_at)
// @Tags master-tokens
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Master Token ID"
// @Param master_token body master_token_update.MasterTokenUpdateCommand true "Master token update data"
// @Success 200 {object} results.APIResponse{data=results.MasterTokenResult} "Master token updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Master token not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/master-tokens/{id} [put]
func (h *MasterTokenHandler) Update(c echo.Context) error {
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

	// Parse token ID from URL parameter
	tokenIDStr := c.Param("id")
	tokenID, err := uuid.Parse(tokenIDStr)
	if err != nil {
		h.logger.Warn("invalid master token ID", "error", err, "token_id", tokenIDStr)
		return results.SendValidationError(c, "invalid master token ID")
	}

	var cmd master_token_update.MasterTokenUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context and token ID
	cmd.ID = tokenID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	token, err := h.masterTokenService.Update(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to update master token", err, "token_id", tokenID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrMasterTokenNotFound) {
			return results.SendNotFoundError(c, "master token not found")
		}
		return results.SendInternalServerError(c, "failed to update master token", err)
	}

	tokenResult := results.NewMasterTokenResult(token)
	tokenResult.Token = "" // Never return token value on update
	return results.SendOKResponse(c, tokenResult, "Master token updated successfully")
}

// Delete deletes a master token
// @Summary Delete master token
// @Description Deletes a master token by ID
// @Tags master-tokens
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Master Token ID"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Master token not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/master-tokens/{id} [delete]
func (h *MasterTokenHandler) Delete(c echo.Context) error {
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

	// Parse token ID from URL parameter
	tokenIDStr := c.Param("id")
	tokenID, err := uuid.Parse(tokenIDStr)
	if err != nil {
		h.logger.Warn("invalid master token ID", "error", err, "token_id", tokenIDStr)
		return results.SendValidationError(c, "invalid master token ID")
	}

	err = h.masterTokenService.Delete(c.Request().Context(), accountID, actorUserID, tokenID)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to delete master token", err, "token_id", tokenID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrMasterTokenNotFound) {
			return results.SendNotFoundError(c, "master token not found")
		}
		return results.SendInternalServerError(c, "failed to delete master token", err)
	}

	return results.SendNoContentResponse(c)
}
