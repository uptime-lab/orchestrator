package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
)

// RefreshTokenRepository persists issued refresh credentials so they can be
// rotated and revoked.
type RefreshTokenRepository interface {
	// Create records a newly issued token.
	Create(ctx context.Context, token *refresh_token.RefreshToken) error
	// Claim atomically marks an unused, unexpired, unrevoked token as redeemed
	// and returns it. TTL and single-use live in the UPDATE's WHERE clause, so
	// two concurrent refreshes cannot both win.
	//
	// Returns ErrRefreshTokenReused when the row exists but was already
	// redeemed — the caller must then revoke the whole family — and
	// ErrTokenInvalid when there is no such token at all.
	Claim(ctx context.Context, tokenHash string) (*refresh_token.RefreshToken, error)
	// Rotate is Claim plus the successor's Create as ONE credential-state
	// transition under the user's row lock: the presented
	// token is claimed, the family is checked to be unrevoked and the user's
	// password unchanged since issuedAt, and only then is the successor
	// inserted — so a revocation that ran meanwhile (which takes the same
	// lock) has either already revoked the family, refusing the rotation, or
	// runs after and finds the successor. Answers like Claim: the claimed
	// row, ErrRefreshTokenReused (with the row) on a replay, ErrTokenInvalid
	// when the token, the family or the credential epoch is dead.
	// generation is the presented token's credential generation (0 = a token
	// from before the claim existed, tolerated); a row that has moved on
	// refuses.
	Rotate(ctx context.Context, tokenHash string, issuedAt time.Time, generation int64, successor *refresh_token.RefreshToken) (*refresh_token.RefreshToken, error)
	// Issue records the FIRST token of a session, or a sibling in an
	// existing family (an account switch), under the user's row lock. Under
	// the lock it re-reads
	// the credential epoch — users.credential_generation AND
	// password_changed_at — and refuses when either differs from the
	// snapshot the pair was minted from (generation, passwordChangedAt), so
	// a login whose password was verified BEFORE a reset cannot store a
	// session AFTER it. Both columns, because a reset written outside this
	// API may move only the timestamp; the mint time itself is not
	// compared, since the pair is minted after the reset and would pass.
	// When the family already has rows it must be live in the same sense
	// lockLiveSession uses — no revoked row and at least one unrevoked,
	// unexpired one — so a revocation that took the lock first is final.
	// Every refusal is ErrTokenInvalid and stores nothing. generation 0 is
	// tolerated as in Rotate. Create stays for fixtures and tests; no login
	// path may call it.
	Issue(ctx context.Context, token *refresh_token.RefreshToken, generation int64, passwordChangedAt *time.Time) error
	// RevokeFamily kills every token descended from one login. Used on replay
	// detection and on logout.
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
	// RevokeAllForUser kills every session a user holds (password reset,
	// administrative disable).
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
	// DeleteExpired prunes rows that can no longer be redeemed, returning how
	// many went (the periodic sweep in main.go logs a nonzero count).
	DeleteExpired(ctx context.Context) (int64, error)
	// ListSessions returns one entry per live token family — one per login —
	// newest activity first. This is the inventory a user needs to answer
	// "where am I signed in", which nothing could produce before.
	ListSessions(ctx context.Context, userID uuid.UUID) ([]refresh_token.Session, error)
	// RevokeFamilyForUser kills one session, refusing a family that belongs to
	// somebody else. Scoped on purpose: the family id travels to the client, so
	// the check cannot live in the caller.
	RevokeFamilyForUser(ctx context.Context, userID, familyID uuid.UUID) error
	// RevokeOtherFamilies kills every live session except one — "sign out
	// everywhere else", the action a user takes after losing a device.
	RevokeOtherFamilies(ctx context.Context, userID, keepFamilyID uuid.UUID) error
	// EnforceSessionCap revokes the least recently active families beyond keep
	// and reports how many it killed. Called on fresh login, so an account that
	// never logs out cannot accumulate sessions without bound.
	EnforceSessionCap(ctx context.Context, userID uuid.UUID, keep int) (int, error)
	// AnnotateDevice attaches the request-side identification to an issued
	// token. Separate from Create because the device is known to the HTTP
	// handler, not to the service that mints the pair.
	AnnotateDevice(ctx context.Context, tokenHash string, device refresh_token.Device) error
}
