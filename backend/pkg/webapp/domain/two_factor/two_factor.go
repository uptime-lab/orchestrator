// Package two_factor models a user's TOTP second factor and its recovery
// codes.
//
// Two properties shape the design:
//
//   - The shared secret is a credential equivalent to the password, so it is
//     encrypted at rest with the same AES-256-GCM encryptor the secrets table
//     uses. Recovery codes are NOT — they are one-time and are stored as
//     SHA-256 digests, exactly like every other one-time token in this system.
//   - Enrollment is two-step. Generating a secret does not enable anything;
//     only a correct code from the user's authenticator confirms it. Enabling
//     on generation would let a user lock themselves out of their own account
//     by closing the tab before scanning the QR.
package two_factor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// recoveryCodeCount is how many single-use codes are issued at confirmation.
	recoveryCodeCount = 10
	// recoveryCodeBytes gives 80 bits per code — far beyond guessable, while
	// still short enough to write down.
	recoveryCodeBytes = 10
)

// TwoFactor is one user's second factor.
type TwoFactor struct {
	UserID uuid.UUID
	// SecretCiphertext is the TOTP shared secret, encrypted. The plaintext is
	// held only long enough to build the enrollment URI and to verify a code.
	SecretCiphertext string
	// ConfirmedAt is nil while enrollment is pending. A pending factor is
	// ignored at login: an unconfirmed secret must never be able to lock
	// anyone out.
	ConfirmedAt *time.Time
	// RecoveryCodeHashes holds SHA-256 digests of the unused codes. A used code
	// is removed rather than marked, so the list doubles as the remaining count.
	RecoveryCodeHashes []string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Enabled reports whether this factor is confirmed and should gate logins.
func (t *TwoFactor) Enabled() bool {
	return t != nil && t.ConfirmedAt != nil
}

// GenerateRecoveryCodes returns fresh codes in plaintext (shown ONCE) together
// with the digests to store.
func GenerateRecoveryCodes() (plain []string, hashes []string, err error) {
	plain = make([]string, 0, recoveryCodeCount)
	hashes = make([]string, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		raw := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		// Grouped for legibility: these get written on paper.
		encoded := hex.EncodeToString(raw)
		code := fmt.Sprintf("%s-%s-%s-%s", encoded[0:5], encoded[5:10], encoded[10:15], encoded[15:20])
		plain = append(plain, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return plain, hashes, nil
}

// HashRecoveryCode normalizes and digests a recovery code. Normalization is
// forgiving on purpose — the user is typing something off a piece of paper
// during a bad day, so case and the grouping dashes must not matter.
func HashRecoveryCode(code string) string {
	normalized := strings.ToLower(strings.TrimSpace(code))
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// ConsumeRecoveryCode removes a matching code and reports whether it matched.
// Single-use is enforced by removal.
func (t *TwoFactor) ConsumeRecoveryCode(code string) bool {
	target := HashRecoveryCode(code)
	for i, h := range t.RecoveryCodeHashes {
		if h == target {
			t.RecoveryCodeHashes = append(t.RecoveryCodeHashes[:i], t.RecoveryCodeHashes[i+1:]...)
			t.UpdatedAt = time.Now()
			return true
		}
	}
	return false
}

// Confirm marks the factor active and installs its recovery codes.
func (t *TwoFactor) Confirm(hashes []string) {
	now := time.Now()
	t.ConfirmedAt = &now
	t.RecoveryCodeHashes = hashes
	t.UpdatedAt = now
}
