package entitlement

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Record is one applied entitlement file as the install stores it: the paste
// form verbatim (so it can be re-verified on every read, against whatever
// keys the running build trusts) plus the signed terms denormalised for
// listing. One row per paste, never edited: extending or upgrading is a NEW
// file, and the newest valid one wins at read time, so nothing is ever
// revoked by applying the next.
//
// The table is instance-wide — there is no account_id — and so it is not
// part of any tenant's data: a tenant purge leaves it alone.
type Record struct {
	ID         uuid.UUID
	Document   string
	CustomerID string
	Kind       Kind
	DeviceCap  int
	IssuedAt   time.Time
	ExpiresAt  time.Time
	AppliedAt  time.Time
	// Who pasted it. Recorded for the card and the trail; it grants nothing.
	AppliedByUserID    uuid.UUID
	AppliedByAccountID uuid.UUID
}

// RejectedErrorCode is the machine-readable code on a refused paste.
const RejectedErrorCode = "entitlement_rejected"

// RejectedError is the typed refusal of a pasted file: it did not verify, or
// its terms do not validate. The reason is one of this package's own
// sentinels, whose text is written for the person who pasted the file.
type RejectedError struct {
	Reason error
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("the entitlement file was not accepted (%v). Paste the whole value exactly as it was issued, "+
		"and check that it was issued for this platform", e.Reason)
}

func (e *RejectedError) Unwrap() error { return e.Reason }

// The request itself is what must change — a different paste — so 400.
func (e *RejectedError) HTTPStatus() int   { return http.StatusBadRequest }
func (e *RejectedError) ErrorCode() string { return RejectedErrorCode }

// NotInstanceOperatorErrorCode is the machine-readable code on a paste from
// an account that does not operate the install.
const NotInstanceOperatorErrorCode = "not_instance_operator"

// NotInstanceOperatorError refuses an entitlement paste from a super_admin
// of an account other than the install's operator. The entitlement is
// instance-wide, and an install that hosts several accounts has one
// super_admin per account: any of them replacing the file would change what
// every other account may admit.
type NotInstanceOperatorError struct{}

func (e *NotInstanceOperatorError) Error() string {
	return "the device entitlement is instance-wide and is managed by the account that operates this install. " +
		"Ask the install's operator to apply the file"
}

// The caller is authenticated and the request well-formed; this account may
// not make it, and no retry changes that, so 403.
func (e *NotInstanceOperatorError) HTTPStatus() int   { return http.StatusForbidden }
func (e *NotInstanceOperatorError) ErrorCode() string { return NotInstanceOperatorErrorCode }
