package oidc

import (
	"context"
	"net/http"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Step-up re-authentication through the identity provider.
//
// The step-up guard in front of credential changes lets a user with NO
// credential through — there is nothing they could present, and demanding a
// proof they cannot make would lock them out of ever securing their account.
// For a federated user that exemption would let a stolen OIDC-only session
// register the attacker's OWN passkey, password or
// TOTP, and because a password reset does not remove enrolled factors, the
// victim's recovery would not evict them.
//
// The proof those users CAN make is at their identity provider. This is that
// round-trip: a signed-in user starts it, the IdP is asked to re-authenticate
// them for real (prompt=login, max_age=0), and the returned identity has to be
// one already linked to that same user before a step-up ticket is minted.

const (
	// stepUpCookie carries the in-flight step-up, exactly like linkCookie
	// carries an in-flight link. httpOnly and set only by an ACCESS-TOKEN
	// authenticated POST, which is what lets the public /start and /callback
	// trust the mode at all: a caller who cannot set the cookie cannot get a
	// ticket, and one who omits it merely gets an ordinary login.
	stepUpCookie = "oidc_stepup"
	// stepUpTicketTTL bounds the round trip; the same 5 minutes the link flow
	// allows for a redirect out to the IdP and back.
	stepUpTicketTTL = 5 * time.Minute
	// stepUpHandoffTTL bounds how long the one-time code at the end stays
	// exchangeable. Short: the SPA redeems it on the very next request.
	stepUpHandoffTTL = 60 * time.Second
)

// TicketGranter mints a step-up ticket for a proof performed elsewhere — here,
// a fresh IdP authentication. Narrow on purpose: this handler must be able to
// GRANT a confirmation and nothing else.
type TicketGranter interface {
	// Grant binds the ticket to the session the step-up was STARTED from and
	// issues it only while that session is live. The callback arm runs on a
	// public route with no access token, so the proof is captured at /reauth
	// and carried in the in-flight ticket.
	Grant(ctx context.Context, userID uuid.UUID, session services.SessionProof) (string, error)
	// TTL lets the response tell the client when the confirmation lapses, so it
	// re-prompts on its own terms instead of discovering it mid-request.
	TTL() time.Duration
}

// IdentityLister reports the external identities already linked to a user, so
// the callback can refuse a step-up completed as somebody else.
type IdentityLister interface {
	ListIdentities(ctx context.Context, userID uuid.UUID) ([]external_identity.ExternalIdentity, error)
}

// SetStepUp wires re-authentication through the IdP. Optional: with no granter
// the endpoints report the method as unavailable rather than half-working.
func (h *Handler) SetStepUp(granter TicketGranter) {
	h.stepUpGranter = granter
}

// SetAuditRecorder wires the trail for the two federated-credential changes
// this handler can make. Set after construction to match SetStepUp, and
// optional for the same reason: a missing recorder must degrade to an
// unrecorded action, never to a broken route.
func (h *Handler) SetAuditRecorder(r *audit.Recorder) {
	h.audit = r
}

// stepUpTicket binds an in-flight step-up to the user AND the session that
// started it. The session proof is what the callback hands to the granter.
type stepUpTicket struct {
	userID  uuid.UUID
	session services.SessionProof
	expires time.Time
}

// stepUpHandoff holds a minted confirmation behind a one-time code, so the
// ticket never travels in a URL — the same reason the login handoff exists.
// familyID pins the redemption to the session that started the step-up.
type stepUpHandoff struct {
	userID   uuid.UUID
	familyID uuid.UUID
	ticket   string
	expires  time.Time
}

// StepUpStart begins re-authentication at the IdP for the signed-in user.
// @Summary Begin step-up re-authentication with the identity provider
// @Description Starts a proof-of-presence round trip for a user whose only credential is their IdP account. Sets a short-lived httpOnly cookie, then the SPA navigates to /auth/oidc/start; the provider is asked to re-authenticate the person rather than answer from an existing session. On return the SPA exchanges the one-time code from /account?oidc_reauth=<code> for a confirmation ticket.
// @Tags auth
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse "Step-up started"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 503 {object} results.APIResponse "Step-up not available"
// @Router /auth/oidc/reauth [post]
func (h *Handler) StepUpStart(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	if h.stepUpGranter == nil {
		return results.SendError(c, http.StatusServiceUnavailable, "step-up is not available", nil)
	}
	code, err := randomToken()
	if err != nil {
		return results.SendInternalServerError(c, "failed to start re-authentication", err)
	}
	h.storeStepUpTicket(code, userID, middleware.GetSessionProofFromContext(c))
	secure := c.Request().TLS != nil || c.Scheme() == "https"
	c.SetCookie(&http.Cookie{
		Name: stepUpCookie, Value: code, Path: "/", MaxAge: flowCookieMaxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	return results.SendOKResponse(c, nil, "re-authentication started")
}

type stepUpCompleteRequest struct {
	Code string `json:"code"`
}

// StepUpComplete exchanges the one-time code for the confirmation ticket.
// @Summary Finish step-up re-authentication
// @Description Exchanges the one-time code the IdP round trip returned for a short-lived confirmation ticket. Bound to the user who started the flow: another user's code is not accepted. Send the ticket back in the X-Reauth-Ticket header.
// @Tags auth
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param body body stepUpCompleteRequest true "One-time code"
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "Confirmation ticket"
// @Failure 401 {object} results.APIResponse "Invalid or expired code"
// @Router /auth/oidc/reauth/complete [post]
func (h *Handler) StepUpComplete(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	var req stepUpCompleteRequest
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return results.SendValidationError(c, "code is required")
	}
	ticket, ok := h.consumeStepUpHandoff(req.Code, userID, middleware.GetSessionFamilyFromContext(c))
	if !ok {
		return results.SendUnauthorizedError(c, "invalid or expired confirmation")
	}
	return results.SendOKResponse(c, map[string]interface{}{
		"ticket":     ticket,
		"expires_in": int(h.stepUpGranter.TTL().Seconds()),
	}, "identity confirmed")
}

// stepUpInProgress reports whether the browser carries a live step-up cookie,
// without consuming it. /start uses this to decide that the IdP must be asked
// for a REAL re-authentication rather than an ordinary login.
func (h *Handler) stepUpInProgress(c echo.Context) bool {
	ck, err := c.Cookie(stepUpCookie)
	if err != nil || ck.Value == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.stepUpTickets[ck.Value]
	return ok && time.Now().Before(t.expires)
}

// consumeStepUpTicket returns the user and session that started the step-up
// (one-time).
func (h *Handler) consumeStepUpTicket(c echo.Context) (uuid.UUID, services.SessionProof, bool) {
	ck, err := c.Cookie(stepUpCookie)
	if err != nil || ck.Value == "" {
		return uuid.Nil, services.SessionProof{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.stepUpTickets[ck.Value]
	if !ok {
		return uuid.Nil, services.SessionProof{}, false
	}
	delete(h.stepUpTickets, ck.Value)
	if time.Now().After(t.expires) {
		return uuid.Nil, services.SessionProof{}, false
	}
	return t.userID, t.session, true
}

func (h *Handler) storeStepUpTicket(code string, userID uuid.UUID, session services.SessionProof) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for k, v := range h.stepUpTickets {
		if now.After(v.expires) {
			delete(h.stepUpTickets, k)
		}
	}
	h.stepUpTickets[code] = stepUpTicket{userID: userID, session: session, expires: now.Add(stepUpTicketTTL)}
}

func (h *Handler) storeStepUpHandoff(code string, userID, familyID uuid.UUID, ticket string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for k, v := range h.stepUpHandoffs {
		if now.After(v.expires) {
			delete(h.stepUpHandoffs, k)
		}
	}
	h.stepUpHandoffs[code] = stepUpHandoff{userID: userID, familyID: familyID, ticket: ticket, expires: now.Add(stepUpHandoffTTL)}
}

// consumeStepUpHandoff redeems a code ONCE, and only for the user AND session
// it was minted for. Without the user check the code — which travels in a URL
// the browser keeps in history — would elevate whoever presented it; the
// session check keeps the user's other sessions from redeeming it either,
// which is the same binding the ticket itself carries.
func (h *Handler) consumeStepUpHandoff(code string, userID, familyID uuid.UUID) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.stepUpHandoffs[code]
	if !ok {
		return "", false
	}
	delete(h.stepUpHandoffs, code)
	if time.Now().After(entry.expires) || entry.userID != userID || familyID == uuid.Nil || entry.familyID != familyID {
		return "", false
	}
	return entry.ticket, true
}

func (h *Handler) clearStepUpCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{Name: stepUpCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

// completeStepUp is the callback arm: the IdP just re-authenticated somebody,
// and this decides whether that somebody is the user who asked.
func (h *Handler) completeStepUp(c echo.Context, userID uuid.UUID, session services.SessionProof, provider, subject string) error {
	h.clearStepUpCookie(c)
	if h.stepUpGranter == nil || h.identityLister == nil {
		return c.Redirect(http.StatusFound, accountPath+"?oidc_reauth=error")
	}

	// The identity must ALREADY be linked to this user. Otherwise anyone able
	// to complete a login at the provider — including with their own, unrelated
	// account — would satisfy a step-up for the session they are sitting in,
	// which is precisely the proof this is supposed to demand.
	ids, err := h.identityLister.ListIdentities(c.Request().Context(), userID)
	if err != nil {
		h.logger.Warn("step-up identity lookup failed", "error", err, "user_id", userID)
		return c.Redirect(http.StatusFound, accountPath+"?oidc_reauth=error")
	}
	matched := false
	for i := range ids {
		if ids[i].Provider == provider && ids[i].Subject == subject {
			matched = true
			break
		}
	}
	if !matched {
		h.logger.Warn("step-up completed as a different identity", "user_id", userID, "provider", provider)
		return c.Redirect(http.StatusFound, accountPath+"?oidc_reauth=mismatch")
	}

	// The granter refuses a session that died during the round trip (signed
	// out, revoked, password changed) — the IdP proved the person, but the
	// session the ticket would unlock is gone.
	ticket, err := h.stepUpGranter.Grant(c.Request().Context(), userID, session)
	if err != nil {
		h.logger.Warn("failed to mint a step-up ticket", "error", err, "user_id", userID)
		return c.Redirect(http.StatusFound, accountPath+"?oidc_reauth=error")
	}
	code, err := randomToken()
	if err != nil {
		return c.Redirect(http.StatusFound, accountPath+"?oidc_reauth=error")
	}
	h.storeStepUpHandoff(code, userID, session.FamilyID, ticket)
	return c.Redirect(http.StatusFound, accountPath+"?oidc_reauth="+code)
}
