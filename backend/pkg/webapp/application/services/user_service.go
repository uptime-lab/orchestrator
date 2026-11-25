package services

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_beta_notice_ack"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_password_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/user_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/google/uuid"
)

// UserService defines the interface for user operations
type UserService interface {
	GetByID(ctx context.Context, userID uuid.UUID) (*user.User, error)
	ListByAccountID(ctx context.Context, query user_list.UserListQuery) ([]*user.User, error)
	// UpdateMe and UpdatePassword take the caller's session proof: both
	// change how the identity signs in, and both persist under the
	// session-bound row lock. A missing proof is
	// refused, never treated as any session.
	UpdateMe(ctx context.Context, cmd user_update.UserUpdateCommand, session SessionProof) (*user.User, error)
	UpdatePassword(ctx context.Context, cmd user_password_update.UserUpdatePasswordCommand, session SessionProof) (*user.User, error)
	AcknowledgeBetaNotice(ctx context.Context, cmd user_beta_notice_ack.UserBetaNoticeAckCommand) (*user.User, error)
}
