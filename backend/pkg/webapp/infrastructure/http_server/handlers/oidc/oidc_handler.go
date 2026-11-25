package oidc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	oidcclient "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/oidc"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"golang.org/x/time/rate"
)

const (
	stateCookie      = "oidc_state"
	nonceCookie      = "oidc_nonce"
	linkCookie       = "oidc_link"
	inviteCookie     = "oidc_invite" // carries the invite code across the redirect round-trip
	flowCookieMaxAge = 300           // 5 min to complete the redirect round-trip
	handoffTTL       = 60 * time.Second
	linkTicketTTL    = 5 * time.Minute
	// pendingLinkTTL bounds how long the user has to type the password of the
	// existing account after the Google round-trip.
	pendingLinkTTL = 5 * time.Minute
	// frontendCallbackPath is where the browser is sent after a successful
	// login callback; the SPA there exchanges the one-time code for tokens.
	frontendCallbackPath = "/oidc-callback"
	// accountPath is where the browser returns after a link callback (the
	// user is already signed in); the SPA reads ?oidc_link=ok|<error>.
	accountPath = "/account"
)

// bindingCookie ties the one-time code the callback hands to the SPA (a login
// handoff or a pending link) to the browser that finished the IdP round-trip.
// See issueBrowserBinding.
const bindingCookie = "oidc_binding"

// handoff holds a completed LoginResult behind a one-time code so tokens never
// travel in a URL. Consumed once by POST /auth/oidc/complete, and only by the
// browser holding the binding cookie set next to it.
type handoff struct {
	result  *services.LoginResult
	binding string
	expires time.Time
}

// linkTicket binds an in-flight "link my Google account" round-trip to the
// authenticated user who started it, so the callback links to them instead of
// starting a new session — AND to the session that started it. The callback
// is public and holds no access token, so the proof travels here and is
// re-asked right before the identity is attached: a link
// started by a session that was then signed out, revoked everywhere or
// outlived by a password reset must not persist a sign-in method that would
// survive all three.
type linkTicket struct {
	userID  uuid.UUID
	session services.SessionProof
	expires time.Time
}

// pendingLink holds the IdP-verified identity of a login that collided with an
// existing local account it is not linked to. The SPA prompts for that
// account's password and posts it back with the one-time code; see
// LinkExisting.
//
// Holding the identity server-side is what makes the follow-up step safe: the
// browser never carries it, and the code alone proves nothing without the
// password — nor without the binding cookie of the browser that earned it.
type pendingLink struct {
	identity services.OIDCIdentity
	binding  string
	expires  time.Time
}

// Handler serves the OIDC login flow. It is only wired when OIDC is enabled and
// the provider discovered successfully.
type Handler struct {
	client      *oidcclient.Client
	authService services.AuthService
	cfg         config.OIDCConfig
	inviteCode  string // when non-empty, provisioning a NEW account via OIDC requires it
	// openRegistration (SERVER_OPEN_REGISTRATION) admits a new account with
	// no code, ahead of inviteCode and of cfg.AutoProvision; see
	// provisionDecision.
	openRegistration bool
	logger           *slog.Logger

	// stepUpGranter mints a confirmation ticket after a fresh IdP
	// authentication; identityLister proves the returned identity is one this
	// user already owns. See stepup.go.
	stepUpGranter  TicketGranter
	identityLister IdentityLister

	// audit records the two federated-credential changes this handler can make.
	// Optional so the handler stays constructible without it; the routes that
	// use it check for nil rather than panicking, because an audit write must
	// never be the thing that fails a security fix.
	audit *audit.Recorder

	mu             sync.Mutex
	handoffs       map[string]handoff
	linkTickets    map[string]linkTicket
	pendingLinks   map[string]pendingLink
	stepUpTickets  map[string]stepUpTicket
	stepUpHandoffs map[string]stepUpHandoff
}

func NewHandler(client *oidcclient.Client, authService services.AuthService, cfg config.OIDCConfig, inviteCode string, openRegistration bool, logger *slog.Logger) *Handler {
	return &Handler{
		client:           client,
		authService:      authService,
		cfg:              cfg,
		inviteCode:       inviteCode,
		openRegistration: openRegistration,
		logger:           logger.With("handler", "oidc"),
		handoffs:         make(map[string]handoff),
		linkTickets:      make(map[string]linkTicket),
		pendingLinks:     make(map[string]pendingLink),
		// The auth service is also the identity lister; kept as a narrow
		// interface so stepup.go states exactly what it needs.
		identityLister: authService,
		stepUpTickets:  make(map[string]stepUpTicket),
		stepUpHandoffs: make(map[string]stepUpHandoff),
	}
}

// Register wires the OIDC routes. config/start/callback/complete are public
// (the IdP round-trip and one-time codes are the credentials); link/identities
// operate on the current user and require the access token.
//
// recentAuth guards /link, and it is not optional. Attaching a federated
// identity ADDS a way to sign in, and one that survives everything a victim
// would reach for: a password change, a passkey rotation, a TOTP enrolment and
// a full session revocation all leave it in place, and an OIDC login skips the
// TOTP gate by design. Without the step-up, one stolen access token buys a
// permanent account backdoor — the exact attack reauth exists to stop.
//
// The step-up ENDPOINTS on this group stay ungated, or a federated-only user
// could never make the proof /link now demands.
func (h *Handler) Register(e *echo.Echo, authMiddleware, recentAuth echo.MiddlewareFunc) {
	g := e.Group("/auth/oidc")
	g.GET("/config", h.Config)
	g.GET("/start", h.Start)
	g.GET("/callback", h.Callback)
	g.POST("/complete", h.Complete)
	// link-existing verifies a password, so it gets the same per-IP throttle as
	// /auth/login. This group has no limiter of its own — /auth's limiter is on
	// a different group and is NOT inherited here.
	g.POST("/link-existing", h.LinkExisting, credentialRateLimit())
	g.POST("/link", h.LinkStart, authMiddleware, recentAuth)
	g.GET("/identities", h.Identities, authMiddleware)
	// Unlink carries NO step-up, and that is the rule applied rather than an
	// omission: the gate belongs in front of things that ADD or REPOINT a way
	// to sign in. Removing one is recoverable (link it again) and is refused
	// outright when it would be the last credential, so the worst a hijacked
	// session achieves here is an inconvenience — while gating it would let an
	// attacker's linked identity survive precisely because the victim could not
	// pass a step-up. DELETE /users/me/passkeys/:id is ungated for the same
	// reason.
	g.DELETE("/identities/:id", h.UnlinkIdentity, authMiddleware)
	// Step-up re-authentication for a user whose only credential is the IdP.
	g.POST("/reauth", h.StepUpStart, authMiddleware)
	g.POST("/reauth/complete", h.StepUpComplete, authMiddleware)
}

// credentialRateLimit throttles password-verifying endpoints per client IP,
// matching the limiter on /auth (0.5 req/s sustained, burst 10).
func credentialRateLimit() echo.MiddlewareFunc {
	return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(0.5),
			Burst:     10,
			ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return results.SendError(c, http.StatusTooManyRequests, "too many requests, slow down", nil)
		},
	})
}

// Config reports that OIDC login is available (this route only exists when a
// provider was discovered), so the SPA can conditionally show the button.
// provider is the display name the SPA builds its own wording from ("Sign in
// with Google"; empty means "single sign-on"); issuer lets the account page
// tell this provider's linked identities from any left by an earlier one.
// sign_up says whether an unknown identity can create an account here, the
// same rule the callback applies: with open registration it needs nothing
// ("open"); otherwise with an invite code configured it needs the code
// ("invite_code"); without either, OIDC_AUTO_PROVISION decides ("open" or
// "closed"). label is the legacy English button text, kept for older SPAs.
// @Summary OIDC availability
// @Description Reports that federated login is configured on this instance: the provider's display name (empty when none is configured and the issuer is not Google), its issuer URL, whether an unknown identity can create an account (sign_up: open — open registration, or OIDC_AUTO_PROVISION with no invite code; invite_code; or closed), and a legacy English button label. The route exists only when a provider was discovered at startup, so a 404 means OIDC is off.
// @Tags auth
// @Produce json
// @Success 200 {object} results.APIResponse{data=map[string]interface{}} "OIDC is available"
// @Router /auth/oidc/config [get]
func (h *Handler) Config(c echo.Context) error {
	return results.SendOKResponse(c, map[string]any{
		"enabled":  true,
		"label":    h.cfg.ButtonLabel(),
		"provider": h.cfg.DisplayName(),
		"issuer":   h.cfg.IssuerURL,
		"sign_up":  h.signUpMode(),
	}, "oidc config")
}

// signUpMode mirrors provisionDecision: open registration admits without a
// code; otherwise a configured invite code gates provisioning regardless of
// AutoProvision; without either, AutoProvision alone decides.
func (h *Handler) signUpMode() string {
	switch {
	case h.openRegistration:
		return "open"
	case h.inviteCode != "":
		return "invite_code"
	case h.cfg.AutoProvision:
		return "open"
	default:
		return "closed"
	}
}

// provisionDecision decides whether an unknown email may provision a NEW
// account, and whether a refusal is for want of the invite code. The
// instance's own sign-up gate decides first, whatever OIDC_AUTO_PROVISION
// says, exactly as the password sign-up decides:
//
//   - open registration: provisioning is allowed with no code, the same
//     answer the sign-up form gets, so the page never shows a provider
//     button that the callback then refuses for a code it did not ask for;
//   - an invite code configured (invite-gated): provisioning REQUIRES a
//     matching invite (from the httpOnly cookie set at Start), so a stranger
//     with an account at the provider cannot self-register;
//   - neither: AutoProvision alone decides.
//
// The account ceiling is asked later, by the auth service, on every path.
// Existing users always log in (LoginOIDC only consults this for brand-new
// emails).
func (h *Handler) provisionDecision(c echo.Context) (allowProvision, inviteGated bool) {
	if h.openRegistration {
		return true, false
	}
	if h.inviteCode == "" {
		return h.cfg.AutoProvision, false
	}
	if ck, cerr := c.Cookie(inviteCookie); cerr == nil {
		return subtle.ConstantTimeCompare([]byte(ck.Value), []byte(h.inviteCode)) == 1, true
	}
	return false, true
}

// Start redirects the browser to the IdP with a fresh CSRF state and nonce,
// stashed in short-lived httpOnly cookies for the callback to check.
// @Summary Begin federated login
// @Description Redirects the browser to the identity provider. Not callable by an API client: the state and nonce live in httpOnly cookies that only this redirect sets, and the callback rejects a response without them.
// @Tags auth
// @Param invite query string false "Invite code, carried across the IdP round-trip; required to provision a NEW account when the instance is invite-gated"
// @Success 302 {string} string "Redirect to the identity provider"
// @Failure 500 {object} results.APIResponse "Could not start the login flow"
// @Router /auth/oidc/start [get]
func (h *Handler) Start(c echo.Context) error {
	state, err := randomToken()
	if err != nil {
		return results.SendInternalServerError(c, "failed to start login", err)
	}
	nonce, err := randomToken()
	if err != nil {
		return results.SendInternalServerError(c, "failed to start login", err)
	}
	secure := c.Request().TLS != nil || c.Scheme() == "https"
	h.setFlowCookie(c, stateCookie, state, secure)
	h.setFlowCookie(c, nonceCookie, nonce, secure)
	// Invite-gated sign-up: carry the invite code (if the SPA passed one) across the IdP
	// round-trip in an httpOnly cookie; the callback requires it to provision a new
	// account. Existing users log in without it.
	if invite := c.QueryParam("invite"); invite != "" {
		h.setFlowCookie(c, inviteCookie, invite, secure)
	}
	// A step-up in flight (httpOnly cookie, settable only by the authenticated
	// POST) must not be answered from an existing session at the IdP, or it
	// proves nothing beyond "this browser was already signed in there".
	if h.stepUpInProgress(c) {
		return c.Redirect(http.StatusFound, h.client.AuthCodeURLForStepUp(state, nonce))
	}
	return c.Redirect(http.StatusFound, h.client.AuthCodeURL(state, nonce))
}

// Callback verifies the IdP response, resolves the identity to a session, and
// redirects the browser to the SPA with a one-time handoff code.
// @Summary Identity-provider callback
// @Description The IdP redirects here. Verifies state and nonce against the cookies /auth/oidc/start set, resolves the identity to a session, and redirects to the SPA carrying a one-time handoff code — never the tokens themselves, which the SPA trades for at /auth/oidc/complete. Failures redirect with an error parameter rather than answering with a status, because the caller is a browser mid-redirect.
// @Tags auth
// @Param state query string true "CSRF state, must match the cookie"
// @Param code query string true "Authorization code from the IdP"
// @Success 302 {string} string "Redirect to the SPA with a one-time handoff code"
// @Router /auth/oidc/callback [get]
func (h *Handler) Callback(c echo.Context) error {
	stateParam := c.QueryParam("state")
	stateCk, err := c.Cookie(stateCookie)
	if err != nil || stateParam == "" || stateParam != stateCk.Value {
		return h.fail(c, "invalid_state")
	}
	nonceCk, err := c.Cookie(nonceCookie)
	if err != nil {
		return h.fail(c, "invalid_state")
	}
	h.clearFlowCookies(c)

	if e := c.QueryParam("error"); e != "" {
		return h.fail(c, e)
	}
	code := c.QueryParam("code")
	if code == "" {
		return h.fail(c, "missing_code")
	}

	identity, err := h.client.Exchange(c.Request().Context(), code, nonceCk.Value)
	if err != nil {
		h.logger.Warn("oidc exchange failed", "error", err)
		return h.fail(c, "verification_failed")
	}

	svcIdentity := services.OIDCIdentity{
		Provider: identity.Provider,
		Subject:  identity.Subject,
		Email:    identity.Email,
		Name:     identity.Name,
	}

	// Step-up mode: a signed-in user proving they are still themselves. Checked
	// before link mode, and it never creates or attaches anything — the only
	// outcome is a confirmation ticket for an identity they already hold.
	if userID, session, ok := h.consumeStepUpTicket(c); ok {
		return h.completeStepUp(c, userID, session, identity.Provider, identity.Subject)
	}

	// Link mode: the round-trip was started from Account by a signed-in user
	// (link ticket cookie). Attach the identity to them instead of starting a
	// new session, and return to /account.
	if userID, session, ok := h.consumeLinkTicket(c); ok {
		h.clearLinkCookie(c)
		return h.completeLink(c, userID, session, svcIdentity)
	}

	allowProvision, inviteGated := h.provisionDecision(c)

	result, err := h.authService.LoginOIDC(c.Request().Context(), svcIdentity, allowProvision)
	if err != nil {
		if errors.Is(err, app_errors.ErrOIDCNoAccount) {
			// New email that couldn't provision: distinguish "needs an invite"
			// from a plain no-account so the SPA can prompt for the code.
			if inviteGated && !allowProvision {
				return h.fail(c, "invite_required")
			}
			return h.fail(c, "no_account")
		}
		if errors.Is(err, app_errors.ErrOIDCAccountLinkRequired) {
			// A local account with this email that the identity is not linked
			// to: no implicit link. Offer the inline "prove it with that
			// account's password" step instead of bouncing the user back to
			// the login form.
			linkCode, terr := randomToken()
			if terr != nil {
				return h.fail(c, "account_exists")
			}
			binding, terr := h.issueBrowserBinding(c)
			if terr != nil {
				return h.fail(c, "account_exists")
			}
			h.storePendingLink(linkCode, binding, svcIdentity)
			return c.Redirect(http.StatusFound, frontendCallbackPath+"?link_code="+linkCode)
		}
		if errors.Is(err, app_errors.ErrOIDCAccountExists) {
			// The matching account has no password to prove itself with here:
			// its owner signs in as they already can and links from settings.
			return h.fail(c, "account_exists")
		}
		if errors.Is(err, app_errors.ErrMemberLimitReached) {
			// An invite accepted through the IdP is held to the account's
			// seat cap like every other acceptance.
			return h.fail(c, "member_limit")
		}
		if errors.Is(err, app_errors.ErrExternalIdentityConflict) {
			// The provider account is still held by another user's (revoked)
			// link, so nobody new can be created for it; nothing was.
			return h.fail(c, "identity_conflict")
		}
		h.logger.Warn("oidc login failed", "error", err)
		return h.fail(c, "login_failed")
	}

	handoffCode, err := randomToken()
	if err != nil {
		return h.fail(c, "login_failed")
	}
	binding, err := h.issueBrowserBinding(c)
	if err != nil {
		return h.fail(c, "login_failed")
	}
	h.storeHandoff(handoffCode, binding, result)
	return c.Redirect(http.StatusFound, frontendCallbackPath+"?code="+handoffCode)
}

// issueBrowserBinding sets a fresh random value in an httpOnly cookie on the
// callback's redirect and returns it, to be stored next to the one-time code
// the redirect carries. The code alone then buys nothing: /complete and
// /link-existing require the cookie too, and only the browser that finished
// this IdP round-trip holds it. Without it, a code obtained from one's OWN
// login could be planted in someone else's browser through a link to
// /oidc-callback?code=..., signing them in to the planter's account.
//
// SameSite=Lax is enough: the cookie is SET on a top-level navigation from
// the IdP and SENT on the SPA's same-origin POST, while a cross-site request
// cannot carry it.
func (h *Handler) issueBrowserBinding(c echo.Context) (string, error) {
	binding, err := randomToken()
	if err != nil {
		return "", err
	}
	secure := c.Request().TLS != nil || c.Scheme() == "https"
	h.setFlowCookie(c, bindingCookie, binding, secure)
	return binding, nil
}

// presentedBinding is the binding cookie of this request, or "" without one.
func presentedBinding(c echo.Context) string {
	ck, err := c.Cookie(bindingCookie)
	if err != nil {
		return ""
	}
	return ck.Value
}

// clearBindingCookie removes the binding once a code was presented: each one
// serves exactly one code.
func clearBindingCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{Name: bindingCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

// bindingMatches compares in constant time and refuses an empty value on
// either side, so a code stored without a binding can never be redeemed.
func bindingMatches(stored, presented string) bool {
	if stored == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(presented)) == 1
}

type completeRequest struct {
	Code string `json:"code"`
}

// Complete exchanges the one-time handoff code for the login result: a token
// pair (single account) or a 202 account-selection challenge (multi-account),
// mirroring the password-login discriminated response.
// @Summary Complete federated login
// @Description Trades the one-time handoff code from the callback redirect for the login result. Answers 200 with a token pair when the identity resolves to a single account, or 202 with an account-selection challenge when it resolves to several — the same discriminated shape a password login uses. The code is single-use and short-lived, and is redeemable only together with the httpOnly binding cookie the callback set in the same browser; a same-origin request sends it without any client code.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body completeRequest true "Handoff code"
// @Success 200 {object} results.APIResponse{data=results.AuthResult} "Login successful"
// @Success 202 {object} results.APIResponse{data=results.LoginChallengeResult} "Account selection required"
// @Failure 400 {object} results.APIResponse "Code is required"
// @Failure 401 {object} results.APIResponse "Invalid or expired login code"
// @Router /auth/oidc/complete [post]
func (h *Handler) Complete(c echo.Context) error {
	var req completeRequest
	if err := c.Bind(&req); err != nil || req.Code == "" {
		return results.SendValidationError(c, "code is required")
	}
	result, ok := h.consumeHandoff(req.Code, presentedBinding(c))
	clearBindingCookie(c)
	if !ok {
		return results.SendUnauthorizedError(c, "invalid or expired login code")
	}
	if result.Tokens != nil {
		h.authService.RecordSessionDevice(c.Request().Context(), result.Tokens.RefreshToken,
			refresh_token.NewDevice(c.Request().UserAgent(), c.RealIP()))
		return results.SendOKResponse(c, results.NewAuthResult(result.Tokens, result.User), "login successful")
	}
	return results.SendSuccess(c, http.StatusAccepted, results.NewLoginChallengeResult(result), "account selection required")
}

type linkExistingRequest struct {
	LinkCode string `json:"link_code"`
	Password string `json:"password"`
}

// LinkExisting completes a login that collided with an existing local account
// the identity is not linked to: the one-time code identifies the IdP-verified
// identity held server-side (redeemable only with the binding cookie of the
// browser that earned it), and the password proves control of the local
// account. A password alone signs this account in, so linking with it grants
// nothing more; an account with an enrolled factor is refused and linked from
// settings instead. It stamps the account's email as verified, and later
// sign-ins with this provider account resolve to the link directly.
//
// @Summary Link a pending external identity to an existing account by password
// @Tags authz
// @Accept json
// @Produce json
// @Param body body linkExistingRequest true "One-time link code and the account password"
// @Success 200 {object} results.APIResponse{data=results.AuthResult} "Linked and logged in"
// @Success 202 {object} results.APIResponse{data=results.LoginChallengeResult} "Account selection required"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Invalid code or password"
// @Failure 429 {object} results.APIResponse "Too many failed attempts for this address (code login_throttled, Retry-After header)"
// @Router /auth/oidc/link-existing [post]
func (h *Handler) LinkExisting(c echo.Context) error {
	var req linkExistingRequest
	if err := c.Bind(&req); err != nil || req.LinkCode == "" || req.Password == "" {
		return results.SendValidationError(c, "link_code and password are required")
	}
	// Single-use: a wrong password burns the code and the user restarts the
	// Google round-trip. That caps password guesses per IdP round-trip at one,
	// on top of the per-IP rate limit.
	identity, ok := h.consumePendingLink(req.LinkCode, presentedBinding(c))
	clearBindingCookie(c)
	if !ok {
		return results.SendUnauthorizedError(c, "invalid or expired link code")
	}
	result, err := h.authService.LinkOIDCWithPassword(c.Request().Context(), identity, req.Password, c.RealIP())
	if err != nil {
		// This endpoint verifies a PASSWORD, so a rejection here has to reach
		// the burst detector as well as the login form — one unwatched door is
		// the one an attacker uses. Counted only for a credential rejection:
		// the service maps every "wrong account / wrong password" outcome onto
		// ErrInvalidCredentials, while a conflict or a storage failure past
		// that point is not a guess and must not raise an auth-burst anomaly.
		if errors.Is(err, app_errors.ErrInvalidCredentials) {
			h.authService.NoteFailedLogin(c.Request().Context(), c.RealIP())
		}
		// The per-address brake answers before any password is compared.
		var throttled *app_errors.LoginThrottledError
		if errors.As(err, &throttled) {
			c.Response().Header().Set("Retry-After", strconv.Itoa(throttled.RetryAfterSeconds()))
			return results.SendErrorWithCode(c, http.StatusTooManyRequests, throttled.Error(), app_errors.LoginThrottledErrorCode)
		}
		// The one refusal that is NOT uniform: it is reached only past a
		// correct password, so it reveals nothing to a guesser, and it names
		// the way forward (sign in, complete the second step, link from
		// settings) to the user who owns the account.
		if errors.Is(err, app_errors.ErrOIDCLinkNeedsSecondFactor) {
			return results.SendError(c, http.StatusForbidden, err.Error(), nil)
		}
		// Uniform response: never reveal whether the account, the password or
		// the identity was the problem.
		h.logger.Warn("oidc link-on-conflict failed", "error", err)
		return results.SendUnauthorizedError(c, "invalid or expired link code")
	}
	if result.Tokens != nil {
		h.authService.RecordSessionDevice(c.Request().Context(), result.Tokens.RefreshToken,
			refresh_token.NewDevice(c.Request().UserAgent(), c.RealIP()))
		return results.SendOKResponse(c, results.NewAuthResult(result.Tokens, result.User), "login successful")
	}
	return results.SendSuccess(c, http.StatusAccepted, results.NewLoginChallengeResult(result), "account selection required")
}

func (h *Handler) storePendingLink(code, binding string, identity services.OIDCIdentity) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for k, v := range h.pendingLinks {
		if now.After(v.expires) {
			delete(h.pendingLinks, k)
		}
	}
	h.pendingLinks[code] = pendingLink{identity: identity, binding: binding, expires: now.Add(pendingLinkTTL)}
}

// consumePendingLink spends the code whether or not the binding matches: a
// code presented from the wrong browser is dead either way.
func (h *Handler) consumePendingLink(code, binding string) (services.OIDCIdentity, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pl, ok := h.pendingLinks[code]
	if !ok {
		return services.OIDCIdentity{}, false
	}
	// Delete before the expiry check so a replay fails either way.
	delete(h.pendingLinks, code)
	if time.Now().After(pl.expires) || !bindingMatches(pl.binding, binding) {
		return services.OIDCIdentity{}, false
	}
	return pl.identity, true
}

// fail redirects to the SPA callback with an error code (the SPA shows a
// message); avoids leaking details and keeps a single UX entry point.
func (h *Handler) fail(c echo.Context, reason string) error {
	h.clearFlowCookies(c)
	return c.Redirect(http.StatusFound, frontendCallbackPath+"?error="+reason)
}

func (h *Handler) setFlowCookie(c echo.Context, name, value string, secure bool) {
	// Path "/" (not "/auth/oidc"): a gateway may prefix the browser-visible
	// path (e.g. "/api/auth/oidc/..."), so a narrower path would stop the
	// cookie being sent back on the callback.
	c.SetCookie(&http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   flowCookieMaxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearFlowCookies(c echo.Context) {
	for _, name := range []string{stateCookie, nonceCookie, inviteCookie} {
		c.SetCookie(&http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	}
}

func (h *Handler) storeHandoff(code, binding string, result *services.LoginResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Opportunistically evict expired entries.
	now := time.Now()
	for k, v := range h.handoffs {
		if now.After(v.expires) {
			delete(h.handoffs, k)
		}
	}
	h.handoffs[code] = handoff{result: result, binding: binding, expires: now.Add(handoffTTL)}
}

// consumeHandoff spends the code whether or not the binding matches, like
// consumePendingLink.
func (h *Handler) consumeHandoff(code, binding string) (*services.LoginResult, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ho, ok := h.handoffs[code]
	if !ok {
		return nil, false
	}
	delete(h.handoffs, code)
	if time.Now().After(ho.expires) || !bindingMatches(ho.binding, binding) {
		return nil, false
	}
	return ho.result, true
}

// LinkStart begins "link my Google account" for the signed-in user: it stores
// a short-lived ticket bound to their id and sets a cookie the callback reads.
// The SPA then navigates to /auth/oidc/start.
// @Summary Start linking an external identity
// @Description Requires a step-up confirmation, because the identity it attaches is a way to sign in that outlives a password change, a passkey rotation, a TOTP enrolment and a session revocation. Send the ticket in X-Reauth-Ticket, or expect 403 with code reauth_required. A user whose only credential is the IdP makes that proof at /auth/oidc/reauth.
// @Tags authz
// @Security ApiKeyAuth
// @Param X-Reauth-Ticket header string false "Step-up confirmation ticket"
// @Success 200 {object} results.APIResponse "Link started"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 403 {object} results.APIResponse "Step-up confirmation required (code reauth_required)"
// @Router /auth/oidc/link [post]
func (h *Handler) LinkStart(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	code, err := randomToken()
	if err != nil {
		return results.SendInternalServerError(c, "failed to start link", err)
	}
	h.storeLinkTicket(code, userID, middleware.GetSessionProofFromContext(c))
	secure := c.Request().TLS != nil || c.Scheme() == "https"
	c.SetCookie(&http.Cookie{
		Name: linkCookie, Value: code, Path: "/", MaxAge: flowCookieMaxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	// Recorded HERE rather than where the link is actually made, because the
	// callback that makes it is public and has no account, while
	// audit_events.account_id is NOT NULL. The target is the USER, since no
	// identity row exists yet at this point. See ActionExternalIdentityLinkStart.
	if h.audit != nil {
		if accountID, aerr := middleware.GetAccountIDFromContext(c); aerr == nil {
			h.audit.Record(c.Request().Context(), audit.UserActor(accountID, userID),
				audit_event.ActionExternalIdentityLinkStart,
				audit_event.TargetUser, userID.String())
		}
	}
	return results.SendOKResponse(c, nil, "link started")
}

// identityResult describes one linked identity.
//
// ID is what makes the list addressable, and its absence is why the unlink
// route could not be driven at all before this: the SPA could see that a Google
// account was linked and had no way to name it in a request.
type identityResult struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

// Identities returns the current user's linked external identities.
// @Summary List my external identities
// @Description Live links only — an identity that was unlinked is not listed, and is not counted as a credential.
// @Tags authz
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} results.APIResponse{data=[]identityResult} "Linked identities"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Router /auth/oidc/identities [get]
func (h *Handler) Identities(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	ids, err := h.authService.ListIdentities(c.Request().Context(), userID)
	if err != nil {
		return results.SendInternalServerError(c, "failed to list identities", err)
	}
	out := make([]identityResult, 0, len(ids))
	for i := range ids {
		out = append(out, identityResult{
			ID:        ids[i].ID.String(),
			Provider:  ids[i].Provider,
			Email:     ids[i].Email,
			CreatedAt: ids[i].CreatedAt.Format(time.RFC3339),
		})
	}
	return results.SendOKResponse(c, out, "identities retrieved successfully")
}

// UnlinkIdentity detaches one federated identity from the current user.
//
// @Summary Unlink one of my external identities
// @Description Detaches a federated sign-in from the current account. Refused with 409 when it is the last way the user could sign in — the remaining set counts password, passkeys and other linked identities, and deliberately NOT TOTP, which is a second factor and cannot start a session. The removal is a soft delete: an operator can restore it, the user cannot, so re-linking means going through the provider again.
// @Tags authz
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "External identity id, from GET /auth/oidc/identities"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Malformed identity id"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "No such identity for this user"
// @Failure 409 {object} results.APIResponse "This is the last remaining sign-in method"
// @Router /auth/oidc/identities/{id} [delete]
func (h *Handler) UnlinkIdentity(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		return results.SendUnauthorizedError(c, "unauthorized")
	}
	identityID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return results.SendValidationError(c, "invalid identity id")
	}
	switch err := h.authService.UnlinkOIDC(c.Request().Context(), userID, identityID); {
	case err == nil:
	case errors.Is(err, app_errors.ErrExternalIdentityNotFound):
		// Deliberately the same answer for "no such id", "belongs to someone
		// else" and "already revoked", so this cannot enumerate other accounts'
		// identity ids.
		return results.SendNotFoundError(c, "external identity not found")
	case errors.Is(err, app_errors.ErrExternalIdentityLastCredential):
		return results.SendError(c, http.StatusConflict, err.Error(), nil)
	default:
		return results.SendInternalServerError(c, "failed to unlink identity", err)
	}

	// Best-effort, like every audit write except the terms acknowledgement: the
	// identity is already detached, and failing the request because its record
	// would not write turns bookkeeping into an outage.
	if h.audit != nil {
		if accountID, aerr := middleware.GetAccountIDFromContext(c); aerr == nil {
			h.audit.Record(c.Request().Context(), audit.UserActor(accountID, userID),
				audit_event.ActionExternalIdentityUnlink,
				audit_event.TargetExternalIdentity, identityID.String())
		}
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handler) storeLinkTicket(code string, userID uuid.UUID, session services.SessionProof) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for k, v := range h.linkTickets {
		if now.After(v.expires) {
			delete(h.linkTickets, k)
		}
	}
	h.linkTickets[code] = linkTicket{userID: userID, session: session, expires: now.Add(linkTicketTTL)}
}

// consumeLinkTicket returns the user id and the session bound to the link
// cookie (one-time), or ok=false when there is no valid link in progress.
func (h *Handler) consumeLinkTicket(c echo.Context) (uuid.UUID, services.SessionProof, bool) {
	ck, err := c.Cookie(linkCookie)
	if err != nil || ck.Value == "" {
		return uuid.Nil, services.SessionProof{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.linkTickets[ck.Value]
	if !ok {
		return uuid.Nil, services.SessionProof{}, false
	}
	delete(h.linkTickets, ck.Value)
	if time.Now().After(t.expires) {
		return uuid.Nil, services.SessionProof{}, false
	}
	return t.userID, t.session, true
}

// completeLink is the callback's link arm: the IdP has verified an identity
// and the ticket names the user and session that asked for it. The session is
// re-proven live HERE, immediately before the identity is persisted — the
// start route's step-up proved it minutes ago, and a sign-out, a "sign out
// everywhere" or a password reset in between must end the link the same way
// they end the step-up ticket. Fails closed: an unreadable
// session state refuses, exactly like the guarded routes do.
func (h *Handler) completeLink(c echo.Context, userID uuid.UUID, session services.SessionProof, identity services.OIDCIdentity) error {
	ctx := c.Request().Context()
	// One transition: the live-session test and the insert share the user's
	// row lock inside the service's repository call, so a revocation cannot
	// land between them.
	if err := h.authService.LinkOIDCFromSession(ctx, userID, identity, session); err != nil {
		if errors.Is(err, app_errors.ErrExternalIdentityConflict) {
			return c.Redirect(http.StatusFound, accountPath+"?oidc_link=conflict")
		}
		if errors.Is(err, app_errors.ErrTokenInvalid) || errors.Is(err, app_errors.ErrUserNotFound) {
			h.logger.Warn("oidc link refused: the session that started it is no longer live",
				"user_id", userID, "family_id", session.FamilyID, "error", err)
			return c.Redirect(http.StatusFound, accountPath+"?oidc_link=error")
		}
		h.logger.Warn("oidc link failed", "error", err, "user_id", userID)
		return c.Redirect(http.StatusFound, accountPath+"?oidc_link=error")
	}
	return c.Redirect(http.StatusFound, accountPath+"?oidc_link=ok")
}

func (h *Handler) clearLinkCookie(c echo.Context) {
	c.SetCookie(&http.Cookie{Name: linkCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
