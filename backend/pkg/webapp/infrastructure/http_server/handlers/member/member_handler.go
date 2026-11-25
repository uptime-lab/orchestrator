package member

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	member_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// MemberHandler serves account member, binding and invite management.
type MemberHandler struct {
	service *member_service.Service
	// superAdmin answers whether the caller holds account-wide super_admin.
	// The routes that need both that role and a step-up ask it FIRST (see
	// requireSuperAdminFirst); the service still checks on its own.
	superAdmin func(context.Context, commands.AuthContext) error
	logger     *slog.Logger
}

func NewMemberHandler(service *member_service.Service, logger *slog.Logger) *MemberHandler {
	return &MemberHandler{service: service, superAdmin: service.RequireSuperAdmin, logger: logger.With("handler", "member")}
}

// requireSuperAdminFirst refuses a caller without super_admin before the
// step-up runs, so a member who may not do this is told so at once rather
// than asked to confirm their identity for a request that is then refused
// anyway. It adds no permission: the service makes the same check again.
// Removing oneself is leaving, which needs no rights, so DELETE of the
// caller's own id passes through to the step-up and the service as before.
func (h *MemberHandler) requireSuperAdminFirst(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if h.superAdmin == nil {
			return next(c)
		}
		auth, err := h.authContext(c)
		if err != nil {
			return results.SendUnauthorizedError(c, "unauthorized")
		}
		if c.Request().Method == http.MethodDelete {
			if target, perr := uuid.Parse(c.Param("userId")); perr == nil && target == auth.UserID {
				return next(c)
			}
		}
		if err := h.superAdmin(c.Request().Context(), auth); err != nil {
			return h.sendMemberError(c, err)
		}
		return next(c)
	}
}

// RegisterRoutes registers member routes. invite-signup is public: the
// invite token in the body is the credential.
func (h *MemberHandler) RegisterRoutes(e *echo.Echo, authMiddleware, recentAuth echo.MiddlewareFunc) {
	members := e.Group("/api/v1/members")
	members.Use(authMiddleware)
	members.GET("", h.List)

	// Privilege management requires a step-up, for the reason already written
	// against passkey registration: "a hijacked session cannot bind its own
	// authenticator — which a later password reset would NOT remove". Inviting
	// a super_admin is strictly stronger than binding an authenticator.
	//
	// Without this, an attacker holding a super_admin's access token could make
	// two requests — POST /members/invites with role=super_admin, then the
	// public /auth/invite-signup with the token from the response — and end up
	// with a SEPARATE identity carrying account-wide super_admin. The victim
	// could then change their password, revoke every session and rotate 2FA,
	// and the attacker would keep the account, because their access no longer descended from
	// the victim's credential at all.
	//
	// EnsureRecentAuth fails OPEN for a user with nothing to prove with, so this
	// cannot lock out an OIDC-only super_admin.
	//
	// The role is checked before the step-up (admin, then recentAuth): a
	// member whose role refuses the request is told so, instead of being
	// asked to confirm their identity for a request that is refused anyway.
	admin := h.requireSuperAdminFirst
	members.PUT("/:userId/bindings", h.ReplaceBindings, admin, recentAuth)
	members.PUT("/:userId/status", h.UpdateStatus, admin, recentAuth)
	members.DELETE("/me", h.Leave)
	members.DELETE("/:userId", h.Remove, admin, recentAuth)
	members.GET("/invites", h.ListInvites)
	// The two routes that put mail on the wire carry a per-IP limiter as well as
	// the per-account daily cap in the service.
	//
	// Both, not either: the service cap is the real bound on how much mail one
	// tenant can send, but it is counted per ACCOUNT, and self-service signup
	// means an attacker can hold several. The limiter bounds the rate a single
	// source can drive regardless of how many accounts it speaks for. Neither
	// subsumes the other.
	//
	// Deliberately looser than the public /auth limiters beside them: this is an
	// authenticated super_admin action, and a real admin onboarding a team does
	// click several times in a row.
	inviteLimit := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(1), Burst: 10, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return echo.NewHTTPError(http.StatusTooManyRequests, "too many invitations; slow down")
		},
	})
	members.POST("/invites", h.Invite, admin, recentAuth, inviteLimit)
	members.DELETE("/invites/:id", h.RevokeInvite)
	members.POST("/invites/:id/resend", h.ResendInvite, admin, recentAuth, inviteLimit)

	e.POST("/api/v1/invites/accept", h.AcceptInvite, authMiddleware)
	// Public and outside the rate-limited /auth group, so it carries its own
	// limiter: the handler bcrypt-hashes a caller-supplied password, which is a
	// free CPU-exhaustion primitive without one. (Guessing the token itself is
	// hopeless — 256 bits, hashed, atomic single-use.)
	// Public preview, same limiter envelope as the signup below it.
	e.GET("/auth/invite/preview", h.PreviewInvite, echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(0.5), Burst: 10, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		},
	}))

	e.POST("/auth/invite-signup", h.InviteSignup, echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(0.5), Burst: 10, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		},
	}))
}

func (h *MemberHandler) authContext(c echo.Context) (commands.AuthContext, error) {
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

// sendMemberError maps member-service errors onto HTTP responses.
func (h *MemberHandler) sendMemberError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, app_errors.ErrUnauthorized):
		return results.SendForbiddenError(c, "super admin rights required")
	case errors.Is(err, app_errors.ErrMemberBindingsStale):
		return results.SendErrorWithCode(c, http.StatusConflict, err.Error(), app_errors.MemberBindingsStaleCode)
	case errors.Is(err, app_errors.ErrLastSuperAdmin):
		return results.SendError(c, http.StatusConflict, "account must keep at least one active super admin", err)
	case errors.Is(err, app_errors.ErrAccountMemberNotFound):
		return results.SendNotFoundError(c, "member not found")
	case errors.Is(err, app_errors.ErrAccountInviteNotFound):
		return results.SendNotFoundError(c, "invite not found")
	case errors.Is(err, app_errors.ErrAlreadyMember):
		return results.SendError(c, http.StatusConflict, "user is already a member", err)
	case errors.Is(err, app_errors.ErrInvalidRole):
		return results.SendValidationError(c, err.Error())
	case errors.Is(err, app_errors.ErrInvalidMemberStatus):
		return results.SendValidationError(c, app_errors.ErrInvalidMemberStatus.Error())
	case errors.Is(err, app_errors.ErrInviteEmailMismatch):
		return results.SendError(c, http.StatusConflict, "invite was issued for a different email", err)
	case errors.Is(err, app_errors.ErrInviteNotValid):
		return results.SendError(c, http.StatusGone, "invite is expired, revoked or already accepted", err)
	case errors.Is(err, app_errors.ErrInviteRateLimited):
		// The message carries the specifics (how many, how long to wait), so it
		// is passed through rather than replaced with a generic line: this is a
		// limit a legitimate admin can hit, and one they can act on.
		return results.SendError(c, http.StatusTooManyRequests, err.Error(), err)
	case errors.Is(err, app_errors.ErrMemberLimitReached):
		// 409 rather than 429: this is not "too fast", it is "too many", and the
		// caller's next step is to remove a member rather than to wait. The
		// message carries the counts, which is what makes it actionable.
		return results.SendError(c, http.StatusConflict, err.Error(), err)
	case errors.Is(err, app_errors.ErrUserAlreadyExists):
		return results.SendError(c, http.StatusConflict, "user already exists", err)
	case errors.Is(err, app_errors.ErrUserEmailRequired):
		return results.SendValidationError(c, "email is required")
	case errors.Is(err, app_errors.ErrUserEmailInvalid):
		return results.SendValidationError(c, "email is not a valid address")
	case errors.Is(err, app_errors.ErrUserPasswordRequired):
		return results.SendValidationError(c, "password is required")
	case errors.Is(err, app_errors.ErrUserPasswordTooShort),
		errors.Is(err, app_errors.ErrUserPasswordTooLong):
		return results.SendValidationError(c, err.Error())
	default:
		return results.SendInternalServerError(c, "member operation failed", err)
	}
}

// List returns the account's members with bindings
// @Summary List account members
// @Description Returns the account's members with their status and role bindings
// @Tags members
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.MemberResult} "Members"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members [get]
func (h *MemberHandler) List(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	members, err := h.service.List(c.Request().Context(), auth)
	if err != nil {
		return h.sendMemberError(c, err)
	}
	return results.SendOKResponse(c, results.NewMemberResults(members), "members retrieved successfully")
}

type bindingSpecRequest struct {
	Role string     `json:"role"`
	OUID *uuid.UUID `json:"ou_id,omitempty"`
}

type replaceBindingsRequest struct {
	Bindings []bindingSpecRequest `json:"bindings"`
	// ExpectedBindingIDs is the optional precondition: the ids of the member's
	// bindings as the client read them (GET /members). When present and the
	// member's bindings have changed since, the write is refused with 409 and
	// code member_bindings_stale. An empty list means "the member had none".
	ExpectedBindingIDs *[]uuid.UUID `json:"expected_binding_ids,omitempty"`
}

// ReplaceBindings replaces a member's role bindings
// @Summary Replace a member's role bindings
// @Description Declaratively replaces the member's bindings. Rejects changes that would leave the account without an active super admin. With expected_binding_ids, refuses (409, code member_bindings_stale) when the member's bindings changed since the client read them. Demoting a member below super admin revokes the invitations they issued.
// @Tags members
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param userId path string true "User ID"
// @Param bindings body replaceBindingsRequest true "New binding set"
// @Success 200 {object} results.APIResponse{data=[]results.RoleBindingResult} "Updated bindings"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 403 {object} results.APIResponse "Super admin rights required, or step-up confirmation required (code reauth_required)"
// @Failure 404 {object} results.APIResponse "Member not found"
// @Failure 409 {object} results.APIResponse "Last super admin, or the bindings changed since they were read (code member_bindings_stale)"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members/{userId}/bindings [put]
func (h *MemberHandler) ReplaceBindings(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	targetID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		return results.SendValidationError(c, "invalid user ID")
	}
	var req replaceBindingsRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	specs := make([]member_service.BindingSpec, 0, len(req.Bindings))
	for _, b := range req.Bindings {
		specs = append(specs, member_service.BindingSpec{Role: authz.Role(b.Role), OUID: b.OUID})
	}
	bindings, err := h.service.ReplaceBindings(c.Request().Context(), auth, targetID, specs, req.ExpectedBindingIDs)
	if err != nil {
		return h.sendMemberError(c, err)
	}
	return results.SendOKResponse(c, results.NewRoleBindingResults(bindings), "bindings updated successfully")
}

type updateStatusRequest struct {
	Status string `json:"status"`
}

// UpdateStatus enables or disables a member
// @Summary Enable or disable a member
// @Description Sets the membership status (active|disabled). A disabled member keeps bindings but cannot authenticate into the account.
// @Tags members
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param userId path string true "User ID"
// @Param status body updateStatusRequest true "New status"
// @Success 200 {object} results.APIResponse "Status updated"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 403 {object} results.APIResponse "Super admin rights required, or step-up confirmation required (code reauth_required)"
// @Failure 404 {object} results.APIResponse "Member not found"
// @Failure 409 {object} results.APIResponse "Last super admin"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members/{userId}/status [put]
func (h *MemberHandler) UpdateStatus(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	targetID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		return results.SendValidationError(c, "invalid user ID")
	}
	var req updateStatusRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	err = h.service.UpdateStatus(c.Request().Context(), auth, targetID, accountMemberStatus(req.Status))
	if err != nil {
		return h.sendMemberError(c, err)
	}
	return results.SendOKResponse(c, nil, "member status updated successfully")
}

// Remove removes a member from the account
// @Summary Remove a member
// @Description Removes the membership and all the member's bindings in this account. The user identity is never deleted.
// @Tags members
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param userId path string true "User ID"
// @Success 204 "No Content"
// @Failure 403 {object} results.APIResponse "Super admin rights required, or step-up confirmation required (code reauth_required)"
// @Failure 404 {object} results.APIResponse "Member not found"
// @Failure 409 {object} results.APIResponse "Last super admin"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members/{userId} [delete]
func (h *MemberHandler) Remove(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	targetID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		return results.SendValidationError(c, "invalid user ID")
	}
	if err := h.service.Remove(c.Request().Context(), auth, targetID); err != nil {
		return h.sendMemberError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Leave removes the current user's own membership (self-leave)
// @Summary Leave the current account
// @Description Removes the current user's membership. Rejected for the last active super admin.
// @Tags members
// @Produce json
// @Security ApiKeyAuth
// @Success 204 "No Content"
// @Failure 409 {object} results.APIResponse "Last super admin"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members/me [delete]
func (h *MemberHandler) Leave(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	if err := h.service.Remove(c.Request().Context(), auth, auth.UserID); err != nil {
		return h.sendMemberError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

type inviteRequest struct {
	Email string     `json:"email"`
	Role  string     `json:"role"`
	OUID  *uuid.UUID `json:"ou_id,omitempty"`
}

// Invite creates an invite and returns the one-shot token
// @Summary Invite a user into the account
// @Description Creates an invite pinned to email+role+OU. The link is emailed when mail delivery is configured (`delivered` reports whether it went out). The response also carries the plaintext invite token EXACTLY ONCE, so the link can always be shared out of band.
// @Tags members
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param invite body inviteRequest true "Invite data"
// @Success 201 {object} results.APIResponse{data=results.InviteCreatedResult} "Invite created (token shown once)"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 403 {object} results.APIResponse "Super admin rights required, or step-up confirmation required (code reauth_required)"
// @Failure 409 {object} results.APIResponse "Already a member"
// @Failure 429 {object} results.APIResponse "Invitation rate limit reached for this account"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members/invites [post]
func (h *MemberHandler) Invite(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req inviteRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	outcome, err := h.service.Invite(c.Request().Context(), auth, req.Email, authz.Role(req.Role), req.OUID)
	if err != nil {
		return h.sendMemberError(c, err)
	}
	return results.SendCreatedResponse(c, results.InviteCreatedResult{
		Invite:    results.NewInviteResult(outcome.Invite),
		Token:     outcome.Token,
		Delivered: outcome.Delivered,
	}, "invite created successfully")
}

// ResendInvite re-mints and re-sends a pending invite
// @Summary Resend a pending invite
// @Description Mints a NEW token for an existing pending invite (same email, role and OU scope), revokes the previous one and emails the link when delivery is configured. The stored token is only a digest, so the original link cannot be re-sent — and keeping it alive after a resend would be the wrong default anyway.
// @Tags members
// @Produce json
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket, from POST /api/v1/users/me/reauth/password, /reauth/totp or /reauth/passkey/finish"
// @Param id path string true "Invite ID"
// @Success 200 {object} results.APIResponse{data=results.InviteCreatedResult} "Invite re-sent (new token shown once)"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 403 {object} results.APIResponse "Super admin rights required, or step-up confirmation required (code reauth_required)"
// @Failure 404 {object} results.APIResponse "Invite not found"
// @Failure 410 {object} results.APIResponse "Invite already accepted"
// @Failure 429 {object} results.APIResponse "Re-sent too soon, or the account's daily invitation limit is reached"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members/invites/{id}/resend [post]
func (h *MemberHandler) ResendInvite(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	inviteID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid invite ID")
	}
	outcome, err := h.service.ResendInvite(c.Request().Context(), auth, inviteID)
	if err != nil {
		return h.sendMemberError(c, err)
	}
	return results.SendOKResponse(c, results.InviteCreatedResult{
		Invite:    results.NewInviteResult(outcome.Invite),
		Token:     outcome.Token,
		Delivered: outcome.Delivered,
	}, "invite re-sent successfully")
}

// ListInvites returns pending invites
// @Summary List pending invites
// @Description Returns the account's unaccepted, unexpired invites (tokens are never returned)
// @Tags members
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]results.InviteResult} "Pending invites"
// @Failure 403 {object} results.APIResponse "Super admin rights required"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members/invites [get]
func (h *MemberHandler) ListInvites(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	invites, err := h.service.ListInvites(c.Request().Context(), auth)
	if err != nil {
		return h.sendMemberError(c, err)
	}
	return results.SendOKResponse(c, results.NewInviteResults(invites), "invites retrieved successfully")
}

// RevokeInvite deletes a pending invite
// @Summary Revoke a pending invite
// @Description Deletes a pending invite; accepted invites cannot be revoked
// @Tags members
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Invite ID"
// @Success 204 "No Content"
// @Failure 403 {object} results.APIResponse "Super admin rights required"
// @Failure 404 {object} results.APIResponse "Invite not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/members/invites/{id} [delete]
func (h *MemberHandler) RevokeInvite(c echo.Context) error {
	auth, err := h.authContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	inviteID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid invite ID")
	}
	if err := h.service.RevokeInvite(c.Request().Context(), auth, inviteID); err != nil {
		return h.sendMemberError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

type acceptInviteRequest struct {
	Token string `json:"token"`
}

// AcceptInvite joins the authenticated user to the invite's account
// @Summary Accept an invite (existing user)
// @Description Adds the authenticated user to the invite's account. The user's email must match the invited email; the token is single-use.
// @Tags members
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param invite body acceptInviteRequest true "Invite token"
// @Success 200 {object} results.APIResponse{data=results.AccountOptionResult} "Joined account"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 409 {object} results.APIResponse "Email mismatch or already a member"
// @Failure 410 {object} results.APIResponse "Invite no longer valid"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/invites/accept [post]
func (h *MemberHandler) AcceptInvite(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req acceptInviteRequest
	if err := c.Bind(&req); err != nil || req.Token == "" {
		return results.SendValidationError(c, "token is required")
	}
	invite, err := h.service.AcceptInvite(c.Request().Context(), userID, req.Token)
	if err != nil {
		return h.sendMemberError(c, err)
	}
	return results.SendOKResponse(c, results.AccountOptionResult{ID: invite.AccountID.String()}, "invite accepted successfully")
}

type inviteSignupRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// InviteSignup creates a new user from an invite
// @Summary Sign up with an invite (new user)
// @Description Creates a new user identity from an invite: no account of their own, only the membership the invite grants. The email is forced to the invited email.
// @Tags auth
// @Accept json
// @Produce json
// @Param signup body inviteSignupRequest true "Invite token + profile"
// @Success 201 {object} results.APIResponse{data=results.AuthResult} "User created and joined"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 409 {object} results.APIResponse "User already exists"
// @Failure 410 {object} results.APIResponse "Invite no longer valid"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /auth/invite-signup [post]
func (h *MemberHandler) InviteSignup(c echo.Context) error {
	var req inviteSignupRequest
	if err := c.Bind(&req); err != nil {
		return results.SendValidationError(c, "invalid request")
	}
	if req.Token == "" || len(req.Password) < 8 {
		return results.SendValidationError(c, "token and a password of at least 8 characters are required")
	}
	tokens, usr, err := h.service.AcceptInviteForNewUser(c.Request().Context(), req.Token, req.Name, req.Password)
	if err != nil {
		return h.sendMemberError(c, err)
	}
	return results.SendCreatedResponse(c, results.NewAuthResult(tokens, usr), "user created successfully")
}

// accountMemberStatus converts the wire status into the domain type.
func accountMemberStatus(s string) account_member.MemberStatus {
	return account_member.MemberStatus(s)
}

// PreviewInvite describes an invite to whoever holds its link.
// @Summary Preview an invitation
// @Description Returns the invited address, the account name, the role, and whether that address already has an account — so the invite page can offer "sign in to accept" instead of a signup form the server would reject. Public: the token IS the credential, and the holder was just emailed the address in question.
// @Tags auth
// @Produce json
// @Param token query string true "Invite token"
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Invite summary"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 410 {object} results.APIResponse "Invite is expired, revoked or already accepted"
// @Router /auth/invite/preview [get]
func (h *MemberHandler) PreviewInvite(c echo.Context) error {
	token := c.QueryParam("token")
	if token == "" {
		return results.SendValidationError(c, "token is required")
	}
	preview, err := h.service.PreviewInvite(c.Request().Context(), token)
	if err != nil {
		// One answer for unknown, expired, revoked and already-accepted: this
		// endpoint is public and must not be a directory of live invitations.
		return results.SendError(c, http.StatusGone, "this invitation is no longer valid", nil)
	}
	return results.SendOKResponse(c, map[string]interface{}{
		"email":        preview.Email,
		"account_name": preview.AccountName,
		"role":         preview.Role,
		"user_exists":  preview.UserExists,
	}, "invite preview")
}
