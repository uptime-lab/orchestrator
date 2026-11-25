package external_identity

import (
	"time"

	"github.com/google/uuid"
)

// ExternalIdentity links a local user identity to an external IdP account
// (Google/Azure/Okta/… via OIDC). The stable key is (provider, subject) — the
// OIDC issuer plus the token's `sub`, which never changes even if the email
// does. Generalizes the old single clerk_id column.
type ExternalIdentity struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Provider  string // OIDC issuer URL (e.g. https://accounts.google.com)
	Subject   string // OIDC `sub` claim (stable per provider)
	Email     string // verified email at link time (informational)
	CreatedAt time.Time
}

// New creates an external identity link.
func New(userID uuid.UUID, provider, subject, email string) *ExternalIdentity {
	return &ExternalIdentity{
		ID:        uuid.New(),
		UserID:    userID,
		Provider:  provider,
		Subject:   subject,
		Email:     email,
		CreatedAt: time.Now(),
	}
}
