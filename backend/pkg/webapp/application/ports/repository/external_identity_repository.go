package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
)

// ExternalIdentityRepository persists OIDC identity links.
//
// Revocation is a SOFT delete, so every read here is defined in terms of LIVE
// links and says so. That is not pedantry: a revoked row that leaks into a
// lookup signs in, and one that leaks into a list counts as a credential the
// user no longer has.
type ExternalIdentityRepository interface {
	// Create inserts a link, or revives the SAME user's previously revoked one
	// (clearing revocation and refreshing the email). Returns
	// ErrExternalIdentityConflict when the IdP account belongs to another user.
	Create(ctx context.Context, identity *external_identity.ExternalIdentity) error
	// CreateWhileSessionLive is Create as ONE credential-state transition
	// with the session proof: under the user's row
	// lock it checks that the access token (issuedAt) postdates the last
	// password change and that familyID is still a live session, and only
	// then inserts. A revocation takes the same lock, so it has either
	// already ended the family (ErrTokenInvalid here) or runs after the
	// attachment exists.
	CreateWhileSessionLive(ctx context.Context, identity *external_identity.ExternalIdentity, familyID uuid.UUID, issuedAt time.Time, generation int64) error
	// GetByProviderSubject returns the LIVE link for an IdP account, or a
	// not-found error when it is unlinked, revoked, or was never linked.
	GetByProviderSubject(ctx context.Context, provider, subject string) (*external_identity.ExternalIdentity, error)
	// RevokedExists distinguishes "deliberately unlinked" from "never linked",
	// which GetByProviderSubject cannot.
	RevokedExists(ctx context.Context, provider, subject string) (bool, error)
	// ListByUserID returns a user's LIVE identity links.
	ListByUserID(ctx context.Context, userID uuid.UUID) ([]external_identity.ExternalIdentity, error)
	// Revoke soft-deletes one link, scoped to its owner. Returns
	// ErrExternalIdentityNotFound when it does not exist, belongs to somebody
	// else, or is already revoked.
	Revoke(ctx context.Context, userID, identityID uuid.UUID) error
	// DeleteByUserID hard-removes all links of a user, revoked ones included
	// (identity deletion cleanup — there is no account left to recover into).
	DeleteByUserID(ctx context.Context, userID uuid.UUID) error
}
