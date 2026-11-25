package user_register

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// A password that is present but short used to return ErrUserPasswordRequired,
// so the sign-up form rendered "password is required" next to a filled-in
// password field. The two conditions must stay distinguishable.
func TestValidateSeparatesMissingPasswordFromShortPassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     error
	}{
		{"missing", "", app_errors.ErrUserPasswordRequired},
		{"one below the floor", strings.Repeat("a", user.MinPasswordLength-1), app_errors.ErrUserPasswordTooShort},
		{"exactly the floor", strings.Repeat("a", user.MinPasswordLength), nil},
		{"above the floor", strings.Repeat("a", user.MinPasswordLength+40), nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &RegisterCommand{Email: "someone@example.com", Password: tc.password}
			err := cmd.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("password %q: got %v, want %v", tc.password, err, tc.want)
			}
		})
	}
}

func TestValidateStillRequiresAnEmail(t *testing.T) {
	cmd := &RegisterCommand{Password: strings.Repeat("a", user.MinPasswordLength)}
	if err := cmd.Validate(); !errors.Is(err, app_errors.ErrUserEmailRequired) {
		t.Fatalf("got %v, want ErrUserEmailRequired", err)
	}
}

// app_errors is deliberately a leaf package, so ErrUserPasswordTooShort states
// the floor as a literal rather than interpolating user.MinPasswordLength. That
// duplication is only safe while something checks the two agree — the sign-up
// handler passes err.Error() straight to the browser, so a drifted message is a
// wrong instruction to a real user.
func TestPasswordTooLongMessageMatchesTheCeiling(t *testing.T) {
	msg := app_errors.ErrUserPasswordTooLong.Error()
	ceiling := strconv.Itoa(user.MaxPasswordLength)
	if !strings.Contains(msg, ceiling) {
		t.Fatalf("ErrUserPasswordTooLong says %q but user.MaxPasswordLength is %s — "+
			"update the message in app_errors when changing the ceiling", msg, ceiling)
	}
}

// A password past the ceiling is refused by validation, before anything is
// looked up or hashed: bcrypt cannot hash it, and that failure used to reach
// the caller as a 500.
func TestRegisterCommandRefusesAPasswordBcryptCannotHash(t *testing.T) {
	cmd := RegisterCommand{Email: "a@example.com", Password: strings.Repeat("A", user.MaxPasswordLength+1)}
	if err := cmd.Validate(); !errors.Is(err, app_errors.ErrUserPasswordTooLong) {
		t.Fatalf("Validate() = %v, want ErrUserPasswordTooLong", err)
	}
	// Bytes, not characters: 42 Cyrillic letters are 84 bytes.
	cmd.Password = strings.Repeat("Ж", 42)
	if err := cmd.Validate(); !errors.Is(err, app_errors.ErrUserPasswordTooLong) {
		t.Fatalf("Validate() on 84 bytes of Cyrillic = %v, want ErrUserPasswordTooLong", err)
	}
	cmd.Password = strings.Repeat("A", user.MaxPasswordLength)
	if err := cmd.Validate(); err != nil {
		t.Fatalf("a password of exactly the ceiling must pass, got %v", err)
	}
}

func TestPasswordTooShortMessageMatchesTheFloor(t *testing.T) {
	msg := app_errors.ErrUserPasswordTooShort.Error()
	floor := strconv.Itoa(user.MinPasswordLength)
	if !strings.Contains(msg, floor) {
		t.Fatalf("ErrUserPasswordTooShort says %q but user.MinPasswordLength is %s — "+
			"update the message in app_errors when changing the floor", msg, floor)
	}
}
