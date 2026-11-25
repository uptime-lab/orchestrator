// Package password_reset models the one-time links that let a user who has
// lost their password prove mailbox control and choose a new one.
//
// It deliberately mirrors domain/email_verification — opaque token, SHA-256 at
// rest, single-use enforced by the database — but the threat model is stricter.
// A verification link only confirms an address; a reset link IS account
// takeover for anyone holding it. Hence the much shorter TTL below, and hence
// the service consuming it stamps PasswordChangedAt, which invalidates every
// refresh token issued before the reset.
package password_reset

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// Prefix marks the token in logs and links; it is part of the secret.
	Prefix = "prt_"
	// DefaultTTL is short on purpose: unlike a verification link, a reset link
	// in the wrong hands is a full account takeover, and the cost of expiry is
	// only one more request from the login page.
	DefaultTTL = 1 * time.Hour
	// tokenBytes matches the other one-time tokens in the system (256 bits).
	tokenBytes = 32
)

// PasswordReset is a pending password-reset grant for one user.
//
// Email is captured at mint time and re-checked on confirm: if the user changes
// their address before clicking, a link mailed to the old mailbox must not
// still reset the account.
type PasswordReset struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Email     string
	TokenHash string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// New mints a reset grant for the user's current address. The plaintext token
// is returned ONCE — only its digest is persisted, so a database read does not
// yield usable links.
func New(userID uuid.UUID, email string, ttl time.Duration) (*PasswordReset, string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := Prefix + hex.EncodeToString(raw)
	now := time.Now()
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &PasswordReset{
		ID:        uuid.New(),
		UserID:    userID,
		Email:     strings.ToLower(strings.TrimSpace(email)),
		TokenHash: Hash(token),
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}, token, nil
}

// Hash digests a token for storage and lookup. Lookup is by digest, so no
// constant-time comparison is needed: the index does an equality match on a
// SHA-256 value an attacker cannot steer.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
