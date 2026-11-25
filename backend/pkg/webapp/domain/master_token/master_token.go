package master_token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidName  = errors.New("name is required")
	ErrInvalidToken = errors.New("token is required")
)

type MasterToken struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	Name            string
	Token           string // The actual token value (only set on creation, not stored)
	TokenHash       string // SHA256 hash of the token (stored in DB)
	IsActive        bool
	DefaultUsername string
	DefaultPassword string // ENCRYPTED at rest
	// DefaultPasswordUnreadable: stored but undecryptable; see node.Node for the
	// contract. Never persisted.
	DefaultPasswordUnreadable bool
	SSHKeyID                  *uuid.UUID
	// OUID: devices adopted with this token are placed into this OU.
	OUID       *uuid.UUID
	Tags       []string // free-form purpose labels (shared rules in domain/tags)
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewMasterToken creates a new master token with a generated token value
func NewMasterToken(accountID uuid.UUID, name, defaultUsername, defaultPassword string, sshKeyID *uuid.UUID) (*MasterToken, error) {
	if name == "" {
		return nil, ErrInvalidName
	}

	token, err := generateToken()
	if err != nil {
		return nil, err
	}

	tokenHash := HashToken(token)

	now := time.Now()
	return &MasterToken{
		ID:              uuid.New(),
		AccountID:       accountID,
		Name:            name,
		Token:           token,
		TokenHash:       tokenHash,
		IsActive:        true,
		DefaultUsername: defaultUsername,
		DefaultPassword: defaultPassword,
		SSHKeyID:        sshKeyID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// NewMasterTokenWithExpiry creates a new master token with an expiration time
func NewMasterTokenWithExpiry(accountID uuid.UUID, name, defaultUsername, defaultPassword string, sshKeyID *uuid.UUID, expiresAt time.Time) (*MasterToken, error) {
	mt, err := NewMasterToken(accountID, name, defaultUsername, defaultPassword, sshKeyID)
	if err != nil {
		return nil, err
	}
	mt.ExpiresAt = &expiresAt
	return mt, nil
}

// IsExpired checks if the token has expired
func (mt *MasterToken) IsExpired() bool {
	if mt.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*mt.ExpiresAt)
}

// IsValid checks if the token is active and not expired
func (mt *MasterToken) IsValid() bool {
	return mt.IsActive && !mt.IsExpired()
}

// UpdateLastUsed updates the last used timestamp
func (mt *MasterToken) UpdateLastUsed() {
	now := time.Now()
	mt.LastUsedAt = &now
	mt.UpdatedAt = now
}

// Deactivate marks the token as inactive
func (mt *MasterToken) Deactivate() {
	mt.IsActive = false
	mt.UpdatedAt = time.Now()
}

// Activate marks the token as active
func (mt *MasterToken) Activate() {
	mt.IsActive = true
	mt.UpdatedAt = time.Now()
}

// generateToken generates a secure random token with "mt_" prefix
func generateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "mt_" + hex.EncodeToString(bytes), nil
}

// HashToken creates a SHA256 hash of the token for storage
func HashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
