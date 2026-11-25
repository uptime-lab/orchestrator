package commands

import (
	"fmt"
	"regexp"
)

// HostKeyFingerprintPrefix is the only fingerprint form the SSH adapter emits
// and compares: OpenSSH's "SHA256:" followed by the unpadded base64 digest.
const HostKeyFingerprintPrefix = "SHA256:"

// hostKeyFingerprintShape is the EXACT shape golang.org/x/crypto/ssh's
// FingerprintSHA256 produces: the prefix and 43 unpadded base64 characters (a
// 32-byte digest). Anything else cannot match a real key, so accepting it only
// carries an attacker-sized string into the job row, the audit detail, the
// error message and the log — a prefix check alone let ~32 MiB through.
var hostKeyFingerprintShape = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// ValidateHostKeyFingerprint accepts an empty value (no pin) or a SHA256
// fingerprint of exactly the shape the adapter emits. Anything else is refused
// at the request boundary rather than carried to the SSH handshake, where a
// malformed pin would read as "the device's key changed".
func ValidateHostKeyFingerprint(field, value string) error {
	if value == "" {
		return nil
	}
	if !hostKeyFingerprintShape.MatchString(value) {
		return fmt.Errorf("%s must be a SHA256 host-key fingerprint (SHA256: followed by 43 base64 characters), as reported by the connection test", field)
	}
	return nil
}
