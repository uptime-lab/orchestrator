package services

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/google/uuid"
)

// AccountService defines the interface for account operations
type AccountService interface {
	CreateAccount(ctx context.Context, accountName string) (*account.Account, error)
	GetAccount(ctx context.Context, id uuid.UUID) (*account.Account, error)
	UpdateAccount(ctx context.Context, cmd account_update.AccountUpdateCommand) (*account.Account, error)
	DeleteAccount(ctx context.Context, cmd account_delete.AccountDeleteCommand) error
}
