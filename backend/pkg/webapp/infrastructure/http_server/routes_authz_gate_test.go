package http_server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	ws_ports "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	access_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/access"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	email_verification_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/email_verification"
	entitlement_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/entitlement"
	master_token_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/master_token"
	member_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/member"
	node_activity_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_activity"
	node_attention_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_attention"
	node_favorite_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_favorite"
	node_replace_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_replace"
	nodeSSHAdoptService "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_ssh_adopt"
	organizational_unit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/organizational_unit"
	passkey_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/passkey"
	password_reset_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/password_reset"
	reauth_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/reauth"
	secret_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret"
	secret_tag_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret_tag"
	ssh_key_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/ssh_key"
	two_factor_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/two_factor"
	utility_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/utility"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	workflow_template_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_template"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	oidcapi "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/oidc"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/utility_registry"
)

// Why this file exists.
//
// The JWT middleware validates signature, expiry and token type and never
// resolves membership. What refuses a REMOVED member is the authz enforcer:
// every Authorize / Can / AllowedOUs / PermissionMap / RequireActiveMembership
// call resolves the membership row first and fails on a missing or inactive
// one, and the member service invalidates the enforcer's cache on every
// mutation. So a removed member is refused at once on every route that
// consults the enforcer — and keeps their access until the token expires ONLY
// on the JWT-authed routes that never do.
//
// This test measures that set, by driving rather than by reading: Echo exposes
// a route's method and path but not its middleware chain, and a handler may
// consult the enforcer in its body, through a helper, or only after it has
// fetched the resource it authorizes against (authorizeNodeAction loads the
// node to learn its OU). The real router is built with a membership port that
// RECORDS every resolution and answers "not a member"; every JWT-authed route
// is then driven with a valid access token for that principal, and classified
// by whether membership was resolved at all. A route that resolved it refused
// the removed member; a route that did not ran its handler on the strength of
// the token alone.
//
// The ungated set is asserted against the explicit allowlist below, each entry
// with its reason. A route that is ungated and not listed fails, so the window
// cannot grow silently; a listed route that has since gained a gate fails too,
// so the list cannot rot into an overstatement.
//
// What is measured is "was membership resolved", not "was the Enforcer type
// called": the member service resolves membership through the same repository
// port on its own (requireActiveMember / requireSuperAdmin) and never calls the
// enforcer, so it is wired over the same recording port and counts the same way.
// The AuthService is stubbed, so its own in-service resolution (switch-account,
// the accounts list) is NOT seen by the probe; those routes are allowlisted as
// self-only with that noted.

// ungatedExposure classifies what a route hands to a caller whose membership
// nobody checked.
type ungatedExposure string

const (
	// exposureSelf: the response is about the caller's own user record,
	// credentials or sessions — nothing another tenant member owns.
	exposureSelf ungatedExposure = "self-only"
	// exposureStatic: tenant-independent content (a registry, a config flag,
	// a signer-served blob) identical for every caller.
	exposureStatic ungatedExposure = "static"
	// exposureTenantRead: a read of the account's own data. A WRITE against
	// tenant state would be a fourth class; no ungated route is one today, and
	// the first to appear should be gated rather than classified.
	exposureTenantRead ungatedExposure = "TENANT READ"
)

type ungatedRoute struct {
	exposes ungatedExposure
	reason  string
}

// ungatedRoutes is the measured set of JWT-authed routes that never resolve
// membership. Keys are canonical (every parameter is {}). Adding an entry is a
// product decision about that route, not a way to silence the test; removing
// one is what closing a gap looks like.
var ungatedRoutes = map[string]ungatedRoute{
	// --- the caller's own identity, credentials and sessions ----------------
	"GET /api/v1/users/me":                           {exposureSelf, "own profile"},
	"PUT /api/v1/users/me":                           {exposureSelf, "own profile; step-up gated (email is the sign-in identifier)"},
	"PUT /api/v1/users/me/password":                  {exposureSelf, "own password; step-up gated"},
	"POST /api/v1/users/me/beta-acknowledgement":     {exposureSelf, "a statement about oneself; the audit row is the consent record"},
	"POST /api/v1/users/me/resend-verification":      {exposureSelf, "mails the caller's own address; rate-limited"},
	"GET /api/v1/users/me/2fa":                       {exposureSelf, "own second-factor state"},
	"POST /api/v1/users/me/2fa/enroll":               {exposureSelf, "own factor; step-up gated"},
	"POST /api/v1/users/me/2fa/confirm":              {exposureSelf, "own factor; step-up gated"},
	"DELETE /api/v1/users/me/2fa":                    {exposureSelf, "own factor; demands a current code, a stronger proof than a ticket"},
	"GET /api/v1/users/me/passkeys":                  {exposureSelf, "own passkeys"},
	"POST /api/v1/users/me/passkeys/register/begin":  {exposureSelf, "own passkey; step-up gated"},
	"POST /api/v1/users/me/passkeys/register/finish": {exposureSelf, "own passkey; step-up gated"},
	"DELETE /api/v1/users/me/passkeys/{}":            {exposureSelf, "removal only reduces access; deliberately not step-up gated"},
	"GET /api/v1/users/me/reauth/methods":            {exposureSelf, "step-up endpoint: which proofs the caller can make"},
	"POST /api/v1/users/me/reauth/password":          {exposureSelf, "step-up endpoint: mints the ticket, so it cannot demand one"},
	"POST /api/v1/users/me/reauth/totp":              {exposureSelf, "step-up endpoint"},
	"POST /api/v1/users/me/reauth/passkey/begin":     {exposureSelf, "step-up endpoint"},
	"POST /api/v1/users/me/reauth/passkey/finish":    {exposureSelf, "step-up endpoint"},
	"GET /auth/sessions":                             {exposureSelf, "own session inventory"},
	"DELETE /auth/sessions/{}":                       {exposureSelf, "own session; ownership is in the repository query"},
	"POST /auth/sessions/revoke-others":              {exposureSelf, "own sessions"},
	"GET /auth/accounts":                             {exposureSelf, "accounts the caller is an active member of; membership resolved inside AuthService (stubbed here)"},
	"POST /auth/switch-account":                      {exposureSelf, "re-resolves membership inside AuthService.SwitchAccount (stubbed here); a removed member gets no token for that account"},
	"GET /auth/oidc/identities":                      {exposureSelf, "own linked identities"},
	"POST /auth/oidc/link":                           {exposureSelf, "attaches an identity to the caller; step-up gated"},
	"DELETE /auth/oidc/identities/{}":                {exposureSelf, "removal only reduces access; deliberately not step-up gated"},
	"POST /auth/oidc/reauth":                         {exposureSelf, "IdP step-up endpoint; the only proof a federated-only user can make"},
	"POST /auth/oidc/reauth/complete":                {exposureSelf, "IdP step-up endpoint"},
	"POST /api/v1/invites/accept":                    {exposureSelf, "joins the caller to the invite's account; the caller is by definition not yet a member there"},
	"DELETE /api/v1/members/me":                      {exposureSelf, "self-leave; Service.Remove skips the super_admin check for the caller's own id, and the repository enforces the last-super_admin invariant"},

	// --- tenant-independent content ----------------------------------------
	"GET /api/v1/server/capabilities":  {exposureStatic, "deployment feature flags off *config.Config; authed because they leak topology, action-free because they are the same for every tenant"},
	"GET /api/v1/workflow-blocks":      {exposureStatic, "workflow_block.Registry() verbatim — the documented catalog precedent"},
	"POST /api/v1/schedule/preview":    {exposureStatic, "pure calculator over a cron string; no tenant data"},
	"GET /api/v1/device-profiles":      {exposureStatic, "device-profile registry, identical for every tenant (comment at its registration)"},
	"GET /api/v1/utilities/catalog":    {exposureStatic, "utility registry, identical for every tenant (comment at its registration)"},
	"GET /api/v1/eeprom/base-firmware": {exposureStatic, "signer's bootloader base catalog; no tenant data, no comment at its registration"},
	"GET /api/v1/eeprom/bootcode/{}":   {exposureStatic, "signer-served recovery.bin per SoC; no tenant data, no comment at its registration"},

	// --- a personal view over tenant objects --------------------------------
	"GET /api/v1/nodes/favorites": {exposureSelf, "the caller's OWN favourite marks (node ids they chose); deliberately action-free, comment at its registration"},

	// --- tenant data reachable on the token alone ---------------------------
}

// gatedOtherStatus names routes that DO resolve membership but answer a
// removed member with something other than the membership 401: a refusal all
// the same, worth knowing, not a window. Informational — the test logs them.
// A 5xx is NOT informational: it means the handler wrapped a membership error
// as a platform failure (a handler calling AllowedOUs or PermissionMap
// directly must route the error through middleware.MembershipHTTPError). The test FAILS on any gated route that
// answers 5xx, so the shape cannot come back.
var gatedOtherStatus = map[string]string{
	"GET /api/v1/members": "the member service's own requireActiveMember answers ErrUnauthorized, rendered 403",
}

// routeBodies are the minimal request bodies for handlers that BIND and
// validate before they authorize. Without one those routes answer 400 before
// the enforcer is reached and would read as ungated; the body exists only to
// get past validation, never to succeed.
var routeBodies = map[string]string{
	"POST /api/v1/nodes/{}/command":   `{"command":"reboot"}`,
	"POST /api/v1/nodes/commands":     `{"command":"reboot","node_ids":["` + gateResourceID + `"]}`,
	"POST /api/v1/nodes/{}/overclock": `{"preset":"stock"}`,
}

// routeQueries are the query strings for handlers that validate a query
// parameter before they authorize, for the same reason as routeBodies.
var routeQueries = map[string]string{
	"GET /api/v1/nodes/{}/files/content":  "path=/etc/hostname",
	"GET /api/v1/nodes/{}/files/versions": "path=/etc/hostname",
}

// gateResourceID is the id every path parameter is filled with (except the
// account's own id on /accounts/:id, which the handler compares to the token).
// The stubs answer any id, so the value only has to parse.
const gateResourceID = "0f6b1c2e-7a4d-4e3b-9c1a-2b3c4d5e6f70"

// TestJWTRoutesConsultTheEnforcer is the measurement.
func TestJWTRoutesConsultTheEnforcer(t *testing.T) {
	h := newGateHarness(t)

	gated := map[string]int{}
	ungated := map[string]int{}
	var jwtRoutes []string

	for _, r := range h.e.Routes() {
		if r.Method == "" || r.Path == "" || r.Method == echo.RouteNotFound {
			continue
		}
		key := r.Method + " " + canonical(r.Path)
		if _, seen := gated[key]; seen {
			continue
		}
		if _, seen := ungated[key]; seen {
			continue
		}
		if !h.isJWTAuthed(r.Method, r.Path) {
			continue
		}
		jwtRoutes = append(jwtRoutes, key)

		status := h.driveAsRemovedMember(r.Method, r.Path, key)
		if h.probe.Consulted() > 0 {
			gated[key] = status
		} else {
			ungated[key] = status
		}
	}
	sort.Strings(jwtRoutes)

	if len(jwtRoutes) < 100 {
		t.Fatalf("only %d JWT-authed routes found; the token or the no-token probe is broken", len(jwtRoutes))
	}

	// The probe must be real: a gated route refuses the removed member with the
	// membership 401 (membershipHTTPError), not with a stub failure.
	if status := gated["GET /api/v1/secrets"]; status != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/secrets answered %d to a removed member; the non-member enforcer is not refusing", status)
	}

	var missing, stale []string
	for key := range ungated {
		if _, listed := ungatedRoutes[key]; !listed {
			missing = append(missing, key)
		}
	}
	for key := range ungatedRoutes {
		if _, isGated := gated[key]; isGated {
			stale = append(stale, key)
			continue
		}
		if _, isUngated := ungated[key]; !isUngated {
			stale = append(stale, key+" (not a JWT-authed route at all)")
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("%d JWT-authed route(s) ran their handler for a REMOVED member without resolving membership and are not in ungatedRoutes.\n"+
			"Gate the route (an authz action, or RequireActiveMember for a membership-only read), or add it to the allowlist with its reason:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d ungatedRoutes entry(ies) describe a route that now resolves membership, or no longer exists; remove them so the list stays a measurement:\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}

	// The inventory, in the test log, so a run IS the report.
	t.Logf("JWT-authed routes: %d total, %d resolve membership, %d do not", len(jwtRoutes), len(gated), len(ungated))
	var report []string
	for key, status := range ungated {
		entry := ungatedRoutes[key]
		report = append(report, key+" -> "+string(entry.exposes)+" (status "+http.StatusText(status)+"): "+entry.reason)
	}
	sort.Strings(report)
	for _, line := range report {
		t.Log(line)
	}
	var other, serverErrors []string
	for key, status := range gated {
		if status >= http.StatusInternalServerError {
			serverErrors = append(serverErrors, fmt.Sprintf("%s answers %d", key, status))
			continue
		}
		if status != http.StatusUnauthorized {
			other = append(other, fmt.Sprintf("gated but answers %d to a removed member: %s %s", status, key, gatedOtherStatus[key]))
		}
	}
	sort.Strings(other)
	for _, line := range other {
		t.Log(line)
	}
	sort.Strings(serverErrors)
	if len(serverErrors) > 0 {
		t.Errorf("a removed member must be REFUSED (401/403), never answered a platform failure — route through middleware.MembershipHTTPError:\n  %s",
			strings.Join(serverErrors, "\n  "))
	}
}

// --- harness ----------------------------------------------------------------

type gateHarness struct {
	t         *testing.T
	e         *echo.Echo
	probe     *authztest.Probe
	token     string
	ticket    string
	accountID uuid.UUID
	clients   int
}

func newGateHarness(t *testing.T) *gateHarness {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	enforcer, probe := authztest.NonMemberProbe()
	recorder, _ := audittest.New()
	userID, accountID := uuid.New(), uuid.New()

	// The step-up gate sits in front of several self routes; a real reauth
	// service mints a ticket so the drive gets PAST it to whatever follows.
	// The ticket is bound to the access token's session, and both are only
	// honoured while the session is live — a stub says it is, and the token
	// below carries the same family and mint time.
	reauthSvc := reauth_service.NewService(gateUserReader{}, gatePasskeyCounter{}, gateTwoFactor{}, gateHasher{}, logger)
	reauthSvc.SetSessionVerifier(gateSessions{})
	familyID, issuedAt := uuid.New(), time.Now().Truncate(time.Second)
	ticket, err := reauthSvc.Grant(context.Background(), userID, services.SessionProof{FamilyID: familyID, IssuedAt: issuedAt})
	if err != nil {
		t.Fatalf("minting a step-up ticket: %v", err)
	}

	// The two-factor policy runs on every authenticated request; its account
	// lookup fails, which the policy treats as "not required".
	twoFactorPolicy := two_factor_service.NewPolicy(gateAccountRepo{}, &two_factor_service.Service{}, logger)

	// Services that FETCH before they authorize get a stub that answers, so the
	// handler reaches its authorization instead of dying on a nil port. Each
	// stub returns the minimum a handler needs to get to the enforcer.
	catalog, err := utility_registry.Load()
	if err != nil {
		t.Fatalf("loading the utility registry: %v", err)
	}
	utilitySvc := utility_service.NewService(catalog, gateAssignmentRepo{}, gateUtilityStateRepo{},
		gateOURepo{}, gateNodeRepo{}, recorder, logger, 0, 0)
	templateSvc := workflow_template_service.NewService(gateTemplateRepo{}, recorder, logger)
	// The member service resolves membership on its own, through the same port
	// the enforcer uses — so it is wired over the same probe.
	memberSvc := member_service.NewMemberService(gateUserRepo{}, gateAccountRepo{},
		authztest.NonMemberRepository(probe), gateBindingRepo{}, gateMutationRepo{},
		gateInviteRepo{}, gateOURepo{}, gateAuditRepo{}, stubAuthService{},
		config.MemberConfig{}, logger)

	cfg := routesTestConfig()
	srv := NewServer(stubAuthService{}, make(chan ws_ports.Message, 1), logger, cfg)
	srv.RegisterReadiness(routesTestProbe{})
	e := srv.echo
	// A handler that panics on a nil stub is recovered and logged by Echo;
	// the log is not the measurement.
	e.Logger.SetOutput(io.Discard)
	SetupRoutes(
		e,
		cfg,
		stubAuthService{}, stubUserService{}, stubAccountService{}, gateNodeService{},
		stubImageService{}, stubFileService{}, gateNodeCommandService{},
		&ssh_key_service.Service{}, &master_token_service.Service{},
		&organizational_unit_service.Service{}, &access_service.Service{},
		utilitySvc,
		&node_favorite_service.Service{}, &secret_service.Service{},
		&secret_tag_service.Service{}, &node_replace_service.Service{},
		memberSvc, enforcer,
		&oidcapi.Client{},
		stubStorageClient{},
		stubNodeCommunicationService{},
		&websockets.WebSocketManager{},
		templateSvc, &workflow_execution_service.Service{},
		&nodeSSHAdoptService.Service{}, &email_verification_service.Service{},
		&password_reset_service.Service{}, &two_factor_service.Service{},
		twoFactorPolicy, &passkey_service.Service{},
		reauthSvc, recorder, &audit_service.Reader{},
		stubAccountDeletionRequestRepo{},
		// No signer and no firmware cache: both are optional on a real install
		// too, and the routes register either way.
		nil, nil,
		&node_attention_service.Service{}, &node_activity_service.Reader{},
		&entitlement_service.Service{},
		logger,
	)

	claims := &services.TokenClaims{
		UserID: userID, AccountID: accountID, Email: "removed@example.invalid",
		TokenType: services.TokenTypeAccess, FamilyID: familyID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(cfg.JWT.SecretKey))
	if err != nil {
		t.Fatalf("signing the access token: %v", err)
	}

	return &gateHarness{t: t, e: e, probe: probe, token: token, ticket: ticket, accountID: accountID}
}

var gateParam = regexp.MustCompile(`:[^/]+|\*`)

// freshClientAddr gives every request its own client IP. Several groups carry
// a per-IP rate limiter (/auth, the invite routes); the drive makes a few
// hundred requests from one process, and a 429 from the limiter would be
// misread as "not JWT-authed" by the no-token probe.
func (h *gateHarness) freshClientAddr() string {
	h.clients++
	return fmt.Sprintf("10.%d.%d.%d:4000", (h.clients>>16)&0xff, (h.clients>>8)&0xff, h.clients&0xff)
}

// concretePath fills every path parameter. /accounts/:id gets the token's
// account, because the handler compares the two before doing anything; the
// SoC parameter gets a value its allowlist accepts; everything else an id.
func (h *gateHarness) concretePath(path string) string {
	if path == "/api/v1/accounts/:id" {
		return "/api/v1/accounts/" + h.accountID.String()
	}
	return gateParam.ReplaceAllStringFunc(path, func(p string) string {
		if p == ":soc" {
			return "cm4"
		}
		return gateResourceID
	})
}

// isJWTAuthed drives the route with NO credential and recognises the JWT
// middleware's own refusal. Public routes, the websocket upgrades and the
// HMAC-capability routes answer anything but that exact message.
func (h *gateHarness) isJWTAuthed(method, path string) bool {
	req := httptest.NewRequest(method, h.concretePath(path), nil)
	req.RemoteAddr = h.freshClientAddr()
	rec := httptest.NewRecorder()
	h.e.ServeHTTP(rec, req)
	return rec.Code == http.StatusUnauthorized &&
		strings.Contains(rec.Body.String(), "missing authorization header")
}

// driveAsRemovedMember sends one request with a valid access token (and a
// step-up ticket) for the principal the probe reports as not a member, and
// returns the status. The probe is reset first, so Consulted() afterwards is
// this request's count alone. A handler that panics on a nil stub is caught by
// the server's Recover middleware — a 500 with zero consultations is exactly
// "ran without asking".
func (h *gateHarness) driveAsRemovedMember(method, path, key string) int {
	h.probe.Reset()
	var body *strings.Reader
	if b, ok := routeBodies[key]; ok {
		body = strings.NewReader(b)
	} else {
		body = strings.NewReader("")
	}
	target := h.concretePath(path)
	if q, ok := routeQueries[key]; ok {
		target += "?" + q
	}
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = h.freshClientAddr()
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("X-Reauth-Ticket", h.ticket)
	if body.Len() > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.e.ServeHTTP(rec, req)
	return rec.Code
}

// --- stubs --------------------------------------------------------------------
//
// Each returns the least a handler needs to reach its authorization. A method
// not listed is a nil embedded interface and panics, which the Recover
// middleware turns into a 500 — and a 500 AFTER the enforcer was consulted is
// still a refusal, so those panics do not distort the measurement.

var errGateStub = errors.New("gate stub")

type gateNodeService struct{ services.NodeService }

func (gateNodeService) GetByIDAndAccount(_ context.Context, nodeID, accountID uuid.UUID) (*node.Node, error) {
	return &node.Node{ID: nodeID, AccountID: accountID}, nil
}

func (gateNodeService) GetAllByAccountID(context.Context, node_list.NodeListQuery) ([]node.Node, error) {
	return nil, nil
}

type gateNodeCommandService struct{ services.NodeCommandService }

func (gateNodeCommandService) GetNodeCommandByID(_ context.Context, accountID, commandID uuid.UUID) (*node_command.NodeCommand, error) {
	return &node_command.NodeCommand{ID: commandID, AccountID: accountID, NodeID: uuid.MustParse(gateResourceID)}, nil
}

type gateAssignmentRepo struct {
	repository.UtilityAssignmentRepository
}

func (gateAssignmentRepo) GetByID(_ context.Context, accountID, id uuid.UUID) (*utility.Assignment, error) {
	return &utility.Assignment{ID: id, AccountID: accountID, AccountWide: true}, nil
}

type gateUtilityStateRepo struct {
	repository.NodeUtilityStateRepository
}

type gateNodeRepo struct{ repository.NodeRepository }

func (gateNodeRepo) GetByID(_ context.Context, accountID, nodeID uuid.UUID) (*node.Node, error) {
	return &node.Node{ID: nodeID, AccountID: accountID}, nil
}

type gateTemplateRepo struct {
	repository.WorkflowTemplateRepository
}

func (gateTemplateRepo) GetByID(_ context.Context, accountID, templateID uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	return &workflow_template.WorkflowTemplate{ID: templateID, AccountID: accountID}, nil
}

type gateOURepo struct {
	repository.OrganizationalUnitRepository
}
type gateUserRepo struct{ repository.UserRepository }
type gateBindingRepo struct {
	repository.RoleBindingRepository
}
type gateMutationRepo struct {
	repository.MemberMutationRepository
}
type gateInviteRepo struct {
	repository.AccountInviteRepository
}
type gateAuditRepo struct {
	repository.AuditEventRepository
}

type gateAccountRepo struct{ repository.AccountRepository }

func (gateAccountRepo) GetByID(context.Context, uuid.UUID) (*account.Account, error) {
	return nil, errGateStub
}

type gateUserReader struct{}

func (gateUserReader) GetByID(context.Context, uuid.UUID) (*user.User, error) {
	return nil, errGateStub
}

type gatePasskeyCounter struct{}

func (gatePasskeyCounter) CountByUserID(context.Context, uuid.UUID) (int64, error) { return 0, nil }

type gateTwoFactor struct{}

func (gateTwoFactor) HasConfirmedFactor(context.Context, uuid.UUID) (bool, error) { return false, nil }
func (gateTwoFactor) Verify(context.Context, uuid.UUID, string) error             { return errGateStub }

// gateSessions calls every session live: what is under test here is the
// authorization AFTER the step-up, not the step-up itself.
type gateSessions struct{}

func (gateSessions) RequireLiveSession(context.Context, uuid.UUID, services.SessionProof) error {
	return nil
}

type gateHasher struct{}

func (gateHasher) ComparePasswords(string, string) error { return errGateStub }
