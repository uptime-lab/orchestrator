// Package crypto provides symmetric encryption for secrets at rest using
// AES-256-GCM. Ciphertext is returned base64-encoded with the random nonce
// prepended, prefixed with the marker "v1:".
//
// NOTE on "v1:": it marks the ciphertext FORMAT, not which key was used — every
// key writes the same "v1:" prefix. Rotation therefore cannot be audited by
// reading the prefix; it is established by trial decryption
// (EncryptedWithPrimary). Ciphertexts written before the prefix existed have no
// marker and decrypt the same way.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

// ErrCiphertextTooShort is returned when the input is too short to contain a nonce.
var ErrCiphertextTooShort = errors.New("ciphertext too short")

// versionPrefix marks ciphertexts produced with the current (v1) key.
const versionPrefix = "v1:"

// Encryptor encrypts and decrypts strings using AES-256-GCM. It holds one
// PRIMARY key (used to encrypt) and zero or more FALLBACK keys (tried, in
// order, only when decrypting). This supports key rotation: deploy with the
// new key primary and the old key as a fallback, rewrap the data, then drop
// the fallback.
type Encryptor struct {
	key       []byte   // primary, exactly 32 bytes — encrypt + first decrypt attempt
	fallbacks [][]byte // decrypt-only, each 32 bytes
}

func derive(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// NewEncryptor builds an Encryptor from an arbitrary key string. The key is
// always run through SHA-256 so any passphrase length yields a valid 32-byte
// AES-256 key.
func NewEncryptor(key string) *Encryptor {
	return &Encryptor{key: derive(key)}
}

// NewEncryptorWithFallbacks builds an Encryptor that encrypts with primaryKey
// and, when decrypting, also tries each fallback key (older keys during a
// rotation). Empty fallback strings are ignored.
func NewEncryptorWithFallbacks(primaryKey string, fallbackKeys []string) *Encryptor {
	e := &Encryptor{key: derive(primaryKey)}
	for _, f := range fallbackKeys {
		if f != "" {
			e.fallbacks = append(e.fallbacks, derive(f))
		}
	}
	return e
}

// Encrypt returns the base64-encoded ciphertext (nonce prepended) of plaintext.
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return versionPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. It accepts both current ("v1:"-prefixed) and
// legacy (unprefixed) ciphertexts, and tries the primary key first, then each
// fallback key (older keys during a rotation). GCM authentication makes a
// wrong key fail cleanly, so trying keys in turn is safe.
func (e *Encryptor) Decrypt(ciphertext string) (string, error) {
	ciphertext = strings.TrimPrefix(ciphertext, versionPrefix)
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	keys := append([][]byte{e.key}, e.fallbacks...)
	var lastErr error
	for _, key := range keys {
		block, err := aes.NewCipher(key)
		if err != nil {
			lastErr = err
			continue
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			lastErr = err
			continue
		}
		nonceSize := gcm.NonceSize()
		if len(raw) < nonceSize {
			return "", ErrCiphertextTooShort
		}
		nonce, sealed := raw[:nonceSize], raw[nonceSize:]
		plaintext, err := gcm.Open(nil, nonce, sealed, nil)
		if err != nil {
			lastErr = err
			continue // wrong key — try the next
		}
		return string(plaintext), nil
	}
	return "", lastErr
}

// EncryptedWithPrimary reports whether the ciphertext decrypts under the
// PRIMARY key alone. A rewrap uses this to skip rows already on the new key
// (idempotency) and to confirm a row was migrated.
func (e *Encryptor) EncryptedWithPrimary(ciphertext string) bool {
	primaryOnly := &Encryptor{key: e.key}
	_, err := primaryOnly.Decrypt(ciphertext)
	return err == nil
}

// Rewrap decrypts a ciphertext (with any accepted key) and re-encrypts it under
// the PRIMARY key. Used by the key-rotation rewrap job.
func (e *Encryptor) Rewrap(ciphertext string) (string, error) {
	pt, err := e.Decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	return e.Encrypt(pt)
}
