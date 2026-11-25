package user

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
)

// The sign-up handler carries no arm for a closed registration: the typed
// error declares its own 403 and the generic path answers it as itself. This
// pins both halves — the type satisfies the interface the generic path
// consults, and that path renders it as the documented refusal.
var _ results.OperatorFacingError = (*app_errors.RegistrationClosedError)(nil)

func TestAClosedRegistrationReachesTheCallerAsItsOwn403(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/api/v1/auth/signup", nil), rec)

	err := errors.Join(nil, &app_errors.RegistrationClosedError{Reason: app_errors.ReasonNoInviteCode})
	if err := results.SendInternalServerError(c, "registration failed", err); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; the refusal flattened into %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != app_errors.RegistrationClosedErrorCode {
		t.Errorf("code = %q, want %q", body.Code, app_errors.RegistrationClosedErrorCode)
	}
	if body.Message == "" || body.Message[:len("registration is currently closed")] != "registration is currently closed" {
		t.Errorf("message = %q, want the documented sentence (the sign-up page shows it verbatim)", body.Message)
	}
}
