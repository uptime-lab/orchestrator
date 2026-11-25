package results

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// UserResult represents a user in API responses. Users are global
// identities; their account association is carried by memberships.
type UserResult struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Email string    `json:"email"`
	// EmailVerified reports proven mailbox ownership. The UI prompts the user
	// to confirm while this is false; the timestamp itself is not exposed.
	EmailVerified bool `json:"email_verified"`
	// BetaNoticeAcknowledged reports a stored acceptance of the operator's
	// notice or terms of use at the revision in force. Resolved server-side
	// against domain/user's version constant, so the client never decides which
	// revision it has seen. The field name is kept for clients that read it.
	BetaNoticeAcknowledged bool      `json:"beta_notice_acknowledged"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// NewUserResult creates a new UserResult from a domain user
func NewUserResult(user *user.User) *UserResult {
	return &UserResult{
		ID:                     user.ID,
		Name:                   user.Name,
		Email:                  user.Email,
		EmailVerified:          user.EmailVerifiedAt != nil,
		BetaNoticeAcknowledged: user.HasAcknowledgedBetaNotice(),
		CreatedAt:              user.CreatedAt,
		UpdatedAt:              user.UpdatedAt,
	}
}

// UserListResult represents a list of users in API responses
type UserListResult struct {
	Users []*UserResult `json:"users"`
	Count int           `json:"count"`
}

// NewUserListResult creates a new UserListResult from a slice of domain users
func NewUserListResult(users []*user.User) *UserListResult {
	userResults := make([]*UserResult, len(users))
	for i, u := range users {
		userResults[i] = NewUserResult(u)
	}

	return &UserListResult{
		Users: userResults,
		Count: len(users),
	}
}
