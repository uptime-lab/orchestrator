// Package credential_reset carries the commands of the administrative
// credential reset — the server binary's --reset-password and --reset-totp,
// run by the operator of a self-hosted install from a shell on the host.
package credential_reset

import (
	"errors"
	"fmt"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// ErrEmailRequired refuses a command that names nobody.
var ErrEmailRequired = errors.New("an email address is required")

// ResetPasswordCommand replaces a user's password. Password is the
// operator-chosen value read from standard input; empty asks the service to
// generate one. It is never taken from the command line, where every user
// of the host can read it in the process list.
type ResetPasswordCommand struct {
	Email    string
	Password string
}

// Validate refuses an absent address and a chosen password under the floor
// every other password path enforces.
func (c ResetPasswordCommand) Validate() error {
	if err := validateEmail(c.Email); err != nil {
		return err
	}
	if c.Password != "" && len(c.Password) < user.MinPasswordLength {
		return fmt.Errorf("the password must be at least %d characters", user.MinPasswordLength)
	}
	if len(c.Password) > user.MaxPasswordLength {
		return fmt.Errorf("the password must be at most %d bytes", user.MaxPasswordLength)
	}
	return nil
}

// ResetTOTPCommand removes a user's authenticator-app (TOTP) factor and its
// recovery codes. Passkeys are not touched.
type ResetTOTPCommand struct {
	Email string
}

// Validate refuses an absent address.
func (c ResetTOTPCommand) Validate() error {
	return validateEmail(c.Email)
}

func validateEmail(email string) error {
	normalized := user.NormalizeEmail(email)
	if normalized == "" {
		return ErrEmailRequired
	}
	if !strings.Contains(normalized, "@") {
		return fmt.Errorf("%q is not an email address", normalized)
	}
	return nil
}
