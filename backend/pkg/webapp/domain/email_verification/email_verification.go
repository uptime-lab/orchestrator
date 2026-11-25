// Package email_verification models the one-time links that prove a user
// controls the mailbox they signed up with.
//
// Why it matters beyond hygiene: `users.email_verified_at` is what allows a
// Google/OIDC login to silently attach to an existing local account. Password
// signup cannot verify on its own, so without this an attacker who
// pre-registered a victim's address could harvest the victim's Google session
// into an attacker-controlled account. Proving the mailbox is what closes that.
//
// The token is opaque and stored only as a SHA-256 digest, so a database read
// does not yield usable links. Consumption is a single conditional UPDATE, so
// TTL and single-use are enforced by the database rather than by a
// check-then-act in Go.
package email_verification

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
	Prefix = "evt_"
	// DefaultTTL is generous because the link is delivered by email, which may
	// be delayed, and the cost of an expired link is one resend.
	DefaultTTL = 24 * time.Hour
	// tokenBytes matches the invite token's entropy (256 bits).
	tokenBytes = 32
)

// EmailVerification is a pending proof-of-mailbox for one user.
//
// Email is captured at mint time and re-checked on confirm: if the user changes
// their address again before clicking, the older link must not verify the newer
// address.
type EmailVerification struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Email     string
	TokenHash string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// New mints a verification for the user's address. The plaintext token is
// returned ONCE — only its digest is persisted, so a lost token can only be
// replaced, never recovered.
func New(userID uuid.UUID, email string, ttl time.Duration) (*EmailVerification, string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := Prefix + hex.EncodeToString(raw)
	now := time.Now()
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &EmailVerification{
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
