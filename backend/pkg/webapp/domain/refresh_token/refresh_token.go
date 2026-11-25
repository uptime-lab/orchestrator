// Package refresh_token gives the previously stateless refresh token a
// server-side record, so it can be rotated, revoked and — most importantly —
// detected when replayed.
//
// Before this, a refresh token was a bearer JWT with nothing behind it: no
// store, no rotation, no logout. Whoever held one could mint access tokens for
// its full TTL (7 days by default), and the only things that could stop them
// were a password change or losing account membership. Neither is something a
// user does when they merely suspect a stolen laptop.
//
// The design is the standard one: each token is single-use, redeeming it issues
// a successor, and redeeming an ALREADY-redeemed token is treated as theft.
// Because a legitimate client never replays a spent token, a replay means two
// parties hold the same secret — so the whole family is revoked and both are
// forced to log in again. Losing a session is the correct outcome there.
package refresh_token

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RefreshToken is one issued refresh credential.
type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	AccountID uuid.UUID
	// FamilyID ties a token to every successor rotated out of it. Reuse of any
	// member revokes the whole family, which is what makes theft detectable
	// rather than merely survivable.
	FamilyID uuid.UUID
	// TokenHash is the SHA-256 of the JWT. The token itself is never stored, so
	// a database read yields nothing usable.
	TokenHash string
	ExpiresAt time.Time
	// UsedAt marks redemption. A second redemption is the replay signal.
	UsedAt *time.Time
	// RevokedAt marks a token killed by logout, by a family revocation, or by
	// an administrative action.
	RevokedAt *time.Time
	CreatedAt time.Time
	// UserAgent and IP identify the session for a human reading their session
	// list. Self-reported, never an authorization input.
	UserAgent string
	IP        string
}

// New records a freshly issued token. familyID is uuid.Nil for a fresh login,
// which starts a new family.
func New(userID, accountID, familyID uuid.UUID, token string, expiresAt time.Time) *RefreshToken {
	if familyID == uuid.Nil {
		familyID = uuid.New()
	}
	return &RefreshToken{
		ID:        uuid.New(),
		UserID:    userID,
		AccountID: accountID,
		FamilyID:  familyID,
		TokenHash: Hash(token),
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}
}

// Hash digests a token for storage and lookup. Lookup is by digest, so no
// constant-time comparison is needed: the index does an equality match on a
// SHA-256 value an attacker cannot steer.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
