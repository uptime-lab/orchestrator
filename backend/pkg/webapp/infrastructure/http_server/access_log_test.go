package http_server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// The access log must never contain a query string. Several live bearer
// credentials travel as query params — the invite token, the reauth and passkey
// tickets, the OIDC code, and the ?exp=&sig= capability HMACs — so a log line
// carrying the query is a log line carrying a usable credential.
//
// Echo's default format uses ${uri}, which includes the query. This asserts we
// are not on that default, in the one way that actually matters: by driving a
// request and reading what got written.
func TestAccessLogNeverRecordsTheQueryString(t *testing.T) {
	secrets := []struct {
		name  string
		query string
	}{
		{"invite token", "token=inv_supersecretvalue"},
		{"reauth ticket", "ticket=rt_supersecretvalue"},
		{"oidc code", "code=oidc_supersecretvalue&state=xyz"},
		{"capability hmac", "exp=1799999999&sig=deadbeefcafe"},
	}

	for _, tc := range secrets {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg := accessLogConfig()
			cfg.Output = &out

			e := echo.New()
			e.Use(middleware.LoggerWithConfig(cfg))
			e.GET("/some/route", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

			req := httptest.NewRequest(http.MethodGet, "/some/route?"+tc.query, nil)
			e.ServeHTTP(httptest.NewRecorder(), req)

			logged := out.String()
			if logged == "" {
				t.Fatal("the logger wrote nothing — the test is not exercising it")
			}
			// The path must be there; the query must not.
			if !strings.Contains(logged, "/some/route") {
				t.Errorf("access log lost the path entirely: %s", logged)
			}
			if strings.Contains(logged, "?") || strings.Contains(logged, tc.query) {
				t.Errorf("access log contains the query string, which carries a credential:\n%s", logged)
			}
		})
	}
}

// Guards the rest of the line: dropping the query must not have cost any of the
// fields an operator actually needs during an incident.
func TestAccessLogKeepsTheFieldsAnIncidentNeeds(t *testing.T) {
	var out bytes.Buffer
	cfg := accessLogConfig()
	cfg.Output = &out

	e := echo.New()
	e.Use(middleware.RequestID())
	e.Use(middleware.LoggerWithConfig(cfg))
	e.GET("/some/route", func(c echo.Context) error { return c.NoContent(http.StatusTeapot) })

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/some/route", nil))

	logged := out.String()
	for _, field := range []string{
		`"time":`, `"id":`, `"remote_ip":`, `"host":`, `"method":"GET"`,
		`"path":"/some/route"`, `"status":418`, `"latency":`, `"bytes_out":`,
	} {
		if !strings.Contains(logged, field) {
			t.Errorf("access log is missing %s:\n%s", field, logged)
		}
	}
	// RequestID is installed ahead of the logger specifically so this is not
	// empty on every line.
	if strings.Contains(logged, `"id":""`) {
		t.Error(`access log "id" is empty — RequestID must run before the logger`)
	}
}

// A multi-tenant control plane where the same paths are walked by everyone: a
// line that says WHAT was requested and never WHO by cannot answer "customer X
// reports Y happened yesterday".
func TestAccessLogNamesTheTenant(t *testing.T) {
	var out bytes.Buffer
	cfg := accessLogConfig()
	cfg.Output = &out

	e := echo.New()
	e.Use(middleware.LoggerWithConfig(cfg))
	// Set deeper in the chain, exactly as the JWT middleware does — the logger
	// writes its line after next(c) returns, so it must still see them.
	e.GET("/api/v1/nodes", func(c echo.Context) error {
		c.Set("accountID", "acct-42")
		c.Set("userID", "user-7")
		return c.NoContent(http.StatusOK)
	})

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &record); err != nil {
		t.Fatalf("the access log line is not valid JSON: %v\n%s", err, out.String())
	}
	if got, _ := record["account_id"].(string); got != "acct-42" {
		t.Errorf("account_id = %q, want acct-42", got)
	}
	if got, _ := record["user_id"].(string); got != "user-7" {
		t.Errorf("user_id = %q, want user-7", got)
	}
}

// An unauthenticated request must produce the SAME shape with empty values, not a
// line missing two keys — otherwise every log query has to special-case it.
func TestAccessLogShapeIsStableWithoutATenant(t *testing.T) {
	var out bytes.Buffer
	cfg := accessLogConfig()
	cfg.Output = &out

	e := echo.New()
	e.Use(middleware.LoggerWithConfig(cfg))
	e.GET("/health", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &record); err != nil {
		t.Fatalf("the access log line is not valid JSON: %v\n%s", err, out.String())
	}
	for _, key := range []string{"account_id", "user_id"} {
		value, present := record[key]
		if !present {
			t.Errorf("%s is missing on an unauthenticated request; the line shape must not vary", key)
			continue
		}
		if value != "" {
			t.Errorf("%s = %v on an unauthenticated request, want an empty string", key, value)
		}
	}
}

// A value carrying a quote would break the JSON for every consumer of this log.
// These are UUIDs in practice; the guard is for the day one of them is not.
func TestAccessLogCannotBeBrokenByAStrayQuote(t *testing.T) {
	var out bytes.Buffer
	cfg := accessLogConfig()
	cfg.Output = &out

	e := echo.New()
	e.Use(middleware.LoggerWithConfig(cfg))
	e.GET("/api/v1/nodes", func(c echo.Context) error {
		c.Set("accountID", `evil","status":999,"x":"`)
		return c.NoContent(http.StatusOK)
	})

	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &record); err != nil {
		t.Fatalf("a quote in a context value broke the log line: %v\n%s", err, out.String())
	}
	if got, _ := record["account_id"].(string); got != "" {
		t.Errorf("account_id = %q; an unquotable value must be dropped, not emitted", got)
	}
	if status, _ := record["status"].(float64); status != 200 {
		t.Errorf("status = %v; a context value managed to overwrite a real field", status)
	}
}
