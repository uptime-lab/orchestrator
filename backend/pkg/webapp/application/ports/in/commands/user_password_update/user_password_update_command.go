package user_password_update

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// UserUpdatePasswordCommand represents a command to update a user's password
type UserUpdatePasswordCommand struct {
	ID          uuid.UUID            `json:"id"`
	AuthContext commands.AuthContext `json:"-"`
	Password    string               `json:"password" validate:"required,min=8"`
}
