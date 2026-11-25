// Package audit serves the audit trail's read route. Writing goes through
// application/services/audit.Recorder from the services that mutate things;
// this handler is the one place the trail is read back.
package audit

import (
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/audit_event_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// Handler serves GET /api/v1/audit-events.
type Handler struct {
	reader *audit_service.Reader
	logger *slog.Logger
}

// NewHandler wires the audit read handler. The reader is required — a handler
// registered with nothing behind it would answer 500 on a route the document
// promises.
func NewHandler(reader *audit_service.Reader, logger *slog.Logger) *Handler {
	if reader == nil {
		panic("audit handler requires a reader")
	}
	return &Handler{reader: reader, logger: logger.With("handler", "audit")}
}

// RegisterRoutes wires GET /api/v1/audit-events behind audit.read.
//
// audit.read sits with account.export: granted by no role below super_admin,
// because the trail names every member's privileged actions across the whole
// account. Widening it is a product decision, not a matrix edit.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	g := e.Group("/api/v1/audit-events")
	g.Use(authMiddleware)
	g.GET("", h.List, middleware.RequireAction(enforcer, authz.ActionAuditRead))
}

// List returns one page of the account's audit trail
// @Summary List audit events
// @Description Returns the account's audit trail, newest first, server-paginated. Optional exact-match filters: `action` (e.g. `node.delete`, `node_command.reboot`) and `actor` (a user id). Events record who did what to which target; detail carries names, kinds and flags only — never credential material. Requires audit.read (super_admin).
// @Tags audit
// @Produce json
// @Security ApiKeyAuth
// @Param action query string false "Exact action string to filter by"
// @Param actor query string false "Actor user id to filter by"
// @Param page query int false "Page number (1-based, default 1)"
// @Param per_page query int false "Page size (default 50, max 200)"
// @Success 200 {object} results.APIResponse{data=results.AuditEventListResult} "One page of audit events"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/audit-events [get]
func (h *Handler) List(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var actorFilter *uuid.UUID
	if actorParam := c.QueryParam("actor"); actorParam != "" {
		actorID, err := uuid.Parse(actorParam)
		if err != nil {
			return results.SendValidationError(c, "invalid actor id")
		}
		actorFilter = &actorID
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	perPage, _ := strconv.Atoi(c.QueryParam("per_page"))

	query := audit_event_list.AuditEventListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
		Action:      c.QueryParam("action"),
		ActorUserID: actorFilter,
		Page:        page,
		PerPage:     perPage,
	}
	// Normalize HERE so the envelope echoes the EFFECTIVE page/per_page (the
	// executions list sets the precedent and the reasoning).
	if err := query.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	events, total, err := h.reader.List(c.Request().Context(), query)
	if err != nil {
		h.logger.ErrorContext(c.Request().Context(), "failed to list audit events",
			"error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list audit events", err)
	}

	return results.SendOKResponse(c,
		results.NewAuditEventListResult(events, total, query.Page, query.PerPage),
		"audit events retrieved")
}
