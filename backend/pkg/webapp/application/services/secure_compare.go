package services

import "crypto/subtle"

// SecureTokenEqual reports whether two secret tokens are equal using a
// constant-time comparison, avoiding a timing side-channel that a plain
// string comparison would leak on network-facing token checks.
func SecureTokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
