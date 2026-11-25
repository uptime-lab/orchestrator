// Package wsorigin holds the single origin policy every WebSocket upgrader on
// this server uses: the account fan-out socket and the node terminal.
//
// It exists because the two had drifted into different fail-OPEN shapes:
// the fan-out upgrader allowed any origin whenever CORS was
// "enabled" — which is always, since the server installs a wildcard CORS policy
// when the block is disabled — and the terminal allowed any origin when the
// configured list was empty or contained "*". Neither is exploitable while
// authorization is an explicit first-message JWT rather than an ambient cookie,
// but that is a property of today's auth, not of the check, and the check is
// what a future cookie migration would be relying on.
//
// The WebSocket policy is deliberately NOT the CORS boolean. CORS governs
// XHR/fetch, where the browser enforces the response header; a WebSocket
// handshake is exempt from the same-origin policy entirely, so CheckOrigin is
// the only thing between a foreign page and an authenticated socket.
package wsorigin

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

// CheckFunc is the shape gorilla/websocket's Upgrader.CheckOrigin expects.
type CheckFunc func(r *http.Request) bool

// New builds the origin check from config. Resolution order:
//
//  1. WEBSOCKET_ALLOWED_ORIGINS, when set — lets an operator pin the socket
//     origins without widening or narrowing the API's CORS policy.
//  2. otherwise the CORS allowed origins, which on a normal install already
//     name the app.
//  3. when that leaves nothing to match against: SAME-ORIGIN. An on-prem box
//     reached by IP or LAN hostname keeps working with no configuration — the
//     browser's Origin is that same host — which is what the old fail-open
//     branch was really trying to achieve, without also admitting every other
//     site on the internet.
//
// A "*" entry means allow-any only OUTSIDE production. In production it is
// ignored in favour of the same-origin rule and logged, matching the
// fail-closed polarity of the rest of Config validation: anything that is not
// explicitly a development environment is treated as production.
func New(cfg *config.Config, logger *slog.Logger) CheckFunc {
	if cfg == nil {
		return sameOrigin
	}

	configured := cfg.Server.WebSocket.AllowedOrigins
	source := "WEBSOCKET_ALLOWED_ORIGINS"
	if len(trimmed(configured)) == 0 {
		configured = cfg.Server.CORS.AllowedOrigins
		source = "CORS_ALLOWED_ORIGINS"
	}

	allowed := map[string]bool{}
	wildcard := false
	for _, o := range trimmed(configured) {
		if o == "*" {
			wildcard = true
			continue
		}
		allowed[normalize(o)] = true
	}

	if wildcard {
		if !cfg.IsProduction() {
			return func(*http.Request) bool { return true }
		}
		if logger != nil {
			logger.Warn("ignoring wildcard WebSocket origin in production; falling back to same-origin",
				"source", source)
		}
	}

	if len(allowed) == 0 {
		return sameOrigin
	}

	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header at all means no browser: the agent CLI, curl, a
			// health probe. Browsers always send one on a WS handshake, so this
			// cannot be used to bypass the check from a page.
			return true
		}
		if allowed[normalize(origin)] {
			return true
		}
		// An origin equal to the host we are being reached on is the same
		// document that served the app, whatever the configured list says.
		return originMatchesHost(origin, r)
	}
}

// sameOrigin accepts only a handshake whose Origin host is the host the request
// arrived on.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return originMatchesHost(origin, r)
}

func originMatchesHost(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	// Compare host:port and ignore the scheme: the request carries no scheme of
	// its own once it has been through a TLS-terminating proxy, and a downgrade
	// to http on the same host is not the threat this check addresses.
	return strings.EqualFold(u.Host, hostOf(r))
}

// hostOf prefers the forwarded host, since the shipped composes put a reverse
// proxy in front and r.Host would otherwise be the internal service name.
func hostOf(r *http.Request) string {
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); fwd != "" {
		// A comma-separated chain lists the ORIGINAL host first.
		if i := strings.IndexByte(fwd, ','); i >= 0 {
			fwd = strings.TrimSpace(fwd[:i])
		}
		return fwd
	}
	return r.Host
}

func normalize(origin string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(origin), "/"))
}

func trimmed(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
