package app_errors

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The refusal declares its own HTTP meaning so that no handler has to carry
// an arm for it, and still matches the sentinel the older arms branch on.
func TestRegistrationClosedErrorDeclaresItself(t *testing.T) {
	var err error = &RegistrationClosedError{Reason: ReasonNoInviteCode}
	var typed interface {
		error
		HTTPStatus() int
		ErrorCode() string
	}
	if !errors.As(err, &typed) {
		t.Fatal("RegistrationClosedError must be an operator-facing error")
	}
	if typed.HTTPStatus() != http.StatusForbidden {
		t.Errorf("status = %d, want 403: the request was fine and the caller cannot open it", typed.HTTPStatus())
	}
	if typed.ErrorCode() != RegistrationClosedErrorCode {
		t.Errorf("code = %q", typed.ErrorCode())
	}
	if !errors.Is(err, ErrRegistrationClosed) {
		t.Error("the typed error must still match the sentinel")
	}
	wrapped := fmt.Errorf("signup: %w", err)
	if !errors.Is(wrapped, ErrRegistrationClosed) {
		t.Error("wrapping must keep the sentinel reachable")
	}
	// The message is what the sign-up page shows; it must say what to do
	// next and must not say WHY (the reason is for the log).
	msg := err.Error()
	if !strings.HasPrefix(msg, "registration is currently closed") {
		t.Errorf("message must start with the documented sentence, got %q", msg)
	}
	if !strings.Contains(msg, "invite") {
		t.Errorf("message must tell the caller to ask for an invitation, got %q", msg)
	}
	for _, reason := range []RegistrationClosedReason{ReasonNoInviteCode, ReasonAccountCeiling, ReasonCountUnavailable} {
		if strings.Contains((&RegistrationClosedError{Reason: reason}).Error(), string(reason)) {
			t.Errorf("the caller-facing message leaks the reason %q", reason)
		}
	}
}
