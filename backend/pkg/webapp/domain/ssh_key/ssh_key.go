package ssh_key

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// KeyType represents the type of SSH key
type KeyType string

const (
	// KeyTypeSSH is a regular SSH public key written to ~/.ssh/authorized_keys
	KeyTypeSSH KeyType = "ssh"
	// KeyTypeCA is a CA public key written to /etc/ssh/ca_key.pub and configured in sshd_config
	KeyTypeCA KeyType = "ca"
)

// ValidKeyTypes contains all valid key types
var ValidKeyTypes = []KeyType{KeyTypeSSH, KeyTypeCA}

// IsValidKeyType checks if the given key type is valid
func IsValidKeyType(kt string) bool {
	for _, valid := range ValidKeyTypes {
		if string(valid) == kt {
			return true
		}
	}
	return false
}

// SSHKey represents an SSH public key in the system
type SSHKey struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	Alias       string
	KeyType     KeyType  // Type of key: "ssh" (authorized_keys) or "ca" (certificate authority)
	KeyContent  string   // Public key text; stored but never returned in API responses
	Fingerprint string   // MD5 fingerprint in format aa:bb:cc:dd:...
	IsDefault   bool     // Whether this is the default key for the account
	Tags        []string // free-form purpose labels (shared rules in domain/tags)

	// Server-generated "connection keypairs" additionally hold a private key the
	// backend uses to authenticate outbound SSH (see the SSH-adopt flow). The
	// private key is encrypted at rest and NEVER returned by the API.
	//
	// PrivateKey is the plaintext OpenSSH private key. It is populated only when a
	// caller explicitly loads it for internal use (GetWithPrivateKey) or supplies
	// it at generation time; ordinary reads leave it empty. HasPrivateKey reflects
	// whether an encrypted private key exists, independent of PrivateKey being
	// loaded, so the API can flag "connection keys" without decrypting.
	PrivateKey    string
	HasPrivateKey bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Valid OpenSSH key types
var validKeyTypes = []string{
	"ssh-rsa",
	"ssh-ed25519",
	"ecdsa-sha2-nistp256",
	"ecdsa-sha2-nistp384",
	"ecdsa-sha2-nistp521",
	"ssh-dss",
}

// OpenSSH key pattern: <key-type> <base64-data> [optional-comment]
var sshKeyPattern = regexp.MustCompile(`^(\S+)\s+([A-Za-z0-9+/=]+)\s*(.*)$`)

// ValidateOpenSSHFormat validates that the key content is in OpenSSH format
func ValidateOpenSSHFormat(keyContent string) error {
	keyContent = strings.TrimSpace(keyContent)
	if keyContent == "" {
		return fmt.Errorf("key content cannot be empty")
	}

	// Split by newlines and take the first non-empty line
	lines := strings.Split(keyContent, "\n")
	var keyLine string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			keyLine = line
			break
		}
	}

	if keyLine == "" {
		return fmt.Errorf("no valid key line found")
	}

	// Match the OpenSSH pattern
	matches := sshKeyPattern.FindStringSubmatch(keyLine)
	if len(matches) < 3 {
		return fmt.Errorf("invalid SSH key format: expected '<key-type> <base64-data> [comment]'")
	}

	keyType := matches[1]
	base64Data := matches[2]

	// Validate key type
	validType := false
	for _, valid := range validKeyTypes {
		if keyType == valid {
			validType = true
			break
		}
	}
	if !validType {
		return fmt.Errorf("unsupported key type: %s (supported types: %v)", keyType, validKeyTypes)
	}

	// Validate base64 data
	if _, err := base64.StdEncoding.DecodeString(base64Data); err != nil {
		return fmt.Errorf("invalid base64 data in key: %w", err)
	}

	return nil
}

// CalculateMD5Fingerprint calculates the MD5 fingerprint of an SSH public key
// Format: aa:bb:cc:dd:ee:ff:... (colon-separated hex pairs)
func CalculateMD5Fingerprint(keyContent string) (string, error) {
	keyContent = strings.TrimSpace(keyContent)
	if keyContent == "" {
		return "", fmt.Errorf("key content cannot be empty")
	}

	// Split by newlines and take the first non-empty line
	lines := strings.Split(keyContent, "\n")
	var keyLine string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			keyLine = line
			break
		}
	}

	if keyLine == "" {
		return "", fmt.Errorf("no valid key line found")
	}

	// Extract base64 data
	matches := sshKeyPattern.FindStringSubmatch(keyLine)
	if len(matches) < 3 {
		return "", fmt.Errorf("invalid SSH key format")
	}

	base64Data := matches[2]

	// Decode base64
	keyData, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 data: %w", err)
	}

	// Calculate MD5 hash
	hash := md5.Sum(keyData)

	// Format as colon-separated hex pairs
	hexStr := hex.EncodeToString(hash[:])
	var fingerprint strings.Builder
	for i := 0; i < len(hexStr); i += 2 {
		if i > 0 {
			fingerprint.WriteString(":")
		}
		fingerprint.WriteString(hexStr[i : i+2])
	}

	return fingerprint.String(), nil
}

// NewSSHKey creates a new SSHKey instance with default values
func NewSSHKey(accountID uuid.UUID, alias, keyContent string, keyType KeyType) (*SSHKey, error) {
	// Validate key format
	if err := ValidateOpenSSHFormat(keyContent); err != nil {
		return nil, err
	}

	// Validate key type, default to SSH if empty
	if keyType == "" {
		keyType = KeyTypeSSH
	}
	if !IsValidKeyType(string(keyType)) {
		return nil, fmt.Errorf("invalid key type: %s", keyType)
	}

	// Calculate fingerprint
	fingerprint, err := CalculateMD5Fingerprint(keyContent)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate fingerprint: %w", err)
	}

	now := time.Now()
	return &SSHKey{
		ID:          uuid.New(),
		AccountID:   accountID,
		Alias:       alias,
		KeyType:     keyType,
		KeyContent:  keyContent,
		Fingerprint: fingerprint,
		IsDefault:   false, // Will be set by service if it's the first key
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// NewSSHKeyWithID creates a new SSHKey instance with a specific ID
func NewSSHKeyWithID(id, accountID uuid.UUID, alias, keyContent string, keyType KeyType) (*SSHKey, error) {
	// Validate key format
	if err := ValidateOpenSSHFormat(keyContent); err != nil {
		return nil, err
	}

	// Validate key type, default to SSH if empty
	if keyType == "" {
		keyType = KeyTypeSSH
	}
	if !IsValidKeyType(string(keyType)) {
		return nil, fmt.Errorf("invalid key type: %s", keyType)
	}

	// Calculate fingerprint
	fingerprint, err := CalculateMD5Fingerprint(keyContent)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate fingerprint: %w", err)
	}

	now := time.Now()
	return &SSHKey{
		ID:          id,
		AccountID:   accountID,
		Alias:       alias,
		KeyType:     keyType,
		KeyContent:  keyContent,
		Fingerprint: fingerprint,
		IsDefault:   false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// UpdateKeyContent updates the key content and recalculates the fingerprint
func (k *SSHKey) UpdateKeyContent(keyContent string) error {
	// Validate key format
	if err := ValidateOpenSSHFormat(keyContent); err != nil {
		return err
	}

	// Calculate fingerprint
	fingerprint, err := CalculateMD5Fingerprint(keyContent)
	if err != nil {
		return fmt.Errorf("failed to calculate fingerprint: %w", err)
	}

	k.KeyContent = keyContent
	k.Fingerprint = fingerprint
	k.UpdatedAt = time.Now()
	return nil
}
