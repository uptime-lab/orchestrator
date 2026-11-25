package user_login_refresh

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// UserLoginRefreshCommand represents a token refresh request
type UserLoginRefreshCommand struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Validate validates the refresh command
func (cmd *UserLoginRefreshCommand) Validate() error {
	if cmd.RefreshToken == "" {
		return app_errors.ErrTokenInvalid
	}
	return nil
}
