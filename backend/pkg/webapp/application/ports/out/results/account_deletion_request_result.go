package results

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_deletion_request"
)

// AccountDeletionRequestResult is an open request to erase the account.
//
// It carries no status field, and that is not an omission: a request either
// exists or it does not. Adding `status: "pending"` would imply the API can
// report "completed", which it cannot — once the operator erases the account
// there is no account left to ask.
type AccountDeletionRequestResult struct {
	ID          uuid.UUID `json:"id"`
	AccountID   uuid.UUID `json:"account_id"`
	RequestedBy uuid.UUID `json:"requested_by"`
	RequestedAt int64     `json:"requested_at"`
	Reason      string    `json:"reason,omitempty"`
	// Notice states what filing this did and did not do. It travels in the
	// response because the one thing a customer must not conclude is that their
	// data is already gone: it is not, they can still export it, and until the
	// operator acts the account works exactly as before.
	Notice string `json:"notice"`
}

// DeletionRequestNoticeHosted is the notice where an operator console
// performs the erasure (the hosted install).
const DeletionRequestNoticeHosted = "Your account is NOT deleted yet. This request has been recorded and the instance " +
	"operator will erase the account and all its data. Nothing changes until then — you can still sign in, and " +
	"you can still export your data from GET /api/v1/account/export. Cancel with DELETE on this endpoint."

// DeletionRequestNoticeSelfHosted is the notice where nothing executes the
// request: a self-hosted install records it for its operator and has no
// built-in erasure, so promising one would be the false half of the hosted
// notice.
const DeletionRequestNoticeSelfHosted = "Your account is NOT deleted. This request has been recorded for the " +
	"operator of this install, and nothing is erased automatically. Nothing changes unless the operator acts on " +
	"it — you can still sign in, and you can still export your data from GET /api/v1/account/export. Cancel with " +
	"DELETE on this endpoint."

// NewAccountDeletionRequestResult builds the result. erasureExecutor says
// whether this install has something that performs the erasure (the hosted
// operator console); it chooses the notice, never the fields.
func NewAccountDeletionRequestResult(r *account_deletion_request.Request, erasureExecutor bool) AccountDeletionRequestResult {
	notice := DeletionRequestNoticeSelfHosted
	if erasureExecutor {
		notice = DeletionRequestNoticeHosted
	}
	return AccountDeletionRequestResult{
		ID:          r.ID,
		AccountID:   r.AccountID,
		RequestedBy: r.RequestedBy,
		RequestedAt: r.RequestedAt.UTC().Unix(),
		Reason:      r.Reason,
		Notice:      notice,
	}
}
