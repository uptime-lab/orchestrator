// Package fleet serves the Status section's two account-wide reads: what needs
// an operator's attention, and what the platform has been doing to the fleet.
//
// Both are views over nodes, so both are gated on node.read and narrowed by
// the caller's allowed units — there is no new permission here, and there
// should not be: a member who can see a node can see that its agent is behind
// and that a reboot was dispatched to it.
//
// The gate is resolved IN the handler, not by RequireAction middleware.
// node.read is not an any-scope read, so a nil-target check refuses every
// OU-scoped member — the same reason the node list resolves AllowedOUs itself.
package fleet

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_activity_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	node_activity_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_activity"
	node_attention_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_attention"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// nodeSource is the slice of the node service this handler needs. The
// activity log uses it twice over: to turn the caller's allowed units into a
// node id set, because node_commands carries no unit column, and to name the
// node on each row without a lookup per row.
type nodeSource interface {
	GetAllByAccountID(ctx context.Context, query node_list.NodeListQuery) ([]node.Node, error)
}

// errInvalidOU is the one refusal parseOUFilter can produce.
var errInvalidOU = errors.New(`invalid ou_id: must be a unit id or "unassigned"`)

// Handler serves the Status section.
type Handler struct {
	attention *node_attention_service.Service
	activity  *node_activity_service.Reader
	nodes     nodeSource
	enforcer  *authz_service.Enforcer
	logger    *slog.Logger
}

// NewHandler builds the handler. Every dependency is required.
func NewHandler(
	attention *node_attention_service.Service,
	activity *node_activity_service.Reader,
	nodes nodeSource,
	enforcer *authz_service.Enforcer,
	logger *slog.Logger,
) *Handler {
	if attention == nil {
		panic("fleet handler requires an attention service")
	}
	if activity == nil {
		panic("fleet handler requires an activity reader")
	}
	if nodes == nil {
		panic("fleet handler requires a node source")
	}
	if enforcer == nil {
		panic("fleet handler requires an authz enforcer")
	}
	return &Handler{
		attention: attention,
		activity:  activity,
		nodes:     nodes,
		enforcer:  enforcer,
		logger:    logger.With("handler", "fleet"),
	}
}

// RegisterRoutes mounts the section's reads.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc) {
	g := e.Group("/api/v1/fleet")
	g.Use(authMiddleware)
	g.GET("/attention", h.Attention)
	g.GET("/activity", h.Activity)
}

// Attention lists the nodes that need an operator's attention
// @Summary List nodes needing attention
// @Description Returns every node in the account with at least one condition an operator should act on: offline, a newer agent or bootloader available, running the recovery image, sent to recovery by the boot-loop failsafe, a watchdog the device disarmed by itself, a utility that did not converge, hardware declared but never applied, a unit that no longer exists, a watcher that can no longer netboot, an unreadable device credential. Narrowed to the caller's units. `kinds` publishes the whole inventory so a client builds its filter from the server; `omitted` names checks this run could not make, such as an unreachable EEPROM signer. Requires node.read.
// @Tags fleet
// @Produce json
// @Security ApiKeyAuth
// @Param ou_id query string false "Unit id, or \"unassigned\", to narrow the list"
// @Success 200 {object} results.APIResponse{data=results.NodeAttentionListResult} "Nodes needing attention"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/fleet/attention [get]
func (h *Handler) Attention(c echo.Context) error {
	accountID, userID, err := identity(c)
	if err != nil {
		return err
	}
	allowAll, allowedOUs, aerr := h.enforcer.AllowedOUs(c.Request().Context(), accountID, userID, authz.ActionNodeRead)
	if aerr != nil {
		return middleware.MembershipHTTPError(aerr)
	}
	ouFilter, filterUnassigned, perr := parseOUFilter(c.QueryParam("ou_id"))
	if perr != nil {
		return results.SendValidationError(c, perr.Error())
	}

	fleet, err := h.attention.Resolve(c.Request().Context(), accountID)
	if err != nil {
		h.logger.ErrorContext(c.Request().Context(), "failed to resolve the fleet attention list",
			"error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list nodes needing attention", err)
	}

	rows := make([]results.NodeAttentionResult, 0, len(fleet.Nodes))
	for i := range fleet.Nodes {
		n := fleet.Nodes[i].Node
		if !visible(&n, allowAll, allowedOUs) {
			continue
		}
		if !matchesOU(&n, ouFilter, filterUnassigned) {
			continue
		}
		failures := make([]results.NodeAttentionFailureResult, 0, len(fleet.Nodes[i].Failures))
		for _, f := range fleet.Nodes[i].Failures {
			failures = append(failures, results.NodeAttentionFailureResult{
				Slug:      f.Slug,
				CommandID: f.CommandID,
				At:        f.At,
			})
		}
		var busy *results.NodeAttentionBusyResult
		if b := fleet.Nodes[i].Busy; b != nil {
			busy = &results.NodeAttentionBusyResult{Slug: b.Slug, Progress: b.Progress}
		}
		rows = append(rows, results.NewNodeAttentionResult(&n, busy, failures, fleet.Nodes[i].Items))
	}
	return results.SendOKResponse(c, results.NewNodeAttentionListResult(rows, fleet.Omitted), "nodes needing attention retrieved")
}

// Activity lists the account's device command history
// @Summary List fleet activity
// @Description One page of the device commands the platform dispatched for this account, newest first, narrowed to the caller's units. Optional filters: `node_id`, `ou_id` (a unit id or "unassigned"), `status` (repeatable or comma-separated; an unrecognised value is refused rather than ignored), `slug`, `since` and `until` (RFC3339). Rows carry no command payload or result — open a row through GET /api/v1/node-commands/{id} for those. Requires node.read.
// @Tags fleet
// @Produce json
// @Security ApiKeyAuth
// @Param node_id query string false "Node id to filter by"
// @Param ou_id query string false "Unit id, or \"unassigned\", to narrow the list"
// @Param status query string false "Command status to filter by; repeatable or comma-separated"
// @Param slug query string false "Command slug to filter by"
// @Param since query string false "Only commands created at or after this RFC3339 time"
// @Param until query string false "Only commands created at or before this RFC3339 time"
// @Param page query int false "Page number (1-based, default 1)"
// @Param per_page query int false "Page size (default 50, max 200)"
// @Success 200 {object} results.APIResponse{data=results.NodeActivityListResult} "One page of fleet activity"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/fleet/activity [get]
func (h *Handler) Activity(c echo.Context) error {
	ctx := c.Request().Context()
	accountID, userID, err := identity(c)
	if err != nil {
		return err
	}
	allowAll, allowedOUs, aerr := h.enforcer.AllowedOUs(ctx, accountID, userID, authz.ActionNodeRead)
	if aerr != nil {
		return middleware.MembershipHTTPError(aerr)
	}
	ouFilter, filterUnassigned, perr := parseOUFilter(c.QueryParam("ou_id"))
	if perr != nil {
		return results.SendValidationError(c, perr.Error())
	}
	var nodeFilter *uuid.UUID
	if raw := c.QueryParam("node_id"); raw != "" {
		id, uerr := uuid.Parse(raw)
		if uerr != nil {
			return results.SendValidationError(c, "invalid node_id")
		}
		nodeFilter = &id
	}
	since, serr := parseTime(c.QueryParam("since"))
	if serr != nil {
		return results.SendValidationError(c, "invalid since: expected an RFC3339 time")
	}
	until, uerr := parseTime(c.QueryParam("until"))
	if uerr != nil {
		return results.SendValidationError(c, "invalid until: expected an RFC3339 time")
	}

	// The visibility set is built from the node list rather than pushed into
	// SQL: node_commands has no unit column, so the only place the caller's
	// units are known is the node row. The per-account node cap bounds it.
	nodes, err := h.nodes.GetAllByAccountID(ctx, node_list.NodeListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to load nodes for the activity log", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list fleet activity", err)
	}
	byID := make(map[uuid.UUID]*node.Node, len(nodes))
	// A nil slice would mean "no narrowing", so start from an empty one: a
	// caller who can see nothing must get nothing, not everything.
	visibleIDs := []uuid.UUID{}
	for i := range nodes {
		n := &nodes[i]
		byID[n.ID] = n
		if !visible(n, allowAll, allowedOUs) || !matchesOU(n, ouFilter, filterUnassigned) {
			continue
		}
		if nodeFilter != nil && n.ID != *nodeFilter {
			continue
		}
		visibleIDs = append(visibleIDs, n.ID)
	}

	page, _ := strconv.Atoi(c.QueryParam("page"))
	perPage, _ := strconv.Atoi(c.QueryParam("per_page"))
	query := node_activity_list.NodeActivityListQuery{
		AuthContext:    commands.NewAuthContext(accountID, userID),
		VisibleNodeIDs: visibleIDs,
		Statuses:       statusParams(c),
		Slug:           c.QueryParam("slug"),
		Since:          since,
		Until:          until,
		Page:           page,
		PerPage:        perPage,
	}
	// Normalize HERE (the reader uses its own copy): the envelope must echo
	// the EFFECTIVE page/per_page, not the raw input, or a client's paging
	// math is fed back its own out-of-range value.
	if verr := query.Validate(); verr != nil {
		return results.SendValidationError(c, verr.Error())
	}

	cmds, total, err := h.activity.List(ctx, query)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to list fleet activity", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list fleet activity", err)
	}
	rows := make([]results.NodeActivityResult, 0, len(cmds))
	for i := range cmds {
		rows = append(rows, results.NewNodeActivityResult(&cmds[i], byID[cmds[i].NodeID]))
	}
	return results.SendOKResponse(c,
		results.NewNodeActivityListResult(rows, total, query.Page, query.PerPage),
		"fleet activity retrieved")
}

// identity reads the caller out of the request context.
func identity(c echo.Context) (accountID, userID uuid.UUID, err error) {
	userID, err = middleware.GetUserIDFromContext(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}
	accountID, err = middleware.GetAccountIDFromContext(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}
	return accountID, userID, nil
}

// visible applies the authz narrowing. A node with no unit reaches only an
// account-wide reader, matching the node list.
func visible(n *node.Node, allowAll bool, allowedOUs map[uuid.UUID]bool) bool {
	if allowAll {
		return true
	}
	return n.OUID != nil && allowedOUs[*n.OUID]
}

// matchesOU applies the operator's own unit filter, on top of the authz one and
// never instead of it.
func matchesOU(n *node.Node, ouFilter *uuid.UUID, filterUnassigned bool) bool {
	if filterUnassigned {
		return n.OUID == nil
	}
	if ouFilter == nil {
		return true
	}
	return n.OUID != nil && *n.OUID == *ouFilter
}

// parseOUFilter reads the ?ou_id= param, which takes a unit id or the literal
// "unassigned" — the same spelling the node list accepts.
func parseOUFilter(raw string) (ouID *uuid.UUID, unassigned bool, err error) {
	switch raw {
	case "":
		return nil, false, nil
	case "unassigned":
		return nil, true, nil
	}
	id, perr := uuid.Parse(raw)
	if perr != nil {
		return nil, false, errInvalidOU
	}
	return &id, false, nil
}

// statusParams reads ?status=, accepting it repeated or comma-separated so a
// hand-written URL and a generated one both work.
func statusParams(c echo.Context) []string {
	var out []string
	for _, raw := range c.QueryParams()["status"] {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// parseTime reads an optional RFC3339 timestamp.
func parseTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
