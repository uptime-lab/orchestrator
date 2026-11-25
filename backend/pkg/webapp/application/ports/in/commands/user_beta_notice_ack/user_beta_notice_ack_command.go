package user_beta_notice_ack

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// UserBetaNoticeAckCommand records that the caller accepted the operator's
// notice or terms of use. It carries no payload on purpose: the accepted
// revision is the server's BetaNoticeVersion, never a number the client picked.
type UserBetaNoticeAckCommand struct {
	ID          uuid.UUID            `json:"id"`
	AuthContext commands.AuthContext `json:"-"`
}
