package http_server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func errorHandlerEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = envelopeErrorHandler(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return e
}

type envelope struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	Error     string `json:"error"`
	Code      string `json:"code"`
	Timestamp string `json:"timestamp"`
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not the envelope: %v — %s", err, rec.Body.String())
	}
	// Echo's bare {"message": …} unmarshals into the struct just fine; the
	// timestamp is what tells the envelope from it.
	if env.Timestamp == "" {
		t.Fatalf("body is Echo's bare shape, not the envelope: %s", rec.Body.String())
	}
	return env
}

// A returned *echo.HTTPError — the shape middleware.Authorize and the
// per-handler authorize helpers hand back — renders as the results envelope
// with its status and message intact, not as Echo's bare {"message": …}.
func TestAReturnedHTTPErrorRendersAsTheEnvelope(t *testing.T) {
	e := errorHandlerEcho()
	e.GET("/x", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusForbidden, "forbidden: insufficient permissions")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	env := decodeEnvelope(t, rec)
	if env.Success {
		t.Errorf("success = true on a refusal")
	}
	if env.Message != "forbidden: insufficient permissions" {
		t.Errorf("message = %q, the text the middleware chose was dropped", env.Message)
	}
	if env.Timestamp == "" {
		t.Errorf("the envelope carries no timestamp: %s", rec.Body.String())
	}
}

// A plain error (a handler returning err, or a panic through Recover) is a
// generic 500 that echoes NOTHING of the error — it can carry SQL text.
func TestAPlainErrorIsAGeneric500ThatHidesItsText(t *testing.T) {
	e := errorHandlerEcho()
	e.GET("/x", func(c echo.Context) error {
		return errors.New(`pq: relation "nodes" does not exist`)
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "relation") {
		t.Fatalf("the raw error reached the client: %s", rec.Body.String())
	}
	env := decodeEnvelope(t, rec)
	if env.Message != http.StatusText(http.StatusInternalServerError) {
		t.Errorf("message = %q", env.Message)
	}
}

// Echo's own 404 for an unknown route goes through the same handler, so a
// client never meets two error shapes on one server.
func TestAnUnknownRouteRendersAsTheEnvelope(t *testing.T) {
	e := errorHandlerEcho()

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	decodeEnvelope(t, rec)
}

// HEAD never carries a body, as with Echo's default handler.
func TestHeadCarriesNoErrorBody(t *testing.T) {
	e := errorHandlerEcho()
	e.HEAD("/x", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusNotFound, "node not found")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/x", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD answered with a body: %s", rec.Body.String())
	}
}

// A response a handler already wrote before returning an error is left
// alone — writing again would only produce a "response already committed"
// log line and a corrupted body.
func TestACommittedResponseIsNotRewritten(t *testing.T) {
	e := errorHandlerEcho()
	e.GET("/x", func(c echo.Context) error {
		if err := c.String(http.StatusOK, "partial"); err != nil {
			return err
		}
		return echo.NewHTTPError(http.StatusBadRequest, "too late")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Errorf("committed response was rewritten: %d %s", rec.Code, rec.Body.String())
	}
}
