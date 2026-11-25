package user_register

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// RegisterCommand represents a user registration request
type RegisterCommand struct {
	Email      string `json:"email" binding:"required,email"`
	Password   string `json:"password" binding:"required,min=8"`
	Name       string `json:"name,omitempty" binding:"max=256"`
	InviteCode string `json:"invite_code,omitempty"`
	// TermsAccepted is the sign-up form's statement that the person agreed to
	// the operator's terms of use. Optional: an install without terms sends
	// nothing. When true, the acceptance is stored on the user and recorded in
	// the audit trail.
	TermsAccepted bool `json:"terms_accepted,omitempty"`
}

// Validate validates the register command
func (cmd *RegisterCommand) Validate() error {
	if cmd.Email == "" {
		return app_errors.ErrUserEmailRequired
	}
	if cmd.Password == "" {
		return app_errors.ErrUserPasswordRequired
	}
	if len(cmd.Password) < user.MinPasswordLength {
		return app_errors.ErrUserPasswordTooShort
	}
	if len(cmd.Password) > user.MaxPasswordLength {
		return app_errors.ErrUserPasswordTooLong
	}
	// Name is optional, no validation needed
	return nil
}
