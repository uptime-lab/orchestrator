package secret

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/secret_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/secret_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	secret_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret"
	secret_tag_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret_tag"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret_tag"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
)

// SecretHandler serves secret management endpoints.
type SecretHandler struct {
	service    *secret_service.Service
	tagService *secret_tag_service.Service
	nodeReader NodeAccountReader
	enforcer   *authz_service.Enforcer
	audit      *audit.Recorder
	logger     *slog.Logger
}

// NodeAccountReader loads a node scoped to an account, so the node-attribute
// routes can enforce the node's OU scope. Satisfied by
// services.NodeService; narrow so tests can fake it.
type NodeAccountReader interface {
	GetByIDAndAccount(ctx context.Context, nodeID, accountID uuid.UUID) (*node.Node, error)
}

func NewSecretHandler(service *secret_service.Service, tagService *secret_tag_service.Service, nodeReader NodeAccountReader, auditRecorder *audit.Recorder, logger *slog.Logger) *SecretHandler {
	if tagService == nil {
		panic("SecretHandler requires a secret tag service")
	}
	if nodeReader == nil {
		panic("SecretHandler requires a node reader")
	}
	// Required, like every other writer of the trail: this handler is where
	// plaintext secret material leaves the platform, and a nil recorder would
	// make that invisible.
	if auditRecorder == nil {
		panic("SecretHandler requires an audit recorder")
	}
	return &SecretHandler{service: service, tagService: tagService, nodeReader: nodeReader, audit: auditRecorder, logger: logger.With("handler", "secret")}
}

// canReadSecretValues reports whether the caller may see plaintext
// variable/attribute values (admin+); viewers/managers get metadata + has_value
// only. Values of sensitive secrets are never returned regardless.
//
// A YES is RECORDED. secret.read_value gated the plaintext and wrote nothing, so
// a holder could page the whole account's values and leave no row — and the
// first question after a leak is "who saw this credential before it was
// rotated". The row goes here rather than at each endpoint because this is the
// one place the decision is made; a list of call sites is what the gap was.
//
// scope and subjectID say WHAT was served, so listing the account is
// distinguishable from opening one secret.
func (h *SecretHandler) canReadSecretValues(c echo.Context, scope audit.SecretValueReadScope, targetType, subjectID string) bool {
	if middleware.Authorize(c, h.enforcer, authz.ActionSecretReadValue, nil) != nil {
		return false
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return false
	}
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return false
	}
	h.audit.RecordSecretValuesRead(c.Request().Context(), audit.UserActor(accountID, userID),
		scope, targetType, subjectID)
	return true
}

func (h *SecretHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	h.enforcer = enforcer
	// Secret management is admin+; enforced account-wide here. (OU-scoped
	// secret administration by an OU-only admin is a later refinement — an
	// account-wide check is the safe, more-restrictive interim.)
	manage := middleware.RequireAction(enforcer, authz.ActionSecretManage)
	// Enumeration needs a gate of its own. Values were already protected by
	// canReadSecretValues, but every member at every role could list every
	// secret name, key, kind, tag and attachment target in the account — and a
	// name is the {{secrets.<name>}} identifier while attachments map which
	// node holds which credential. That is a reconnaissance map, not metadata.
	read := middleware.RequireAction(enforcer, authz.ActionSecretRead)

	secrets := e.Group("/api/v1/secrets")
	secrets.Use(authMiddleware)
	secrets.POST("", h.Create, manage)
	secrets.GET("", h.List, read)
	// Facets for the filter rail (static paths take priority over /:id).
	secrets.GET("/tags", h.ListTags, read)
	secrets.GET("/ou-counts", h.OUCounts, read)
	secrets.GET("/:id", h.Get, read)
	secrets.PUT("/:id", h.Update, manage)
	secrets.DELETE("/:id", h.Delete, manage)

	// Attachment management (many-to-many secret <-> OU/node).
	secrets.POST("/:id/attachments", h.CreateAttachment, manage)
	secrets.GET("/:id/attachments", h.ListAttachments, read)
	secrets.DELETE("/:id/attachments/:attachmentId", h.DeleteAttachment, manage)

	// Tag attachments (dynamic membership: a tag attached to a target delivers
	// every secret carrying it).
	secretTags := e.Group("/api/v1/secret-tags")
	secretTags.Use(authMiddleware)
	// Tag color + lifecycle (create with a color, recolor, rename, delete).
	secretTags.POST("", h.CreateTag, manage)
	secretTags.PUT("/:tag", h.UpdateTag, manage)
	secretTags.DELETE("/:tag", h.DeleteTag, manage)
	secretTags.POST("/:tag/attachments", h.CreateTagAttachment, manage)
	secretTags.GET("/:tag/attachments", h.ListTagAttachments, read)
	secretTags.DELETE("/:tag/attachments/:attachmentId", h.DeleteAttachment, manage)

	// Secrets attached to a target (value-free DTOs) + declarative batch
	// replace of a target's direct attachments.
	ous := e.Group("/api/v1/organizational-units")
	ous.Use(authMiddleware)
	ous.GET("/:id/secrets", h.ListByOU, read)
	ous.GET("/:id/secret-attachments", h.ListOUAttachments, read)
	ous.PUT("/:id/secret-attachments", h.ReplaceOUAttachments, manage)

	nodes := e.Group("/api/v1/nodes")
	nodes.Use(authMiddleware)
	nodes.GET("/:id/secrets", h.ListByNode, read)
	nodes.GET("/:id/secret-attachments", h.ListNodeAttachments, read)
	nodes.PUT("/:id/secret-attachments", h.ReplaceNodeAttachments, manage)

	// AD-style custom node attributes (backed by env-kind secrets attached to
	// the node; delivered to the agent env and resolvable as
	// {{node.attributes.<key>}}).
	// Gated on NODE actions, not secret.*: an attribute is node metadata that
	// happens to be stored as a secret, and the sidebar tab showing it is open
	// to anyone who can see the node. Authorization is per-node inside the
	// handlers (authorizeNodeAttributes): read = node.read, write =
	// node.attributes_write, both checked against the node's OU so an OU-scoped
	// binding cannot reach nodes of other OUs.
	nodes.GET("/:id/attributes", h.ListNodeAttributes)
	nodes.PUT("/:id/attributes/:key", h.UpsertNodeAttribute)
	nodes.DELETE("/:id/attributes/:key", h.DeleteNodeAttribute)
}

// authorizeNodeAttributes enforces node-scoped authorization for the attribute
// routes, mirroring the node handler's authorizeNodeAction: load
// the node account-scoped (a wrong/foreign id is a clean 404), then check the
// action against the node's OU. A node.read denial is converted to 404 — not
// 403 — so a member scoped to other OUs cannot probe which node ids exist.
// For writes the read gate runs first (deny -> 404, hides existence); a caller
// who can read the node but lacks node.attributes_write gets an honest 403.
func (h *SecretHandler) authorizeNodeAttributes(c echo.Context, nodeID uuid.UUID, write bool) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}
	n, err := h.nodeReader.GetByIDAndAccount(c.Request().Context(), nodeID, accountID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	}
	if err := middleware.Authorize(c, h.enforcer, authz.ActionNodeRead, n.OUID); err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) && httpErr.Code == http.StatusForbidden {
			return echo.NewHTTPError(http.StatusNotFound, "node not found")
		}
		return err
	}
	if !write {
		return nil
	}
	return middleware.Authorize(c, h.enforcer, authz.ActionNodeAttributes, n.OUID)
}

func (h *SecretHandler) authContext(c echo.Context) (commands.AuthContext, error) {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return commands.AuthContext{}, err
	}
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		return commands.AuthContext{}, err
	}
	return commands.NewAuthContext(accountID, userID), nil
}

// Create creates a secret.
// @Summary Create secret
// @Description Creates an account-scoped secret or variable. Values are stored encrypted; sensitive entries are write-only, non-sensitive env entries ("variables") return their value. Name is unique per account, at most 255 characters, contains no "}" (a {{secrets.<name>}} reference ends at the first one) and does not start with the reserved "attr:" prefix. A value is at most 64 KiB, and an env value has no line break.
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param secret body secret_create.SecretCreateCommand true "Secret data"
// @Success 201 {object} results.APIResponse{data=results.SecretResult} "Created"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 409 {object} results.APIResponse "Name already exists"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets [post]
func (h *SecretHandler) Create(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var cmd secret_create.SecretCreateCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.ID = uuid.New()
	cmd.AuthContext = auth

	sec, err := h.service.Create(c.Request().Context(), cmd)
	if err != nil {
		switch {
		case errors.Is(err, secret.ErrNameRequired),
			errors.Is(err, secret.ErrTooManyTags),
			errors.Is(err, secret.ErrInvalidTag):
			return results.SendValidationError(c, err.Error())
		case errors.Is(err, secret.ErrInvalidKind):
			return results.SendValidationError(c, app_errors.ErrInvalidSecretKind.Error())
		case errors.Is(err, app_errors.ErrSecretNameTaken):
			return results.SendError(c, http.StatusConflict, app_errors.ErrSecretNameTaken.Error(), err)
		case errors.Is(err, app_errors.ErrSecretLimitReached):
			// Passed through with its counts rather than replaced by a generic
			// line: node attributes are secrets too, so an account can reach
			// this without ever having visited the Secrets page, and the number
			// is what makes that comprehensible.
			return results.SendError(c, http.StatusConflict, err.Error(), err)
		default:
			return results.SendInternalServerError(c, "failed to create secret", err)
		}
	}
	return results.SendCreatedResponse(c, results.NewSecretResult(sec), "Secret created successfully")
}

// List lists secrets for the account with filtering, sorting and pagination.
// @Summary List secrets
// @Description Lists the account's secrets. Supports server-side search (name/key substring), kind/sensitive/group filters, sorting and pagination. Values are included only for non-sensitive entries.
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param search query string false "Substring match on name/key"
// @Param kind query string false "Filter by kind (env|credential|opaque)"
// @Param sensitive query boolean false "Filter by sensitivity"
// @Param tags query string false "Comma-separated tags; secrets must carry all of them"
// @Param ou_id query string false "Secrets delivered at this OU (attached to it or to any node inside it)"
// @Param node_id query string false "Secrets delivered to this node (directly or via group)"
// @Param unattached query boolean false "Only secrets with no attachment at all"
// @Param sort query string false "Sort column (name|created_at|updated_at, default name)"
// @Param order query string false "Sort order (asc|desc, default asc)"
// @Param page query integer false "1-based page (default 1)"
// @Param per_page query integer false "Page size (default 50, max 200)"
// @Success 200 {object} results.APIResponse{data=results.SecretListResult} "OK"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets [get]
func (h *SecretHandler) List(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	query := secret_list.SecretListQuery{
		Search:      c.QueryParam("search"),
		Kind:        c.QueryParam("kind"),
		Sort:        c.QueryParam("sort"),
		Order:       c.QueryParam("order"),
		AuthContext: auth,
	}
	if raw := c.QueryParam("sensitive"); raw != "" {
		sensitive, err := strconv.ParseBool(raw)
		if err != nil {
			return results.SendValidationError(c, "invalid sensitive filter")
		}
		query.Sensitive = &sensitive
	}
	if raw := c.QueryParam("tags"); raw != "" {
		query.Tags = strings.Split(raw, ",")
	}
	if raw := c.QueryParam("ou_id"); raw != "" {
		ouID, err := uuid.Parse(raw)
		if err != nil {
			return results.SendValidationError(c, "invalid organizational unit ID")
		}
		query.OUID = &ouID
	}
	if raw := c.QueryParam("node_id"); raw != "" {
		nodeID, err := uuid.Parse(raw)
		if err != nil {
			return results.SendValidationError(c, "invalid node ID")
		}
		query.NodeID = &nodeID
	}
	if raw := c.QueryParam("unattached"); raw != "" {
		unattached, err := strconv.ParseBool(raw)
		if err != nil {
			return results.SendValidationError(c, "invalid unattached filter")
		}
		query.Unattached = unattached
	}
	if raw := c.QueryParam("page"); raw != "" {
		query.Page, _ = strconv.Atoi(raw)
	}
	if raw := c.QueryParam("per_page"); raw != "" {
		query.PerPage, _ = strconv.Atoi(raw)
	}
	if err := query.Validate(); err != nil {
		return results.SendValidationError(c, err.Error())
	}

	ctx := c.Request().Context()
	secrets, total, err := h.service.List(ctx, query)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list secrets", err)
	}
	result := results.NewSecretListResult(secrets, total, query.Page, query.PerPage)
	secretCounts, _, err := h.service.AttachmentCounts(ctx, auth.AccountID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to count secret attachments", err)
	}
	result.ApplyAttachmentCounts(secretCounts)
	if !h.canReadSecretValues(c, audit.ScopeAccountSecrets, audit_event.TargetAccount, auth.AccountID.String()) {
		result.RedactValues()
	}
	return results.SendOKResponse(c, result, "Secrets retrieved successfully")
}

// ListTags returns the account's tag vocabulary with usage counts.
// @Summary List secret tags
// @Description Every tag used by the account's secrets, with the number of secrets carrying it. Feeds the filter rail and tag suggestions.
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.SecretTagListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets/tags [get]
func (h *SecretHandler) ListTags(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ctx := c.Request().Context()
	usage, attachments, err := h.service.TagFacets(ctx, auth.AccountID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list secret tags", err)
	}
	rows, err := h.tagService.List(ctx, auth.AccountID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list secret tag colors", err)
	}
	colors := make(map[string]string, len(rows))
	for i := range rows {
		colors[rows[i].Name] = rows[i].Color
	}
	return results.SendOKResponse(c, results.NewSecretTagListResult(usage, attachments, colors), "Secret tags retrieved successfully")
}

// The color fields' enums tags spell secret_tag.Palette for the API
// description; tag_color_enum_test.go holds them equal.
type upsertTagRequest struct {
	Name string `json:"name"`
	// One of the palette names (not a hex value); empty picks a default.
	Color string `json:"color" enums:"red,pink,purple,violet,blue,lightBlue,green,lime,yellow,orange"`
}

// CreateTag creates a tag with a name and color (or updates its color).
// @Summary Create or recolor a tag
// @Description Creates a tag with a name + palette color, or updates the color of an existing tag. Tag membership stays on the secrets; this only sets presentation. The color is a palette name — red, pink, purple, violet, blue, lightBlue, green, lime, yellow or orange — not a hex value; anything else is refused with 400 "invalid tag color". An empty color falls back to a deterministic default.
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body upsertTagRequest true "Tag name and color"
// @Success 201 {object} results.APIResponse{data=results.SecretTagResult} "Created"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secret-tags [post]
func (h *SecretHandler) CreateTag(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req upsertTagRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	tag, err := h.tagService.Upsert(c.Request().Context(), auth.AccountID, req.Name, req.Color)
	if err != nil {
		switch {
		case errors.Is(err, secret_tag.ErrNameRequired), errors.Is(err, secret_tag.ErrInvalidName), errors.Is(err, secret_tag.ErrInvalidColor):
			return results.SendValidationError(c, err.Error())
		default:
			return results.SendInternalServerError(c, "failed to save tag", err)
		}
	}
	return results.SendCreatedResponse(c, results.NewSecretTagResult(*tag), "Tag saved successfully")
}

type updateTagRequest struct {
	Name *string `json:"name"` // rename (cascades across secrets/attachments)
	// Recolor: one of the palette names (not a hex value).
	Color *string `json:"color" enums:"red,pink,purple,violet,blue,lightBlue,green,lime,yellow,orange"`
}

// UpdateTag renames and/or recolors a tag.
// @Summary Rename or recolor a tag
// @Description Renames a tag (cascading the string across every carrying secret and its attachments) and/or updates its color. Rename fails if the new name is already in use. The color is a palette name — red, pink, purple, violet, blue, lightBlue, green, lime, yellow or orange — not a hex value; anything else is refused with 400 "invalid tag color".
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param tag path string true "Current tag name"
// @Param body body updateTagRequest true "New name and/or color"
// @Success 200 {object} results.APIResponse{data=results.SecretTagResult} "OK"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 409 {object} results.APIResponse "New name already in use"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secret-tags/{tag} [put]
func (h *SecretHandler) UpdateTag(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ctx := c.Request().Context()
	current := c.Param("tag")
	var req updateTagRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}

	name := current
	if req.Name != nil && *req.Name != current {
		if err := h.tagService.Rename(ctx, auth.AccountID, current, *req.Name); err != nil {
			switch {
			case errors.Is(err, secret_tag.ErrNameRequired), errors.Is(err, secret_tag.ErrInvalidName):
				return results.SendValidationError(c, err.Error())
			case errors.Is(err, app_errors.ErrSecretTagNameTaken):
				return results.SendError(c, http.StatusConflict, app_errors.ErrSecretTagNameTaken.Error(), err)
			default:
				return results.SendInternalServerError(c, "failed to rename tag", err)
			}
		}
		name = *req.Name
	}

	color := ""
	if req.Color != nil {
		color = *req.Color
	}
	tag, err := h.tagService.Upsert(ctx, auth.AccountID, name, color)
	if err != nil {
		switch {
		case errors.Is(err, secret_tag.ErrNameRequired), errors.Is(err, secret_tag.ErrInvalidName), errors.Is(err, secret_tag.ErrInvalidColor):
			return results.SendValidationError(c, err.Error())
		default:
			return results.SendInternalServerError(c, "failed to save tag", err)
		}
	}
	return results.SendOKResponse(c, results.NewSecretTagResult(*tag), "Tag updated successfully")
}

// DeleteTag removes a tag from the account (strips it from every secret and
// drops its attachments).
// @Summary Delete a tag
// @Description Removes a tag entirely: strips it from every secret, drops its attachments, and deletes the color row.
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param tag path string true "Tag name"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secret-tags/{tag} [delete]
func (h *SecretHandler) DeleteTag(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	if err := h.tagService.Delete(c.Request().Context(), auth.AccountID, c.Param("tag")); err != nil {
		switch {
		case errors.Is(err, secret_tag.ErrNameRequired), errors.Is(err, secret_tag.ErrInvalidName):
			return results.SendValidationError(c, err.Error())
		default:
			return results.SendInternalServerError(c, "failed to delete tag", err)
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// OUCounts returns per-OU delivered-secret counts for the filter tree.
// @Summary Per-OU delivered-secret counts
// @Description For every organizational unit: how many distinct secrets are delivered at it (attached to the OU itself or to any node inside it, directly or via groups), plus the number of fully unattached secrets.
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=results.SecretOUCountsResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets/ou-counts [get]
func (h *SecretHandler) OUCounts(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	counts, unattached, total, err := h.service.OUDeliveryCounts(c.Request().Context(), auth.AccountID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to count secrets per OU", err)
	}
	return results.SendOKResponse(c, results.NewSecretOUCountsResult(counts, unattached, total), "Secret OU counts retrieved successfully")
}

// Get retrieves a secret by ID.
// @Summary Get secret
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Secret ID"
// @Success 200 {object} results.APIResponse{data=results.SecretResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets/{id} [get]
func (h *SecretHandler) Get(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid secret ID")
	}
	sec, err := h.service.GetByID(c.Request().Context(), secret_get.SecretGetQuery{ID: id, AuthContext: auth})
	if err != nil {
		if errors.Is(err, app_errors.ErrSecretNotFound) {
			return results.SendNotFoundError(c, "secret not found")
		}
		return results.SendInternalServerError(c, "failed to get secret", err)
	}
	res := results.NewSecretResult(sec)
	if !h.canReadSecretValues(c, audit.ScopeSecret, audit_event.TargetSecret, id.String()) {
		res.Value = nil
	}
	return results.SendOKResponse(c, res, "Secret retrieved successfully")
}

// Update updates a secret.
// @Summary Update secret
// @Description Updates a secret. Omit value to leave it unchanged; send an empty string to clear it. Turning a sensitive entry non-sensitive is rejected; group_id "" detaches from the group. A changed name and a new value follow the create rules; re-sending the stored name is always accepted.
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Secret ID"
// @Param secret body secret_update.SecretUpdateCommand true "Secret update"
// @Success 200 {object} results.APIResponse{data=results.SecretResult} "OK"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 409 {object} results.APIResponse "Name already exists, or the stored value is unreadable and no new value was sent"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets/{id} [put]
func (h *SecretHandler) Update(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid secret ID")
	}
	var cmd secret_update.SecretUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	cmd.ID = id
	cmd.AuthContext = auth

	sec, err := h.service.Update(c.Request().Context(), cmd)
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrSecretNotFound):
			return results.SendNotFoundError(c, "secret not found")
		case errors.Is(err, secret.ErrCannotDesensitize),
			errors.Is(err, secret.ErrTooManyTags),
			errors.Is(err, secret.ErrInvalidTag):
			return results.SendValidationError(c, err.Error())
		case errors.Is(err, app_errors.ErrSecretNameTaken):
			return results.SendError(c, http.StatusConflict, app_errors.ErrSecretNameTaken.Error(), err)
		case errors.Is(err, app_errors.ErrSecretValueUnreadable):
			// 409, not 500: the row is fine, the write is refused, and the
			// caller's next step is to send a value (which is accepted) — not
			// to retry the same request.
			return results.SendError(c, http.StatusConflict, err.Error(), err)
		default:
			return results.SendInternalServerError(c, "failed to update secret", err)
		}
	}
	return results.SendOKResponse(c, results.NewSecretResult(sec), "Secret updated successfully")
}

// Delete deletes a secret.
// @Summary Delete secret
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Secret ID"
// @Success 204 "No Content"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets/{id} [delete]
func (h *SecretHandler) Delete(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid secret ID")
	}
	err = h.service.Delete(c.Request().Context(), secret_delete.SecretDeleteCommand{ID: id, AuthContext: auth})
	if err != nil {
		if errors.Is(err, app_errors.ErrSecretNotFound) {
			return results.SendNotFoundError(c, "secret not found")
		}
		return results.SendInternalServerError(c, "failed to delete secret", err)
	}
	return results.SendNoContentResponse(c)
}

type createAttachmentRequest struct {
	OUID   *uuid.UUID `json:"ou_id,omitempty"`
	NodeID *uuid.UUID `json:"node_id,omitempty"`
}

// CreateAttachment attaches a secret to an organizational unit or a node.
// @Summary Attach secret to a target
// @Description Attaches a secret to exactly one target: an organizational unit (ou_id) or a node (node_id).
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Secret ID"
// @Param body body createAttachmentRequest true "Attachment target (exactly one of ou_id/node_id)"
// @Success 201 {object} results.APIResponse{data=results.SecretAttachmentResult} "Created"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets/{id}/attachments [post]
func (h *SecretHandler) CreateAttachment(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	secretID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid secret ID")
	}
	var req createAttachmentRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	att, err := h.service.Attach(c.Request().Context(), auth.AccountID, auth.UserID, secretID, req.OUID, req.NodeID)
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrSecretNotFound):
			return results.SendNotFoundError(c, "secret not found")
		case errors.Is(err, secret.ErrInvalidAttachmentTarget), errors.Is(err, app_errors.ErrInvalidAttachmentTarget):
			return results.SendValidationError(c, app_errors.ErrInvalidAttachmentTarget.Error())
		default:
			return results.SendInternalServerError(c, "failed to attach secret", err)
		}
	}
	return results.SendCreatedResponse(c, results.NewSecretAttachmentResult(att), "Secret attached successfully")
}

// ListAttachments lists a secret's attachments.
// @Summary List a secret's attachments
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Secret ID"
// @Success 200 {object} results.APIResponse{data=results.SecretAttachmentListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets/{id}/attachments [get]
func (h *SecretHandler) ListAttachments(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	secretID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid secret ID")
	}
	atts, err := h.service.ListAttachments(c.Request().Context(), auth.AccountID, secretID)
	if err != nil {
		if errors.Is(err, app_errors.ErrSecretNotFound) {
			return results.SendNotFoundError(c, "secret not found")
		}
		return results.SendInternalServerError(c, "failed to list attachments", err)
	}
	return results.SendOKResponse(c, results.NewSecretAttachmentListResult(atts), "Attachments retrieved successfully")
}

// DeleteAttachment detaches a secret from a target.
//
// One handler, TWO registered routes — the second one is documented because its
// absence is what makes a working API read as a broken one: there is no DELETE
// on the tag-attachment COLLECTION path, so a client that guesses
// `DELETE /secret-tags/{tag}/attachments` gets a 404 and concludes detaching is
// broken, when it is the call that is wrong.
// @Summary Detach a secret or a tag
// @Description Removes one attachment by its id. The attachment's subject is a secret XOR a tag, which is why the same handler answers under both prefixes; the id in the path is the ATTACHMENT's, not the subject's.
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Secret ID (or the tag name, on the /secret-tags form)"
// @Param attachmentId path string true "Attachment ID"
// @Success 204 "No Content"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secrets/{id}/attachments/{attachmentId} [delete]
// @Router /api/v1/secret-tags/{id}/attachments/{attachmentId} [delete]
func (h *SecretHandler) DeleteAttachment(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	attachmentID, err := uuid.Parse(c.Param("attachmentId"))
	if err != nil {
		return results.SendValidationError(c, "invalid attachment ID")
	}
	if err := h.service.Detach(c.Request().Context(), auth.AccountID, auth.UserID, attachmentID); err != nil {
		if errors.Is(err, app_errors.ErrSecretAttachmentNotFound) {
			return results.SendNotFoundError(c, "secret attachment not found")
		}
		return results.SendInternalServerError(c, "failed to detach secret", err)
	}
	return results.SendNoContentResponse(c)
}

// ListByOU lists the secrets attached to an organizational unit (no values).
// @Summary List secrets attached to an organizational unit
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Success 200 {object} results.APIResponse{data=results.SecretListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Organizational unit not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id}/secrets [get]
func (h *SecretHandler) ListByOU(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ouID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	// The unit is resolved in the caller's account first: another account's
	// id and an unknown one are both a 404, never an empty 200 — and never a
	// "plaintext served" row against a unit that is not the account's.
	if err := h.service.ValidateTarget(c.Request().Context(), auth.AccountID, &ouID, nil); err != nil {
		return results.SendInternalServerError(c, "failed to list organizational unit secrets", err)
	}
	secrets, err := h.service.ListByOU(c.Request().Context(), auth.AccountID, ouID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list organizational unit secrets", err)
	}
	ouResult := results.NewSecretListResult(secrets, int64(len(secrets)), 1, len(secrets))
	// The read is recorded only when a value would actually be served.
	if !ouResult.CarriesValues() || !h.canReadSecretValues(c, audit.ScopeOUSecrets, audit_event.TargetOU, ouID.String()) {
		ouResult.RedactValues()
	}
	return results.SendOKResponse(c, ouResult, "Secrets retrieved successfully")
}

// ListByNode lists the secrets attached directly to a node (no values).
// @Summary List secrets attached to a node
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.SecretListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/secrets [get]
func (h *SecretHandler) ListByNode(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	// Resolved in the caller's account first, as for the unit route above.
	if err := h.service.ValidateTarget(c.Request().Context(), auth.AccountID, nil, &nodeID); err != nil {
		return results.SendInternalServerError(c, "failed to list node secrets", err)
	}
	secrets, err := h.service.ListByNode(c.Request().Context(), auth.AccountID, nodeID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list node secrets", err)
	}
	nodeResult := results.NewSecretListResult(secrets, int64(len(secrets)), 1, len(secrets))
	// The read is recorded only when a value would actually be served.
	if !nodeResult.CarriesValues() || !h.canReadSecretValues(c, audit.ScopeNodeSecrets, audit_event.TargetNode, nodeID.String()) {
		nodeResult.RedactValues()
	}
	return results.SendOKResponse(c, nodeResult, "Secrets retrieved successfully")
}

// CreateTagAttachment attaches a tag subject to an OU or a node.
// @Summary Attach a tag to a target
// @Description Attaches a tag to exactly one target (ou_id or node_id). Dynamic membership: every secret carrying the tag — now or later — is delivered there. Idempotent.
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param tag path string true "Tag"
// @Param body body createAttachmentRequest true "Attachment target (exactly one of ou_id/node_id)"
// @Success 201 {object} results.APIResponse{data=results.SecretAttachmentResult} "Created"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secret-tags/{tag}/attachments [post]
func (h *SecretHandler) CreateTagAttachment(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req createAttachmentRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	att, err := h.service.AttachTag(c.Request().Context(), auth.AccountID, auth.UserID, c.Param("tag"), req.OUID, req.NodeID)
	if err != nil {
		switch {
		case errors.Is(err, secret.ErrInvalidAttachmentTarget):
			return results.SendValidationError(c, app_errors.ErrInvalidAttachmentTarget.Error())
		case errors.Is(err, secret.ErrInvalidTag), errors.Is(err, secret.ErrTooManyTags), errors.Is(err, secret.ErrInvalidAttachmentSubject):
			return results.SendValidationError(c, err.Error())
		default:
			return results.SendInternalServerError(c, "failed to attach tag", err)
		}
	}
	return results.SendCreatedResponse(c, results.NewSecretAttachmentResult(att), "Tag attached successfully")
}

// ListTagAttachments lists a tag's attachments.
// @Summary List a tag's attachments
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param tag path string true "Tag"
// @Success 200 {object} results.APIResponse{data=results.SecretAttachmentListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/secret-tags/{tag}/attachments [get]
func (h *SecretHandler) ListTagAttachments(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	atts, err := h.service.ListTagAttachments(c.Request().Context(), auth.AccountID, c.Param("tag"))
	if err != nil {
		return results.SendInternalServerError(c, "failed to list tag attachments", err)
	}
	return results.SendOKResponse(c, results.NewSecretAttachmentListResult(atts), "Attachments retrieved successfully")
}

// ListNodeAttachments lists a node's direct secret and tag attachments.
// @Summary List a node's direct secret attachments
// @Description The node's own attachments, one row per secret or tag, with the attachment id that DELETE takes. A tag no secret carries yet is listed here although it delivers nothing; inherited attachments are not listed (see effective-secrets). Edit one attachment with POST /secrets/{id}/attachments, POST /secret-tags/{tag}/attachments and the DELETE routes.
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.SecretAttachmentListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/secret-attachments [get]
func (h *SecretHandler) ListNodeAttachments(c echo.Context) error {
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	return h.listTargetAttachments(c, nil, &nodeID)
}

// ListOUAttachments lists an organizational unit's direct secret and tag attachments.
// @Summary List an organizational unit's direct secret attachments
// @Description The unit's own attachments, one row per secret or tag, with the attachment id that DELETE takes. A tag no secret carries yet is listed here although it delivers nothing; attachments inherited from a parent unit are not listed (see effective-secrets).
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Success 200 {object} results.APIResponse{data=results.SecretAttachmentListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Organizational unit not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id}/secret-attachments [get]
func (h *SecretHandler) ListOUAttachments(c echo.Context) error {
	ouID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	return h.listTargetAttachments(c, &ouID, nil)
}

func (h *SecretHandler) listTargetAttachments(c echo.Context, ouID, nodeID *uuid.UUID) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	atts, err := h.service.ListAttachmentsForTarget(c.Request().Context(), auth.AccountID, ouID, nodeID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list secret attachments", err)
	}
	return results.SendOKResponse(c, results.NewSecretAttachmentListResult(atts), "Attachments retrieved successfully")
}

type replaceAttachmentsRequest struct {
	SecretIDs []uuid.UUID `json:"secret_ids"`
	Tags      []string    `json:"tags"`
}

// ReplaceNodeAttachments declaratively replaces a node's direct secret/tag attachments.
// @Summary Replace a node's secret attachments
// @Description Declarative batch update: subjects listed (secrets and tags) are attached (idempotently), subjects missing are detached. Only direct node attachments are affected; OU-inherited secrets are untouched.
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param body body replaceAttachmentsRequest true "Desired direct attachments"
// @Success 200 {object} results.APIResponse{data=results.SecretAttachmentListResult} "OK"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Unknown secret"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/secret-attachments [put]
func (h *SecretHandler) ReplaceNodeAttachments(c echo.Context) error {
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	return h.replaceAttachments(c, nil, &nodeID)
}

// ReplaceOUAttachments declaratively replaces an OU's direct secret/tag attachments.
// @Summary Replace an organizational unit's secret attachments
// @Description Declarative batch update: subjects listed (secrets and tags) are attached (idempotently), subjects missing are detached.
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "OU ID"
// @Param body body replaceAttachmentsRequest true "Desired direct attachments"
// @Success 200 {object} results.APIResponse{data=results.SecretAttachmentListResult} "OK"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Unknown secret"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/organizational-units/{id}/secret-attachments [put]
func (h *SecretHandler) ReplaceOUAttachments(c echo.Context) error {
	ouID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid organizational unit ID")
	}
	return h.replaceAttachments(c, &ouID, nil)
}

func (h *SecretHandler) replaceAttachments(c echo.Context, ouID, nodeID *uuid.UUID) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req replaceAttachmentsRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	atts, err := h.service.ReplaceAttachmentsForTarget(c.Request().Context(), auth.AccountID, auth.UserID, ouID, nodeID, req.SecretIDs, req.Tags)
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrSecretNotFound):
			return results.SendNotFoundError(c, "secret not found")
		case errors.Is(err, secret.ErrInvalidAttachmentTarget):
			return results.SendValidationError(c, app_errors.ErrInvalidAttachmentTarget.Error())
		case errors.Is(err, secret.ErrInvalidTag), errors.Is(err, secret.ErrTooManyTags):
			return results.SendValidationError(c, err.Error())
		default:
			return results.SendInternalServerError(c, "failed to replace secret attachments", err)
		}
	}
	return results.SendOKResponse(c, results.NewSecretAttachmentListResult(atts), "Secret attachments replaced successfully")
}

type upsertNodeAttributeRequest struct {
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive"`
}

// ListNodeAttributes lists a node's AD-style custom attributes.
// @Summary List a node's custom attributes
// @Description Lists the node's custom attributes (per-node key/value pairs backed by env-kind secrets attached to the node). Values are returned only for non-sensitive attributes.
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 200 {object} results.APIResponse{data=results.NodeAttributeListResult} "OK"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/attributes [get]
func (h *SecretHandler) ListNodeAttributes(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	if aerr := h.authorizeNodeAttributes(c, nodeID, false); aerr != nil {
		return aerr
	}
	attrs, err := h.service.ListNodeAttributes(c.Request().Context(), auth.AccountID, nodeID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list node attributes", err)
	}
	attrResult := results.NewNodeAttributeListResult(attrs)
	if !h.canReadSecretValues(c, audit.ScopeNodeAttributes, audit_event.TargetNode, nodeID.String()) {
		attrResult.RedactValues()
	}
	return results.SendOKResponse(c, attrResult, "Node attributes retrieved successfully")
}

// UpsertNodeAttribute creates or updates one custom attribute of a node.
// @Summary Upsert a node's custom attribute
// @Description Creates or updates one custom attribute of a node (unique per node+key). The value is delivered to the node's agent env keyed by the attribute key and resolves as {{node.attributes.<key>}} in playbooks. Turning a sensitive attribute non-sensitive is rejected; re-create it instead. The value is at most 64 KiB and has no line break (it is written as one line of the node's env file).
// @Tags secrets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param key path string true "Attribute key (A-Z, a-z, 0-9, '_'; cannot start with a digit)"
// @Param body body upsertNodeAttributeRequest true "Attribute value and sensitivity"
// @Success 200 {object} results.APIResponse{data=results.NodeAttributeResult} "OK"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Node not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/attributes/{key} [put]
func (h *SecretHandler) UpsertNodeAttribute(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	if aerr := h.authorizeNodeAttributes(c, nodeID, true); aerr != nil {
		return aerr
	}
	var req upsertNodeAttributeRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	attr, err := h.service.UpsertNodeAttribute(c.Request().Context(), audit.UserActor(auth.AccountID, auth.UserID), auth.AccountID, nodeID, c.Param("key"), req.Value, req.Sensitive)
	if err != nil {
		switch {
		case errors.Is(err, secret.ErrInvalidAttributeKey), errors.Is(err, secret.ErrCannotDesensitize):
			return results.SendValidationError(c, err.Error())
		case errors.Is(err, app_errors.ErrSecretNameTaken):
			return results.SendError(c, http.StatusConflict, app_errors.ErrSecretNameTaken.Error(), err)
		default:
			return results.SendInternalServerError(c, "failed to upsert node attribute", err)
		}
	}
	return results.SendOKResponse(c, results.NewNodeAttributeResult(*attr), "Node attribute saved successfully")
}

// DeleteNodeAttribute removes one custom attribute of a node.
// @Summary Delete a node's custom attribute
// @Tags secrets
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Param key path string true "Attribute key"
// @Success 204 "No Content"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Forbidden"
// @Failure 404 {object} results.APIResponse "Node or attribute not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/nodes/{id}/attributes/{key} [delete]
func (h *SecretHandler) DeleteNodeAttribute(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	nodeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid node ID")
	}
	if aerr := h.authorizeNodeAttributes(c, nodeID, true); aerr != nil {
		return aerr
	}
	if err := h.service.DeleteNodeAttribute(c.Request().Context(), audit.UserActor(auth.AccountID, auth.UserID), auth.AccountID, nodeID, c.Param("key")); err != nil {
		if errors.Is(err, app_errors.ErrSecretNotFound) {
			return results.SendNotFoundError(c, "attribute not found")
		}
		return results.SendInternalServerError(c, "failed to delete node attribute", err)
	}
	return c.NoContent(http.StatusNoContent)
}
