package wsorigin

import (
	"net/http"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

func req(origin, host string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://"+host+"/ws", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func cfgWith(env string, cors, ws []string) *config.Config {
	c := &config.Config{}
	c.Server.Environment = env
	c.Server.CORS.AllowedOrigins = cors
	c.Server.WebSocket.AllowedOrigins = ws
	return c
}

func TestPinnedOriginsAreMatchedExactly(t *testing.T) {
	check := New(cfgWith("production", []string{"https://app.orchestrator.sh"}, nil), nil)

	if !check(req("https://app.orchestrator.sh", "app.orchestrator.sh")) {
		t.Error("the configured app origin must be allowed")
	}
	if !check(req("https://APP.Orchestrator.sh/", "app.orchestrator.sh")) {
		t.Error("case and a trailing slash must not change the verdict")
	}
	if check(req("https://evil.example", "app.orchestrator.sh")) {
		t.Error("a foreign origin must be refused")
	}
}

// The reason the old code failed open: an on-prem box reached by IP has no
// configured origin. Same-origin keeps it working without admitting the world.
func TestUnconfiguredInstallFallsBackToSameOriginNotAllowAll(t *testing.T) {
	check := New(cfgWith("production", nil, nil), nil)

	if !check(req("http://192.168.1.10:8080", "192.168.1.10:8080")) {
		t.Error("a browser on the same host must still be able to connect")
	}
	if check(req("http://evil.example", "192.168.1.10:8080")) {
		t.Error("an unconfigured install must not accept every origin")
	}
}

// APP_ENV polarity: anything that is not explicitly a development environment
// is production, and there a wildcard must not disable the check.
func TestWildcardIsHonouredOnlyOutsideProduction(t *testing.T) {
	dev := New(cfgWith("development", []string{"*"}, nil), nil)
	if !dev(req("http://localhost:5174", "localhost:8080")) {
		t.Error("the Vite dev server talks to a different port and must keep working")
	}

	prod := New(cfgWith("production", []string{"*"}, nil), nil)
	if prod(req("https://evil.example", "app.orchestrator.sh")) {
		t.Error("a wildcard must not open the socket in production")
	}
	if !prod(req("https://app.orchestrator.sh", "app.orchestrator.sh")) {
		t.Error("production must still accept the app's own origin")
	}

	// Unset environment counts as production (Config.IsProduction).
	unset := New(cfgWith("", []string{"*"}, nil), nil)
	if unset(req("https://evil.example", "app.orchestrator.sh")) {
		t.Error("an install that never states its environment must be treated as production")
	}
}

func TestWebSocketOriginsOverrideCORS(t *testing.T) {
	check := New(cfgWith("production",
		[]string{"https://cors.example"},
		[]string{"https://ws.example"}), nil)

	if !check(req("https://ws.example", "app.example")) {
		t.Error("the WebSocket list must be used when set")
	}
	if check(req("https://cors.example", "app.example")) {
		t.Error("setting the WebSocket list must not also inherit the CORS origins")
	}
}

// Behind a reverse proxy r.Host can be the internal service name while the
// browser's Origin is the public host, so the forwarded host is compared.
func TestForwardedHostIsUsedForTheSameOriginComparison(t *testing.T) {
	check := New(cfgWith("production", nil, nil), nil)

	r := req("https://app.orchestrator.sh", "orchestrator-backend:8080")
	r.Header.Set("X-Forwarded-Host", "app.orchestrator.sh")
	if !check(r) {
		t.Error("the forwarded host must be what the origin is compared against")
	}

	chained := req("https://app.orchestrator.sh", "orchestrator-backend:8080")
	chained.Header.Set("X-Forwarded-Host", "app.orchestrator.sh, inner.proxy")
	if !check(chained) {
		t.Error("a proxy chain lists the original host first")
	}
}

func TestMissingOriginIsAllowed(t *testing.T) {
	// Non-browser clients (curl, probes) send no Origin. A browser always does
	// on a WS handshake, so this cannot be used to bypass the check from a page.
	for name, check := range map[string]CheckFunc{
		"pinned":      New(cfgWith("production", []string{"https://app.example"}, nil), nil),
		"same-origin": New(cfgWith("production", nil, nil), nil),
		"nil config":  New(nil, nil),
	} {
		if !check(req("", "app.example")) {
			t.Errorf("%s: a request with no Origin must be allowed", name)
		}
	}
}

func TestMalformedOriginIsRefused(t *testing.T) {
	check := New(cfgWith("production", nil, nil), nil)
	if check(req("://not a url", "app.example")) {
		t.Error("an unparseable origin must not pass the same-origin check")
	}
}
