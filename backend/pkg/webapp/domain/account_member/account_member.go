package account_member

import (
	"time"

	"github.com/google/uuid"
)

// MemberStatus is the lifecycle state of a membership. Disabled members keep
// their bindings and authorship but cannot authenticate into the account
// ("disable, don't delete").
type MemberStatus string

const (
	StatusActive   MemberStatus = "active"
	StatusDisabled MemberStatus = "disabled"
)

// IsValid reports whether the status is one of the defined statuses.
func (s MemberStatus) IsValid() bool {
	return s == StatusActive || s == StatusDisabled
}

// AccountMember links a global user identity to an account. Roles are carried
// separately by role bindings; a membership with no bindings is a visible
// member with no access.
type AccountMember struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	UserID    uuid.UUID
	Status    MemberStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewAccountMember creates an active membership.
func NewAccountMember(accountID, userID uuid.UUID) *AccountMember {
	now := time.Now()
	return &AccountMember{
		ID:        uuid.New(),
		AccountID: accountID,
		UserID:    userID,
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
}
