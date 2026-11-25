package app_errors

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

var (
	// ErrUserPasswordTooLong: the password is longer than bcrypt can hash.
	// Counted in bytes, so the message says bytes: a passphrase in a
	// non-Latin script reaches the limit in far fewer characters. The number
	// is duplicated from user.MaxPasswordLength for the same reason the floor
	// is in ErrUserPasswordTooShort; a test fails if the two drift.
	ErrUserPasswordTooLong = errors.New("user password must be at most 72 bytes (fewer characters when it uses non-Latin letters)")

	// ErrOIDCAccountExists: a federated sign-in matched an existing local
	// account by email, and that account has no password to prove itself
	// with on the spot. A sign-in never attaches a new identity to an
	// existing account on its own; the owner signs in the way they already
	// can and links the provider from account settings.
	ErrOIDCAccountExists = errors.New("an account with this email already exists; sign in, then link the identity from account settings")
)

// TwoFactorLockedErrorCode is the machine-readable code on the answer to a
// second-factor check refused because of too many recent wrong codes. The
// refusal is temporary; a client should tell the user to wait rather than to
// retry.
const TwoFactorLockedErrorCode = "two_factor_locked"

// LoginThrottledErrorCode is the code on a password sign-in refused because
// this address has failed too often recently.
const LoginThrottledErrorCode = "login_throttled"

// LoginThrottledError refuses a password check for an address that has
// failed too often in a short window. It is answered before the password is
// compared, so it says nothing about whether the password was right, and it
// is keyed by the address as typed, so it says nothing about whether an
// account exists either.
type LoginThrottledError struct {
	RetryAfter time.Duration
}

func (e *LoginThrottledError) Error() string {
	return fmt.Sprintf("too many failed sign-in attempts for this address; try again in %d seconds", e.RetryAfterSeconds())
}

// RetryAfterSeconds rounds up, so a client that waits that long is admitted.
func (e *LoginThrottledError) RetryAfterSeconds() int {
	secs := int((e.RetryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}

func (e *LoginThrottledError) HTTPStatus() int   { return http.StatusTooManyRequests }
func (e *LoginThrottledError) ErrorCode() string { return LoginThrottledErrorCode }
