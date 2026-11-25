// Package adoption_code implements the one-time opaque handle behind the
// one-command node adoption link. The link must not carry the adoption blob
// itself — account id, topology and a long-lived reusable credential — so it
// carries only a random code; the device exchanges
// it over TLS for the adoption config. Codes are single-use, short-lived,
// stored hashed, and revoked by re-minting (per-node master-token pattern).
package adoption_code

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Prefix lets tooling (install.sh) tell a code from a legacy adoption blob.
const Prefix = "ac_"

// DefaultTTL bounds the window between minting the link and running it on the
// device. Long enough for a human round-trip, short enough that a leaked link
// (chat log, screen share) goes stale quickly. Re-mint any time.
const DefaultTTL = 1 * time.Hour

// AdoptionCode is one mint of the exchange handle for one node. Only the
// SHA-256 of the code is stored; the plaintext exists in the mint response.
type AdoptionCode struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	NodeID    uuid.UUID
	CodeHash  string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// New mints a code for a node and returns the entity plus the plaintext code
// (returned once, never stored).
func New(accountID, nodeID uuid.UUID) (*AdoptionCode, string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("failed to generate adoption code: %w", err)
	}
	code := Prefix + hex.EncodeToString(raw)
	now := time.Now()
	return &AdoptionCode{
		ID:        uuid.New(),
		AccountID: accountID,
		NodeID:    nodeID,
		CodeHash:  Hash(code),
		ExpiresAt: now.Add(DefaultTTL),
		CreatedAt: now,
	}, code, nil
}

// Hash derives the stored form of a code.
func Hash(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

// LooksLikeCode reports whether a string has the code shape (used to route
// script input between the exchange flow and a legacy blob).
func LooksLikeCode(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, Prefix) {
		return false
	}
	rest := s[len(Prefix):]
	if len(rest) != 32 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}
