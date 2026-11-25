package http_server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// A rejected Origin used to be logged with the whole request header map, so a
// browser extension, a diagnostic tool or a misconfigured deployment that sends
// its credentials from an unexpected Origin put the Authorization bearer, the
// OIDC cookies and the step-up ticket into stdout, the log aggregator and every
// log backup. The line keeps saying WHETHER a credential rode
// along — that is what makes a rejected request worth looking at — and never
// what it was. Canary values, read back from the sink, are the proof.
func TestCORSRejectionLogNeverRecordsCredentialHeaders(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))

	e := echo.New()
	e.Use(corsWithLogging(middleware.CORSConfig{AllowOrigins: []string{"https://app.example"}}, logger))
	e.GET("/api/v1/nodes", func(c echo.Context) error { return c.NoContent(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer CANARY-A")
	req.Header.Set("Cookie", "oidc_state=CANARY-B")
	req.Header.Set("X-Reauth-Ticket", "CANARY-C")
	e.ServeHTTP(httptest.NewRecorder(), req)

	logged := out.String()
	if !strings.Contains(logged, "CORS request rejected") {
		t.Fatalf("the rejection was not logged at all — the test is not exercising the branch:\n%s", logged)
	}
	if !strings.Contains(logged, `"origin":"https://evil.example"`) {
		t.Errorf("the rejected origin must stay in the line:\n%s", logged)
	}
	for _, flag := range []string{`"has_authorization":true`, `"has_cookie":true`, `"has_reauth_ticket":true`} {
		if !strings.Contains(logged, flag) {
			t.Errorf("the line must say a credential rode along (%s):\n%s", flag, logged)
		}
	}
	for _, canary := range []string{"CANARY-A", "CANARY-B", "CANARY-C"} {
		if strings.Contains(logged, canary) {
			t.Errorf("a credential reached the log sink (%s):\n%s", canary, logged)
		}
	}
}
