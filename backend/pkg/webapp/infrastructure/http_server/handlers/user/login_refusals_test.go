package user

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// refusingAuth answers the two credential calls with a fixed error and counts
// what the burst detector was told.
type refusingAuth struct {
	services.AuthService
	err   error
	noted int
}

func (a *refusingAuth) Login(string, string, string) (*services.LoginResult, error) {
	return nil, a.err
}

func (a *refusingAuth) CompleteTwoFactor(context.Context, string, string) (*services.LoginResult, error) {
	return nil, a.err
}

func (a *refusingAuth) NoteFailedLogin(context.Context, string) { a.noted++ }

func call(t *testing.T, fn echo.HandlerFunc, path, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	if err := fn(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out.Code
}

// A second-factor lockout is temporary and the user's to wait out; it used to
// reach the caller as a 500 "login failed", even for the right code.
func TestTwoFactorLockoutIsA429WithAStableCode(t *testing.T) {
	auth := &refusingAuth{err: app_errors.ErrTwoFactorTooManyAttempts}
	h := NewAuthHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), auth, nil)

	rec, code := call(t, h.CompleteTwoFactor, "/auth/2fa", `{"two_factor_token":"t","code":"123456"}`)

	if rec.Code != http.StatusTooManyRequests || code != app_errors.TwoFactorLockedErrorCode {
		t.Fatalf("status %d code %q, want 429 %q (body %s)", rec.Code, code, app_errors.TwoFactorLockedErrorCode, rec.Body.String())
	}
}

// The per-address password brake answers 429 with its code and a Retry-After
// the client can honour, and is not counted as a failed credential.
func TestLoginThrottleIsA429WithRetryAfter(t *testing.T) {
	auth := &refusingAuth{err: &app_errors.LoginThrottledError{RetryAfter: 1500 * time.Millisecond}}
	h := NewAuthHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), auth, nil)

	rec, code := call(t, h.Login, "/auth/login", `{"email":"owner@example.com","password":"whatever-pw"}`)

	if rec.Code != http.StatusTooManyRequests || code != app_errors.LoginThrottledErrorCode {
		t.Fatalf("status %d code %q, want 429 %q (body %s)", rec.Code, code, app_errors.LoginThrottledErrorCode, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2 (rounded up)", got)
	}
	if auth.noted != 0 {
		t.Fatal("a throttled attempt was counted as a failed credential")
	}
}
