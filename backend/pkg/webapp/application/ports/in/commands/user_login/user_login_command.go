package user_login

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// UserLoginCommand represents a user login request
type UserLoginCommand struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// Validate validates the login command
func (cmd *UserLoginCommand) Validate() error {
	if cmd.Email == "" {
		return app_errors.ErrUserEmailRequired
	}
	if cmd.Password == "" {
		return app_errors.ErrUserPasswordRequired
	}
	return nil
}
