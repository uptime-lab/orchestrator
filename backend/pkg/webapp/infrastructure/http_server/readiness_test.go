package http_server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/health"
)

type stubProbe struct {
	ready bool
	body  any
}

func (s stubProbe) Probe(context.Context) (bool, any) { return s.ready, s.body }

func newTestServer() *Server {
	e := echo.New()
	e.HideBanner = true
	return &Server{echo: e, logger: slog.Default()}
}

func doGet(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The whole point: a monitor must be able to tell an unhealthy instance apart
// from a healthy one by status code alone. Before /ready existed, the only
// endpoint on offer answered 200 through a total database failure.
func TestReadyAnswers503WhenADependencyIsDown(t *testing.T) {
	s := newTestServer()
	s.RegisterReadiness(stubProbe{ready: false, body: map[string]string{"status": "fail"}})

	rec := doGet(t, s, "/ready")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 — a monitor reads the code, not the body", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "fail") {
		t.Fatalf("body %q does not say which way the verdict went", rec.Body.String())
	}
}

func TestReadyAnswers200WhenEverythingAnswers(t *testing.T) {
	s := newTestServer()
	s.RegisterReadiness(stubProbe{ready: true, body: map[string]string{"status": "ok"}})

	if rec := doGet(t, s, "/ready"); rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

// The real checker through the real route: what a monitor actually receives.
// The stub above proves the status code; this proves the body it is paired
// with is the report, and that *health.Checker satisfies the port.
func TestReadyServesTheRealReport(t *testing.T) {
	checker := health.New(func(context.Context) error {
		return errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")
	}, "", slog.New(slog.NewTextHandler(io.Discard, nil)))

	s := newTestServer()
	s.RegisterReadiness(checker)

	rec := doGet(t, s, "/ready")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", rec.Code)
	}
	var body struct {
		Status string `json:"status"`
		Checks map[string]struct {
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the report: %v (%s)", err, rec.Body.String())
	}
	if body.Status != "fail" || body.Checks["database"].Status != "fail" {
		t.Fatalf("report does not name the failing dependency: %s", rec.Body.String())
	}
	// The public endpoint must not repeat the driver error, which carries the
	// database host and user.
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("the response leaks the database address: %s", rec.Body.String())
	}
}

// Liveness must stay static. If /health started failing with the database, the
// container healthcheck would restart the backend in a loop during an outage a
// restart cannot fix — and take the logs with it.
func TestHealthStaysStaticEvenWhenReadinessFails(t *testing.T) {
	s := newTestServer()
	s.echo.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	s.RegisterReadiness(stubProbe{ready: false, body: map[string]string{"status": "fail"}})

	if rec := doGet(t, s, "/health"); rec.Code != http.StatusOK {
		t.Fatalf("liveness answered %d while readiness was failing; it must stay 200", rec.Code)
	}
}
