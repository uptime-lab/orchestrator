package account_invite

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/google/uuid"
)

// DefaultTTL is how long an invite stays acceptable.
const DefaultTTL = 7 * 24 * time.Hour

// AccountInvite invites an email address into an account with a fixed role
// and optional OU scope. The plaintext token is returned exactly once at
// creation (there is no mail delivery); only its SHA-256 hash is stored.
type AccountInvite struct {
	ID         uuid.UUID
	AccountID  uuid.UUID
	Email      string // lowercased; acceptance requires a matching user email
	Role       authz.Role
	OUID       *uuid.UUID
	TokenHash  string
	InvitedBy  uuid.UUID
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	CreatedAt  time.Time
}

// NewAccountInvite creates an invite and returns it together with the
// plaintext token (shown once, never stored).
func NewAccountInvite(accountID uuid.UUID, email string, role authz.Role, ouID *uuid.UUID, invitedBy uuid.UUID) (*AccountInvite, string, error) {
	token, err := generateToken()
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	invite := &AccountInvite{
		ID:        uuid.New(),
		AccountID: accountID,
		Email:     NormalizeEmail(email),
		Role:      role,
		OUID:      ouID,
		TokenHash: HashToken(token),
		InvitedBy: invitedBy,
		ExpiresAt: now.Add(DefaultTTL),
		CreatedAt: now,
	}
	return invite, token, nil
}

// NormalizeEmail case-folds an email for comparison and storage.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// HashToken returns the stored form of an invite token.
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// generateToken returns a random invite token with an "inv_" prefix.
func generateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate invite token: %w", err)
	}
	return "inv_" + hex.EncodeToString(bytes), nil
}
