package master_token_list

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// MasterTokenListQuery represents a query to list all master tokens for an account
type MasterTokenListQuery struct {
	AuthContext commands.AuthContext
}
