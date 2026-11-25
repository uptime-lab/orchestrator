package account

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	account_export_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/account_export"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// ExportHandler serves the whole-account data export.
//
// A separate handler from AccountHandler on purpose: it depends on nine read
// sources and an audit recorder, none of which the account CRUD handler wants,
// and folding them in would make every account route constructible only with
// the export's dependencies satisfied.
type ExportHandler struct {
	exporter *account_export_service.Service
	audit    *audit_service.Recorder
	logger   *slog.Logger
}

// NewExportHandler wires the export handler. Both dependencies are required:
// an exporter with no audit recorder would let the entire account walk out
// leaving no record, which is the one thing this endpoint must not do.
func NewExportHandler(exporter *account_export_service.Service, auditRecorder *audit_service.Recorder, logger *slog.Logger) *ExportHandler {
	if exporter == nil {
		panic("account export handler requires an exporter")
	}
	if auditRecorder == nil {
		panic("account export handler requires an audit recorder")
	}
	return &ExportHandler{exporter: exporter, audit: auditRecorder, logger: logger.With("handler", "account_export")}
}

// RegisterRoutes wires GET /api/v1/account/export.
//
// Singular /account, not /accounts/:id: the export is always of the caller's
// own account, taken from the token. A path id would invite a caller to pass
// someone else's and make tenancy a check rather than a property.
func (h *ExportHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	g := e.Group("/api/v1/account")
	g.Use(authMiddleware)
	g.GET("/export", h.Export, middleware.RequireAction(enforcer, authz.ActionAccountExport))
}

// Export returns the account's whole configuration as one JSON document
// @Summary Export the account's data
// @Description Returns everything the account owns as a single JSON document, so a tenant can leave with their configuration. Carries configuration and metadata ONLY — no secret values, no SSH private keys, no access-entry passwords, no adoption or master tokens. OS image files are not included; the document lists them with their checksums and each is fetched from GET /api/v1/images/{id}/download. Sections that cannot be read are named in `omitted` rather than failing the whole export. Requires account.export (super_admin).
// @Tags accounts
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.AccountExportResult "The export document itself as a JSON download (Content-Disposition: attachment) — bare, not wrapped in the results.APIResponse envelope"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/account/export [get]
func (h *ExportHandler) Export(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	ctx := c.Request().Context()
	doc, err := h.exporter.Export(ctx, accountID, userID)
	if err != nil {
		h.logger.ErrorContext(ctx, "account export failed", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to build the account export", err)
	}

	// Recorded before the body is written, and recorded with what was omitted:
	// "the account was exported" and "the account was exported minus its
	// secrets, because that read failed" are different events, and only the
	// trail will remember which one happened.
	detail := fmt.Sprintf("format_version=%d nodes=%d secrets=%d playbooks=%d images=%d",
		doc.FormatVersion, len(doc.Nodes), len(doc.Secrets), len(doc.Playbooks), len(doc.Images))
	if len(doc.Omitted) > 0 {
		for _, o := range doc.Omitted {
			detail += " omitted=" + o.Section
		}
	}
	h.audit.RecordDetail(ctx, audit_service.UserActor(accountID, userID),
		audit_event.ActionAccountExport, "account", accountID.String(), detail)

	// Offered as a download rather than rendered in the browser. The filename
	// carries the account id and the date because a customer comparing two
	// exports needs to tell them apart, and it goes through the same sanitizer
	// the CDN uses so an account NAME can never reach a header.
	name := cdnsign.SanitizeFileName(fmt.Sprintf("orchestrator-export-%s-%s.json",
		accountID.String(), time.Now().UTC().Format("20060102")))
	if name != "" {
		c.Response().Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	return c.JSONPretty(http.StatusOK, doc, "  ")
}
