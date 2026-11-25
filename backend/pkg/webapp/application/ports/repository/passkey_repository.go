package repository

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/passkey"
	"github.com/google/uuid"
)

// PasskeyRepository persists registered WebAuthn credentials.
type PasskeyRepository interface {
	// Create stores a newly registered credential. Fixtures and tests only:
	// a registration goes through CreateWhileSessionLive.
	Create(ctx context.Context, p *passkey.Passkey) error
	// CreateWhileSessionLive is Create as ONE credential-state transition
	// with the session proof, the boundary OIDC linking
	// uses: under the user's row lock it checks that the access token
	// (issuedAt, generation) postdates the last credential change and that
	// familyID is still a live session, and only then inserts. The step-up
	// middleware proved the session before the handler ran; a sign-out or a
	// recovery that lands between that check and this insert takes the same
	// lock and therefore either already ended the family (ErrTokenInvalid
	// here, nothing stored) or runs after the credential exists.
	CreateWhileSessionLive(ctx context.Context, p *passkey.Passkey, familyID uuid.UUID, issuedAt time.Time, generation int64) error
	// ListByUserID returns a user's credentials (public data only).
	ListByUserID(ctx context.Context, userID uuid.UUID) ([]passkey.Passkey, error)
	// GetByCredentialID resolves the credential an assertion presents. This is
	// the lookup that makes discoverable (usernameless) login possible: the
	// handle alone identifies both the credential and its owner.
	GetByCredentialID(ctx context.Context, credentialID []byte) (*passkey.Passkey, error)
	// Update persists counter and last-used changes after an assertion.
	Update(ctx context.Context, p *passkey.Passkey) error
	// Delete removes one credential belonging to the user. Scoped by user so a
	// caller cannot delete somebody else's key by guessing an id.
	Delete(ctx context.Context, userID, passkeyID uuid.UUID) error
	// CountByUserID reports how many credentials a user holds — used to refuse
	// removing the last sign-in method.
	CountByUserID(ctx context.Context, userID uuid.UUID) (int64, error)
}
