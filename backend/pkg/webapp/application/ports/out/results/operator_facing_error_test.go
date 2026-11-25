package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

type fakeFacingError struct {
	status int
	code   string
}

func (e *fakeFacingError) Error() string {
	return "this node runs the watcher agent; netboot into recovery"
}
func (e *fakeFacingError) HTTPStatus() int   { return e.status }
func (e *fakeFacingError) ErrorCode() string { return e.code }

func sendInternal(t *testing.T, err error) (*httptest.ResponseRecorder, APIResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/x", nil), rec)
	if sendErr := SendInternalServerError(c, "failed to provision EEPROM", err); sendErr != nil {
		t.Fatalf("send returned an error: %v", sendErr)
	}
	var body APIResponse
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("response is not an APIResponse: %v (%s)", decodeErr, rec.Body.String())
	}
	return rec, body
}

// The defect this closes, twice over: a typed refusal reaching a handler with no
// arm for it was answered "failed to provision EEPROM" 500 — the platform
// blaming itself for a decision it made deliberately, with the remedy already
// written in the error and thrown away.
func TestAnOperatorFacingErrorIsAnsweredAsItself(t *testing.T) {
	rec, body := sendInternal(t, &fakeFacingError{status: http.StatusConflict, code: "watcher_node"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if body.Code != "watcher_node" {
		t.Errorf("code = %q, want the error's own code", body.Code)
	}
	if body.Message != "this node runs the watcher agent; netboot into recovery" {
		t.Errorf("message = %q, want the error's own text (the remedy)", body.Message)
	}
}

// The mapping has to survive the wrapping every service layer does on the way
// up, or it only works for errors returned bare — which is not how any of them
// reach a handler.
func TestAWrappedOperatorFacingErrorIsStillAnsweredAsItself(t *testing.T) {
	wrapped := fmt.Errorf("dispatch: %w", &fakeFacingError{status: http.StatusConflict, code: "device_busy"})
	rec, body := sendInternal(t, wrapped)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if body.Code != "device_busy" {
		t.Errorf("code = %q, want device_busy", body.Code)
	}
}

// The reason the mapping is OPT-IN. An arbitrary error can carry SQL text,
// table and column names or filesystem paths, and this helper has always
// refused to echo it. Only a type that declares itself operator-facing is
// shown, and everything else keeps the old behaviour exactly.
func TestAnOrdinaryErrorStillAnswers500AndLeaksNothing(t *testing.T) {
	rec, body := sendInternal(t, errors.New(`pq: column "unique_token" of relation "nodes"`))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body.Message != "failed to provision EEPROM" {
		t.Errorf("message = %q, want the caller's generic message", body.Message)
	}
	if body.Error != "" {
		t.Errorf("the wrapped error leaked to the client: %q", body.Error)
	}
	if body.Code != "" {
		t.Errorf("code = %q, want empty for an untyped failure", body.Code)
	}
}

// A nil error keeps meaning "500, generic message" — several callers pass nil
// deliberately after logging themselves.
func TestANilErrorStillAnswers500(t *testing.T) {
	rec, body := sendInternal(t, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body.Message != "failed to provision EEPROM" {
		t.Errorf("message = %q, want the caller's generic message", body.Message)
	}
}

// A typed-nil pointer inside a non-nil error interface. `errors.As` matches it
// and `facing != nil` is true, so an unguarded Error() call panics — under
// middleware.Recover that becomes a 500 with a stack trace, i.e. exactly the
// failure this path removes, reintroduced by the fix for it. Reachable the day
// any helper returning a concrete *SomeError gains a "return nil" branch.
func TestATypedNilDoesNotPanic(t *testing.T) {
	var nilTyped *fakeFacingError
	rec, body := sendInternal(t, error(nilTyped))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body.Message != "failed to provision EEPROM" {
		t.Errorf("message = %q, want the caller's generic message", body.Message)
	}
}

// A status outside 4xx/5xx is a bug in the error type. 0 renders as 200 on a
// recorder and panics on a real ResponseWriter; a 2xx would report a refusal as
// a success. Neither is passed through.
func TestAnOutOfRangeDeclaredStatusFallsBackTo500(t *testing.T) {
	for _, status := range []int{0, 200, 302, 600} {
		rec, _ := sendInternal(t, &fakeFacingError{status: status, code: "bogus"})
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("declared status %d produced %d, want a 500 fallback", status, rec.Code)
		}
	}
}
