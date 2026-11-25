package http_server

import (
	"bytes"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// accessLogConfig is Echo's DefaultLoggerConfig with two deliberate changes:
// it records ${path}, NOT ${uri}, and it names the tenant.
//
// ${uri} is path PLUS query string, and live bearer credentials travel as query
// params on this API — the invite token (7-day TTL; "the token IS the
// credential" per its own doc comment), the reauth and passkey tickets, the
// OIDC code and state, and the ?exp=&sig= capability HMACs on /cdn/* and
// PUT /capture/:id. Anyone able to read the container log could accept an
// invitation as someone else or overwrite a pending capture.
//
// The key is "path" rather than "uri" so the field name matches what it holds.
//
// account_id / user_id come from CustomTagFunc. Without them a line said what was
// requested and never WHO by, so "customer X reports Y happened yesterday" could
// not be narrowed to that customer's requests at all — and this is a
// multi-tenant control plane where the same path is walked by everyone. The
// email is deliberately absent: it is personal data, and the ids are what a
// support question is answered with.
func accessLogConfig() middleware.LoggerConfig {
	return middleware.LoggerConfig{
		Format: `{"time":"${time_rfc3339_nano}","id":"${id}",${custom}"remote_ip":"${remote_ip}",` +
			`"host":"${host}","method":"${method}","path":"${path}","user_agent":"${user_agent}",` +
			`"status":${status},"error":"${error}","latency":${latency},"latency_human":"${latency_human}"` +
			`,"bytes_in":${bytes_in},"bytes_out":${bytes_out}}` + "\n",
		CustomTimeFormat: "2006-01-02 15:04:05.00000",
		CustomTagFunc:    writeTenantTags,
	}
}

// writeTenantTags emits the tenant fields, ALWAYS as valid JSON: empty strings on
// an unauthenticated request rather than omitted keys, so every line has the same
// shape and a log query does not have to special-case the ones that do not.
//
// The values are read from the Echo context, which the JWT middleware populates
// deeper in the chain — the logger middleware writes its line after next(c)
// returns, so they are present by then. Non-string or missing values degrade to
// empty rather than panicking: a log line must never be the thing that fails.
func writeTenantTags(c echo.Context, buf *bytes.Buffer) (int, error) {
	return buf.WriteString(
		`"account_id":"` + contextString(c, "accountID") +
			`","user_id":"` + contextString(c, "userID") + `",`,
	)
}

func contextString(c echo.Context, key string) string {
	value, ok := c.Get(key).(string)
	if !ok {
		return ""
	}
	// A stray quote would break the JSON for every log consumer. These are UUIDs
	// in practice; the guard is for the day one of them is not.
	if bytes.ContainsAny([]byte(value), `"\`) {
		return ""
	}
	return value
}
