package access_entry

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	access_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/access"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	node_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Handler serves the access-entry API (declarative shell access on OU / node /
// account targets). Every route requires access.manage.
type Handler struct {
	svc    *access_service.Service
	logger *slog.Logger
}

func NewHandler(svc *access_service.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger.With("handler", "access_entry")}
}

// RegisterRoutes wires the access-entry endpoints under access.manage.
func (h *Handler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	manage := middleware.RequireAction(enforcer, authz.ActionAccessManage)

	g := e.Group("/api/v1/access-entries")
	g.Use(authMiddleware)
	g.POST("", h.Create, manage)
	g.GET("", h.List, manage)                  // ?ou_id=.. | ?node_id=.. | ?scope=account
	g.GET("/ou-counts", h.OUCounts, manage)    // per-OU entry counts for the rail
	g.GET("/:id", h.Get, manage)
	g.PUT("/:id", h.Update, manage)
	g.DELETE("/:id", h.Delete, manage)
	g.POST("/:id/keys/:keyId", h.AttachKey, manage)
	g.DELETE("/:id/keys/:keyId", h.DetachKey, manage)

	// Resolved effective access for a node (union across its OU chain).
	e.GET("/api/v1/nodes/:id/effective-access", h.EffectiveAccess, authMiddleware, manage)
	// Manual re-push of the node's effective access config to the device.
	e.POST("/api/v1/nodes/:id/access/re-apply", h.ReapplyAccess, authMiddleware, manage)
	// Recovery-mode apply: write the effective access config into the target
	// disk of a NETBOOTED node. The slug is deliberately absent
	// from the generic command mapping — this route is its only entry.
	e.POST("/api/v1/nodes/:id/access/apply-offline", h.ApplyOffline, authMiddleware, manage)
}

// createRequest is the POST body. Type selects the branch: "user" needs a
// username, "ca" needs ca_key_id + principals. Exactly one target (ou_id XOR
// node_id) or neither (account level).
type createRequest struct {
	Type string `json:"type"`
	// OUIDs scopes the entry to a set of units (a user can live in several).
	// The legacy single ou_id is still accepted and merged into the set.
	OUIDs          []uuid.UUID `json:"ou_ids"`
	OUID           *uuid.UUID  `json:"ou_id"`
	NodeID         *uuid.UUID  `json:"node_id"`
	AccountWide    bool        `json:"account_wide"`
	Username       string      `json:"username"`
	Sudo           bool        `json:"sudo"`
	Password       *string     `json:"password"`
	AdoptUnmanaged bool        `json:"adopt_unmanaged"`
	KeyIDs         []uuid.UUID `json:"key_ids"`
	CAKeyID        *uuid.UUID  `json:"ca_key_id"`
	Principals     []string    `json:"principals"`
}

// mergedOUIDs folds the legacy single ou_id into the ou_ids set.
func mergedOUIDs(ouIDs []uuid.UUID, ouID *uuid.UUID) []uuid.UUID {
	if ouID != nil {
		ouIDs = append(ouIDs, *ouID)
	}
	return ouIDs
}

// updateRequest is the PUT body. Nil fields are left unchanged; clear_password
// removes the password (distinct from omitting it).
type updateRequest struct {
	Sudo           *bool     `json:"sudo"`
	Password       *string   `json:"password"`
	ClearPassword  bool      `json:"clear_password"`
	Principals     *[]string `json:"principals"`
	AdoptUnmanaged *bool     `json:"adopt_unmanaged"`
	// A non-nil scope re-targets the entry (assign to a unit / make account-wide
	// / park as unassigned). Omit to leave the scope unchanged.
	Scope *scopeRequest `json:"scope"`
}

// scopeRequest re-targets an entry. No units and no node = unassigned unless
// account_wide is set (then fleet-wide default). The legacy single ou_id is
// merged into ou_ids.
type scopeRequest struct {
	OUIDs       []uuid.UUID `json:"ou_ids"`
	OUID        *uuid.UUID  `json:"ou_id"`
	NodeID      *uuid.UUID  `json:"node_id"`
	AccountWide bool        `json:"account_wide"`
}

// Create godoc
// @Summary Create an access entry
// @Description Creates a declarative access entry (named OS user or trusted CA) on a node, OU, or the account.
// @Tags access-entries
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body createRequest true "Access entry"
// @Success 201 {object} results.APIResponse{data=results.AccessEntryResult}
// @Failure 400 {object} results.APIResponse "Validation error, including adopt_unmanaged with neither a key nor a password (the device would clear the existing account's password and lock it)"
// @Failure 401 {object} results.APIResponse
// @Router /api/v1/access-entries [post]
func (h *Handler) Create(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	actorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req createRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request body")
	}

	var entry *access_entry.AccessEntry
	switch access_entry.Type(req.Type) {
	case access_entry.TypeUser:
		entry, err = h.svc.CreateUserEntry(c.Request().Context(), access_service.CreateUserEntryRequest{
			AccountID:      accountID,
			ActorUserID:    actorUserID,
			OUIDs:          mergedOUIDs(req.OUIDs, req.OUID),
			NodeID:         req.NodeID,
			AccountWide:    req.AccountWide,
			Principals:     req.Principals,
			Username:       req.Username,
			Sudo:           req.Sudo,
			Password:       req.Password,
			AdoptUnmanaged: req.AdoptUnmanaged,
			KeyIDs:         req.KeyIDs,
		})
	case access_entry.TypeCA:
		if req.CAKeyID == nil {
			return results.SendValidationError(c, "ca_key_id is required for a ca entry")
		}
		entry, err = h.svc.CreateCAEntry(c.Request().Context(), access_service.CreateCAEntryRequest{
			AccountID:   accountID,
			ActorUserID: actorUserID,
			OUIDs:       mergedOUIDs(req.OUIDs, req.OUID),
			NodeID:      req.NodeID,
			AccountWide: req.AccountWide,
			CAKeyID:     *req.CAKeyID,
		})
	default:
		return results.SendValidationError(c, "type must be 'user' or 'ca'")
	}
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendCreatedResponse(c, results.NewAccessEntryResult(entry), "access entry created")
}

// List godoc
// @Summary List access entries for a target
// @Description Lists entries directly attached to an OU (ou_id), a node (node_id), or the account (scope=account).
// @Tags access-entries
// @Produce json
// @Security ApiKeyAuth
// @Param ou_id query string false "Organizational unit id"
// @Param node_id query string false "Node id"
// @Param scope query string false "Set to 'account' for account-default entries, 'unassigned' for entries not applied to any node, or 'all' for every entry regardless of scope"
// @Success 200 {object} results.APIResponse{data=[]results.AccessEntryResult}
// @Router /api/v1/access-entries [get]
func (h *Handler) List(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ctx := c.Request().Context()
	var entries []access_entry.AccessEntry
	switch {
	case c.QueryParam("ou_id") != "":
		ouID, perr := uuid.Parse(c.QueryParam("ou_id"))
		if perr != nil {
			return results.SendValidationError(c, "invalid ou_id")
		}
		entries, err = h.svc.ListByOU(ctx, accountID, ouID)
	case c.QueryParam("node_id") != "":
		nodeID, perr := uuid.Parse(c.QueryParam("node_id"))
		if perr != nil {
			return results.SendValidationError(c, "invalid node_id")
		}
		entries, err = h.svc.ListByNode(ctx, accountID, nodeID)
	case c.QueryParam("scope") == "account":
		entries, err = h.svc.ListAccountLevel(ctx, accountID)
	case c.QueryParam("scope") == "unassigned":
		entries, err = h.svc.ListUnassigned(ctx, accountID)
	case c.QueryParam("scope") == "all":
		entries, err = h.svc.ListAllEntries(ctx, accountID)
	default:
		return results.SendValidationError(c, "one of ou_id, node_id or scope=account|unassigned|all is required")
	}
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, results.NewAccessEntryResults(entries), "access entries")
}

// OUCounts godoc
// @Summary Per-OU access-entry counts
// @Description For every organizational unit, how many access entries are scoped to it, plus the account-default (unattached) count and the account total. Feeds the credentials filter rail.
// @Tags access-entries
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.AccessOUCountsResult}
// @Failure 401 {object} results.APIResponse
// @Failure 500 {object} results.APIResponse
// @Router /api/v1/access-entries/ou-counts [get]
func (h *Handler) OUCounts(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	counts, unassigned, accountWide, total, err := h.svc.OUCounts(c.Request().Context(), accountID)
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, results.NewAccessOUCountsResult(counts, unassigned, accountWide, total), "access entry OU counts")
}

// Get godoc
// @Summary Get an access entry
// @Tags access-entries
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Access entry id"
// @Success 200 {object} results.APIResponse{data=results.AccessEntryResult}
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/access-entries/{id} [get]
func (h *Handler) Get(c echo.Context) error {
	accountID, _, entryID, err := h.ids(c)
	if err != nil {
		return results.SendValidationError(c, err.Error())
	}
	entry, err := h.svc.GetEntry(c.Request().Context(), accountID, entryID)
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, results.NewAccessEntryResult(entry), "access entry")
}

// Update godoc
// @Summary Update an access entry
// @Tags access-entries
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Access entry id"
// @Param body body updateRequest true "Fields to change"
// @Success 200 {object} results.APIResponse{data=results.AccessEntryResult}
// @Failure 400 {object} results.APIResponse "Validation error, including turning adopt_unmanaged on, or clearing the password, of an entry left with neither a key nor a password"
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/access-entries/{id} [put]
func (h *Handler) Update(c echo.Context) error {
	accountID, actorUserID, entryID, err := h.ids(c)
	if err != nil {
		return results.SendValidationError(c, err.Error())
	}
	var req updateRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request body")
	}
	var scope *access_service.ScopeChange
	if req.Scope != nil {
		scope = &access_service.ScopeChange{
			OUIDs:       mergedOUIDs(req.Scope.OUIDs, req.Scope.OUID),
			NodeID:      req.Scope.NodeID,
			AccountWide: req.Scope.AccountWide,
		}
	}
	entry, err := h.svc.UpdateEntry(c.Request().Context(), access_service.UpdateEntryRequest{
		AccountID:      accountID,
		ActorUserID:    actorUserID,
		EntryID:        entryID,
		Sudo:           req.Sudo,
		Password:       req.Password,
		ClearPassword:  req.ClearPassword,
		Principals:     req.Principals,
		AdoptUnmanaged: req.AdoptUnmanaged,
		Scope:          scope,
	})
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, results.NewAccessEntryResult(entry), "access entry updated")
}

// Delete godoc
// @Summary Delete an access entry
// @Tags access-entries
// @Security ApiKeyAuth
// @Param id path string true "Access entry id"
// @Success 204
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/access-entries/{id} [delete]
func (h *Handler) Delete(c echo.Context) error {
	accountID, actorUserID, entryID, err := h.ids(c)
	if err != nil {
		return results.SendValidationError(c, err.Error())
	}
	if err := h.svc.DeleteEntry(c.Request().Context(), accountID, actorUserID, entryID); err != nil {
		return h.mapError(c, err)
	}
	return results.SendNoContentResponse(c)
}

// AttachKey godoc
// @Summary Attach a key to an access entry
// @Description Adds a single SSH key to the entry's set (idempotent, additive). Per-key so a stale client cannot clobber the set.
// @Tags access-entries
// @Security ApiKeyAuth
// @Param id path string true "Access entry id"
// @Param keyId path string true "SSH key id"
// @Success 204
// @Failure 400 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/access-entries/{id}/keys/{keyId} [post]
func (h *Handler) AttachKey(c echo.Context) error {
	accountID, actorUserID, entryID, keyID, err := h.idsWithKey(c)
	if err != nil {
		return results.SendValidationError(c, err.Error())
	}
	if err := h.svc.AttachKey(c.Request().Context(), accountID, actorUserID, entryID, keyID); err != nil {
		return h.mapError(c, err)
	}
	return results.SendNoContentResponse(c)
}

// DetachKey godoc
// @Summary Detach a key from an access entry
// @Description Removes a single key. Removing the last key of a user entry locks the user (it is not deleted).
// @Tags access-entries
// @Security ApiKeyAuth
// @Param id path string true "Access entry id"
// @Param keyId path string true "SSH key id"
// @Success 204
// @Failure 404 {object} results.APIResponse
// @Router /api/v1/access-entries/{id}/keys/{keyId} [delete]
func (h *Handler) DetachKey(c echo.Context) error {
	accountID, actorUserID, entryID, keyID, err := h.idsWithKey(c)
	if err != nil {
		return results.SendValidationError(c, err.Error())
	}
	if err := h.svc.DetachKey(c.Request().Context(), accountID, actorUserID, entryID, keyID); err != nil {
		return h.mapError(c, err)
	}
	return results.SendNoContentResponse(c)
}

// EffectiveAccess godoc
// @Summary Resolved effective access for a node
// @Description Union of access entries across the node's OU chain, with per-username override and provenance. Passwords are never returned.
// @Tags access-entries
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node id"
// @Success 200 {object} results.APIResponse{data=[]results.EffectiveAccessEntryResult}
// @Router /api/v1/nodes/{id}/effective-access [get]
func (h *Handler) EffectiveAccess(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node id")
	}
	resolved, err := h.svc.ResolveEffectiveAccess(c.Request().Context(), accountID, nodeID)
	if err != nil {
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, results.NewEffectiveAccessResults(resolved), "effective access")
}

// ReapplyAccess godoc
// @Summary Re-apply access config on a node
// @Description Re-pushes the node's effective access configuration to the device (manual self-heal after a failed or stale apply). Returns queued=true when the node is offline or in recovery and the command waits for a managed-OS boot.
// @Tags access-entries
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=map[string]bool}
// @Failure 400 {object} results.APIResponse
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Failure 409 {object} results.APIResponse "The node runs the watcher (Watcher) agent and executes no commands"
// @Router /api/v1/nodes/{id}/access/re-apply [post]
func (h *Handler) ReapplyAccess(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	actorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node id")
	}
	queued, err := h.svc.ReapplyNode(c.Request().Context(), accountID, actorUserID, nodeID)
	if err != nil {
		var tooOld *node_service.AgentTooOldError
		if errors.As(err, &tooOld) {
			return results.SendValidationError(c, tooOld.Error())
		}
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, map[string]bool{"queued": queued}, "access re-apply dispatched")
}

// applyOfflineRequest names the target disk for the recovery-mode apply. Both
// fields are optional: an empty target_device auto-picks only when the node
// reports exactly one disk, and root_partition 0 lets the agent detect it.
type applyOfflineRequest struct {
	TargetDevice  string `json:"target_device"`
	RootPartition int    `json:"root_partition"`
}

// ApplyOffline godoc
// @Summary Apply access config to a netbooted node's disk
// @Description Writes the node's effective access configuration (users, keys, sudo, trusted CAs) into the installed OS on the target disk while the node is netbooted into recovery. This is how OU-assigned credentials reach a node whose live-OS agent never runs; the settings take effect on the next boot from that disk. Refused unless the node is currently in the recovery bootstrap.
// @Tags access-entries
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param request body applyOfflineRequest false "Target disk; omit to auto-pick when the node reports exactly one"
// @Success 200 {object} results.APIResponse{data=map[string]bool}
// @Failure 400 {object} results.APIResponse
// @Failure 401 {object} results.APIResponse
// @Failure 404 {object} results.APIResponse
// @Failure 409 {object} results.APIResponse "The node is not netbooted into recovery (code recovery_required), or another command already owns the target device"
// @Router /api/v1/nodes/{id}/access/apply-offline [post]
func (h *Handler) ApplyOffline(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	actorUserID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node id")
	}
	var req applyOfflineRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request body")
	}
	if req.RootPartition < 0 {
		return results.SendValidationError(c, "root_partition must not be negative")
	}
	if err := h.svc.ApplyOfflineToNode(c.Request().Context(), accountID, actorUserID, nodeID, req.TargetDevice, req.RootPartition); err != nil {
		var tooOld *node_service.AgentTooOldError
		if errors.As(err, &tooOld) {
			return results.SendValidationError(c, tooOld.Error())
		}
		return h.mapError(c, err)
	}
	return results.SendOKResponse(c, map[string]bool{"dispatched": true}, "offline access apply dispatched")
}

// --- helpers -------------------------------------------------------------

// ids returns the tenant, the CALLER and the entry. The caller comes back from
// the same helper as the account on purpose: every mutating route on this handler
// has to record who made the change, and a separate lookup is one a new route can
// quietly skip.
func (h *Handler) ids(c echo.Context) (accountID, actorUserID, entryID uuid.UUID, err error) {
	accountID, err = middleware.GetAccountIDFromContext(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, errors.New("unauthorized")
	}
	actorUserID, err = middleware.GetUserIDFromContext(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, errors.New("unauthorized")
	}
	entryID, err = uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, errors.New("invalid access entry id")
	}
	return accountID, actorUserID, entryID, nil
}

func (h *Handler) idsWithKey(c echo.Context) (accountID, actorUserID, entryID, keyID uuid.UUID, err error) {
	accountID, actorUserID, entryID, err = h.ids(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, err
	}
	keyID, err = uuid.Parse(c.Param("keyId"))
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, errors.New("invalid key id")
	}
	return accountID, actorUserID, entryID, keyID, nil
}

// mapError converts domain/validation errors to HTTP responses.
func (h *Handler) mapError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, app_errors.ErrAccessEntryNotFound):
		return results.SendNotFoundError(c, "access entry not found")
	case errors.Is(err, app_errors.ErrAccessEntryUsernameTaken):
		return results.SendError(c, http.StatusConflict, err.Error(), nil)
	case errors.Is(err, access_entry.ErrInvalidTarget),
		errors.Is(err, access_entry.ErrInvalidType),
		errors.Is(err, access_entry.ErrUsernameRequired),
		errors.Is(err, access_entry.ErrUsernameInvalid),
		errors.Is(err, access_entry.ErrUsernameReserved),
		errors.Is(err, access_entry.ErrCAKeyRequired),
		errors.Is(err, access_entry.ErrKeyTypeMismatch),
		errors.Is(err, access_entry.ErrPrincipalInvalid),
		errors.Is(err, access_entry.ErrAdoptNeedsCredential),
		errors.Is(err, access_entry.ErrUnknownKey):
		return results.SendValidationError(c, err.Error())
	default:
		results.LogFailure(c.Request().Context(), h.logger, "access entry request failed", err)
		return results.SendInternalServerError(c, "internal server error", err)
	}
}
