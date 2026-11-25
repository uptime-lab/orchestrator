package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"log/slog"
)

// captureVerifier records what the middleware asked, and answers as told.
type captureVerifier struct {
	err     error
	userID  uuid.UUID
	ticket  string
	session services.SessionProof
	calls   int
}

func (v *captureVerifier) EnsureRecentAuth(_ context.Context, userID uuid.UUID, ticket string, session services.SessionProof) error {
	v.calls++
	v.userID, v.ticket, v.session = userID, ticket, session
	return v.err
}

// driveStepUp sends one request through the step-up middleware with the context the
// JWT middleware would have left behind.
func driveStepUp(t *testing.T, verifier *captureVerifier, userID, familyID uuid.UUID, issuedAt time.Time, ticket string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	reached := false
	handler := RequireRecentAuth(verifier, slog.New(slog.DiscardHandler))(func(c echo.Context) error {
		reached = true
		return c.NoContent(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPut, "/", nil)
	if ticket != "" {
		req.Header.Set(ReauthTicketHeader, ticket)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", userID.String())
	c.Set("familyID", familyID.String())
	if !issuedAt.IsZero() {
		c.Set("tokenIssuedAt", issuedAt)
	}
	if err := handler(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if reached != (rec.Code == http.StatusNoContent) {
		t.Fatalf("handler reached=%v but status=%d", reached, rec.Code)
	}
	return rec
}

// The verifier must be told WHICH session is asking: the
// ticket is bound to a session family and honoured only while that family is
// live, so a middleware that dropped the proof would put the guard back on the
// user id alone.
func TestRequireRecentAuthHandsTheSessionProofToTheVerifier(t *testing.T) {
	userID, familyID := uuid.New(), uuid.New()
	issuedAt := time.Now().Truncate(time.Second)
	v := &captureVerifier{}

	rec := driveStepUp(t, v, userID, familyID, issuedAt, "ticket-1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if v.userID != userID || v.ticket != "ticket-1" {
		t.Fatalf("verifier asked about user %s ticket %q", v.userID, v.ticket)
	}
	if v.session.FamilyID != familyID || !v.session.IssuedAt.Equal(issuedAt) {
		t.Fatalf("verifier received session %+v, want family %s issued %s", v.session, familyID, issuedAt)
	}
}

func TestRequireRecentAuthEndsADeadSessionWith401(t *testing.T) {
	// A session the verifier says is gone — signed out, revoked everywhere,
	// or older than the last password change — is a 401, not a re-auth
	// prompt: the same dead session could not obtain a confirmation either.
	v := &captureVerifier{err: app_errors.ErrTokenInvalid}
	rec := driveStepUp(t, v, uuid.New(), uuid.New(), time.Now(), "ticket")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body.String())
	}
}

func TestRequireRecentAuthAsksForAProofWith403(t *testing.T) {
	v := &captureVerifier{err: app_errors.ErrReauthRequired}
	rec := driveStepUp(t, v, uuid.New(), uuid.New(), time.Now(), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ReauthCodeRequired) {
		t.Fatalf("body must carry the %q marker the SPA keys the prompt off: %s", ReauthCodeRequired, rec.Body.String())
	}
}

func TestRequireRecentAuthFailsClosedWhenTheSessionCannotBeRead(t *testing.T) {
	v := &captureVerifier{err: errors.New("pool timeout")}
	rec := driveStepUp(t, v, uuid.New(), uuid.New(), time.Now(), "ticket")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (refused, never waved through)", rec.Code)
	}
}
