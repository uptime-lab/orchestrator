package http_server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	ws_ports "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	access_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/access"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
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
	oidcapi "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/oidc"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

// Why this file exists.
//
// The Swagger annotations and the routes the server actually registers had
// drifted for months, in BOTH directions: routes nobody documented, and
// documented routes that do not exist. The worst of them published
// `GET|PUT /api/v1/nodes/{id}/files/{path}` — the exact route shape that could
// not coexist in Echo and was replaced by `/files/content?path=` — so a client
// written against the published contract called a 404 and had no way to know the
// document was wrong.
//
// A one-off cleanup does not fix that: the counts moved with every route added,
// and every later count found a bigger gap. This test is the structural half —
// it fails the moment a registered route has no `@Router` annotation behind it,
// which is the point where the author still remembers what the route does.
//
// It reads `docs/swagger.json`, i.e. the GENERATED document. That is deliberate:
// running `make backend/docs` is what publishes an annotation, and a test that
// read the annotations directly would pass while the shipped document stayed
// stale.

// nonAPIRoutes are registered routes that are deliberately NOT in the OpenAPI
// document, each for a stated reason. Adding to this list is a decision; it is
// short on purpose.
// Keys are canonical (see canonical): every parameter and wildcard is {}, so an
// entry here cannot be defeated by renaming a path parameter.
var nonAPIRoutes = map[string]string{
	// Not HTTP request/response: the client upgrades and then speaks a frame
	// protocol that OpenAPI cannot describe.
	"GET /ws": "websocket upgrade, not a REST endpoint",

	// Machine surfaces with their own contracts, spoken by the Raspberry Pi boot
	// ROM, by the recovery bootstrap and by install.sh — never by an API client.
	// The ROM's contract is a signature file and an image; describing it in
	// OpenAPI would suggest it is negotiable.
	"GET /http-boot/{}/{}":       "per-serial bootstrap, fetched by the Pi boot ROM",
	"GET /http-boot/time":        "clock sync for a netbooted device whose clock is too far off for TLS",
	"GET /net_install/agent/{}":  "netboot agent binary, fetched by the recovery bootstrap",
	"GET /net_install/eeprom/{}": "signed per-serial EEPROM package tree",

	// Echo's own plumbing.
	"GET /{}": "SPA catch-all",
}

// TestEveryRegisteredRouteIsDocumented walks the routes the server really
// registers and asserts each one appears in the generated OpenAPI document.
func TestEveryRegisteredRouteIsDocumented(t *testing.T) {
	registered := registeredRoutes(t)
	documented := documentedRoutes(t)

	var missing []string
	for _, r := range registered {
		if _, deliberate := nonAPIRoutes[r]; deliberate {
			continue
		}
		if !documented[r] {
			missing = append(missing, r)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d registered route(s) have no @Router annotation in docs/swagger.json.\n"+
			"Add the annotation to the handler and run `make backend/docs`; if the route is\n"+
			"deliberately not part of the API, add it to nonAPIRoutes with a reason.\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestNoDocumentedRouteIsAPhantom is the other direction, and it is the one that
// misleads a client: a published route that the server does not serve answers
// 404, and the caller has no way to tell a bad document from a broken server.
func TestNoDocumentedRouteIsAPhantom(t *testing.T) {
	registered := map[string]bool{}
	for _, r := range registeredRoutes(t) {
		registered[r] = true
	}

	var phantom []string
	for r := range documentedRoutes(t) {
		if !registered[r] {
			phantom = append(phantom, r)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Errorf("%d documented route(s) are not registered by the server:\n  %s",
			len(phantom), strings.Join(phantom, "\n  "))
	}
}

// registeredRoutes builds the real router and returns "METHOD /path" for every
// route on it, normalised to the OpenAPI parameter spelling.
func registeredRoutes(t *testing.T) []string {
	t.Helper()
	// Through NewServer + RegisterReadiness, not a bare echo.New(): /health is
	// registered inside NewServer and /ready inside RegisterReadiness, so an
	// inventory built from SetupRoutes alone silently omits the two endpoints an
	// external monitor actually calls — which is the same class of blind spot
	// this file exists to close.
	srv := NewServer(stubAuthService{}, make(chan ws_ports.Message, 1),
		slog.New(slog.DiscardHandler), routesTestConfig())
	srv.RegisterReadiness(routesTestProbe{})
	e := srv.echo
	SetupRoutes(
		e,
		routesTestConfig(),
		stubAuthService{}, stubUserService{}, stubAccountService{}, stubNodeService{},
		stubImageService{}, stubFileService{}, stubNodeCommandService{},
		&ssh_key_service.Service{}, &master_token_service.Service{},
		&organizational_unit_service.Service{}, &access_service.Service{},
		&utility_service.Service{},
		&node_favorite_service.Service{}, &secret_service.Service{},
		&secret_tag_service.Service{}, &node_replace_service.Service{},
		&member_service.Service{}, &authz_service.Enforcer{},
		&oidcapi.Client{},
		stubStorageClient{},
		stubNodeCommunicationService{},
		&websockets.WebSocketManager{},
		&workflow_template_service.Service{}, &workflow_execution_service.Service{},
		&nodeSSHAdoptService.Service{}, &email_verification_service.Service{},
		&password_reset_service.Service{}, &two_factor_service.Service{},
		&two_factor_service.Policy{}, &passkey_service.Service{},
		&reauth_service.Service{}, &audit_service.Recorder{}, &audit_service.Reader{},
		stubAccountDeletionRequestRepo{},
		// No signer and no firmware cache: both are optional on a real install
		// too, and the routes register either way.
		nil, nil,
		&node_attention_service.Service{}, &node_activity_service.Reader{},
		&entitlement_service.Service{},
		slog.New(slog.DiscardHandler),
	)

	seen := map[string]bool{}
	var out []string
	for _, r := range e.Routes() {
		// Echo synthesises a `echo_route_not_found` entry per GROUP so that an
		// unmatched path under it still runs the group's middleware. Those are
		// not routes a client can call, and they carry the group prefix rather
		// than any real path.
		if r.Method == "" || r.Path == "" || r.Method == echo.RouteNotFound {
			continue
		}
		key := r.Method + " " + canonical(r.Path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

var pathParam = regexp.MustCompile(`\{[^}]*\}|:[^/]+|\*`)

// canonical erases parameter NAMES, so `/nodes/:id` and `/nodes/{nodeId}` are
// the same route and Echo's `/cdn/*` is the same as the document's
// `/cdn/{path}`. What is compared is the SHAPE — which segments are literal and
// which are variable — because that is what a client has to get right. A
// parameter named differently in the document is a cosmetic mismatch; a segment
// that is literal in one and variable in the other is a 404.
func canonical(path string) string {
	return pathParam.ReplaceAllString(path, "{}")
}

// documentedRoutes reads the generated OpenAPI document and returns the same
// canonical "METHOD /path" keys.
func documentedRoutes(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "swagger.json"))
	if err != nil {
		t.Fatalf("reading the generated OpenAPI document (run `make backend/docs`): %v", err)
	}
	var doc struct {
		BasePath string                            `json:"basePath"`
		Paths    map[string]map[string]interface{} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing docs/swagger.json: %v", err)
	}

	out := map[string]bool{}
	for path, ops := range doc.Paths {
		echoPath := canonical(doc.BasePath + path)
		for method := range ops {
			switch strings.ToUpper(method) {
			case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
				out[fmt.Sprintf("%s %s", strings.ToUpper(method), echoPath)] = true
			}
		}
	}
	return out
}

// routesTestConfig is the minimum the router needs to register EVERYTHING. The
// quota values stay zero so the rate limiter is a pass-through.
func routesTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.EEPROMSigner.URL = "http://eeprom-signer:9099"
	// Three route families register only under a condition. The inventory has to
	// see them, or a route drops out of the check by being configured off in a
	// test rather than by anyone deciding it should not be documented.
	cfg.Provisioning.HTTPBootDir = "/var/lib/orchestrator/net_install"
	cfg.JWT.SecretKey = "routes-test-secret-key-not-used-to-sign-anything"
	return cfg
}

// --- stubs -----------------------------------------------------------------
//
// Registration only takes the METHOD VALUES off these; nothing is called. They
// embed their interface so the file does not have to track every method, and a
// nil embedded interface is exactly right: a call would panic loudly rather
// than quietly return a zero value.

type stubAuthService struct{ services.AuthService }

// RequireSettledAccount answers every account as settled, so the route
// checks here see what an ordinary account sees; the open-signup gate has
// its own tests in middleware.
func (stubAuthService) RequireSettledAccount(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// routesTestProbe only has to exist for /ready to be registered; it is never
// called, because nothing in this file serves a request.
type routesTestProbe struct{}

func (routesTestProbe) Probe(context.Context) (bool, any) { return true, nil }

type stubUserService struct{ services.UserService }
type stubAccountService struct{ services.AccountService }
type stubNodeService struct{ services.NodeService }
type stubImageService struct{ services.ImageService }
type stubFileService struct{ services.FileService }
type stubNodeCommandService struct{ services.NodeCommandService }

// The terminal routes register only when the communication service also
// implements TerminalService — the gRPC server does — so the stub claims both.
type stubNodeCommunicationService struct {
	service.NodeCommunicationService
	services.TerminalService
}

// GetStorageType is the one method called during registration: it decides
// whether the CDN routes exist at all. "local" is the default backend and is
// served through the CDN.
type stubStorageClient struct{ storage.StorageClient }

func (stubStorageClient) GetStorageType() string { return storage.TypeLocal }

type stubAccountDeletionRequestRepo struct {
	repository.AccountDeletionRequestRepository
}
