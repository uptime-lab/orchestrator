package user_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// UserListQuery represents a query to list all users for an account
type UserListQuery struct {
	AuthContext commands.AuthContext
	// Optional pagination parameters can be added here in the future
	// Offset int
	// Limit  int
}
