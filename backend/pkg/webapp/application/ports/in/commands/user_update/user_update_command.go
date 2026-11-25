package user_update

import (
	"strings"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// UserUpdateCommand represents a command to update a user.
//
// Name and Email are POINTERS so that an omitted field means "unchanged" — the
// convention NodeUpdateCommand and SecretUpdateCommand already follow. They
// used to be plain strings, which made this endpoint a full replace: a PUT body
// without "email" wrote the empty string over users.email. Both sign-in and
// password reset resolve by email, so that locked the user out of their own
// account with no self-service way back short of a database edit.
//
// The struct carried `validate:"required,email"` tags that were supposed to
// prevent exactly that. They never ran: no echo.Validator is installed anywhere
// in this process, so the tags were decorative — which is also why nobody
// noticed that their name bound (max=100) disagreed with the one on the
// register command (max=256). The fix is deliberately structural rather than
// one more guard: with a pointer there is no value to blank, so a future caller
// cannot reintroduce the bug by forgetting to validate.
type UserUpdateCommand struct {
	ID          uuid.UUID            `json:"id"`
	AuthContext commands.AuthContext `json:"-"`
	Name        *string              `json:"name,omitempty"`
	Email       *string              `json:"email,omitempty"`
}

// Validate checks the command's own constraints. It is called by the service
// rather than by an echo.Validator — none is installed — so every caller of
// UpdateMe gets the check, not only the HTTP handler.
func (c UserUpdateCommand) Validate() error {
	if c.Name != nil && strings.TrimSpace(*c.Name) == "" {
		return app_errors.ErrUserNameRequired
	}
	if c.Name != nil && len(*c.Name) > user.MaxNameLength {
		return app_errors.ErrUserNameTooLong
	}
	if c.Email != nil {
		// An explicitly supplied empty email is a blanking attempt, not a
		// no-op: "leave it alone" is expressed by omitting the field. Rejecting
		// it here is what keeps the old behaviour from arriving through a
		// client that sends "email": "" instead of omitting the key.
		if user.NormalizeEmail(*c.Email) == "" {
			return app_errors.ErrUserEmailRequired
		}
		if !user.IsValidEmail(*c.Email) {
			return app_errors.ErrUserEmailInvalid
		}
	}
	return nil
}
