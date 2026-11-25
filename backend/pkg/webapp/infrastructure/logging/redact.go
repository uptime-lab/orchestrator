package logging

import (
	"log/slog"
	"net/http"
)

// RedactedHeaderValue replaces a credential header's value in a log line. A
// marker rather than an omission, so the line still says the header was there.
const RedactedHeaderValue = "[redacted]"

// credentialHeaders are the request headers that carry a reusable credential:
// the bearer token, the OIDC state/nonce/link/invite cookies, and the step-up
// ticket (middleware.ReauthTicketHeader). Canonical form, as http.Header stores
// them.
var credentialHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"Set-Cookie":          true,
	"X-Reauth-Ticket":     true,
}

// RedactHeaderAttr is a slog.HandlerOptions.ReplaceAttr that redacts credential
// headers out of any attribute whose value is a header map, whatever key the
// caller gave it.
//
// It is a BACKSTOP, not the fix: the call sites that log headers should say
// `has_authorization` and the like, never the map. It exists for the call site
// nobody reviewed — a CORS rejection line that logs the whole request header map
// means a browser extension or a misconfigured client sending its
// bearer from an unexpected Origin put a live credential into stdout and every
// log backup. Installed once, on the base handler in main.go, it covers every
// logger derived from it.
//
// Works on a copy: the request's own header map is still in use by the handler
// chain.
func RedactHeaderAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindAny {
		return a
	}
	switch h := a.Value.Any().(type) {
	case http.Header:
		return slog.Any(a.Key, redactHeaders(h))
	case map[string][]string:
		return slog.Any(a.Key, redactHeaders(h))
	}
	return a
}

func redactHeaders(h map[string][]string) http.Header {
	out := make(http.Header, len(h))
	for name, values := range h {
		if credentialHeaders[http.CanonicalHeaderKey(name)] {
			out[name] = []string{RedactedHeaderValue}
			continue
		}
		out[name] = append([]string(nil), values...)
	}
	return out
}
