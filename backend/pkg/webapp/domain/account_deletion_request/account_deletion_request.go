// Package account_deletion_request is a tenant's standing request to have their
// account erased.
//
// It is a REQUEST, not a deletion, and that split is the design rather than a
// shortcut. Erasing an account means deleting from every account-scoped table
// under one inventory that is known to be complete; an erasure path in this
// server beside an operator's own would be two inventories that must agree
// forever, and the failure mode when they drift is a HALF-purged account — a
// tenant who was told they were deleted and whose credential material is
// still there. So the tenant side files a request the operator executes,
// which is also the shape a grace period fits into if one is ever wanted.
//
// The row is transient: cancelling deletes it. The durable record is the audit
// trail (`account.deletion_requested` / `account.deletion_cancelled`), which is
// the same division the invite cap already relies on — the working table is
// deletable by a request path, the audit trail is not.
package account_deletion_request

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrNameMismatch is returned when the caller's typed account name does not
// match. It is the CONTROL, not decoration: the type-the-name dialog is UX
// sugar, and the server-side echo is what actually stops a misdirected click.
var ErrNameMismatch = errors.New("account name does not match")

// ErrAlreadyRequested is returned when a request is already open. Deliberately
// not idempotent-success: a second request is either a double-click, which
// deserves no action, or a second person asking, which the first requester's
// name in the existing row answers better than a silent overwrite.
var ErrAlreadyRequested = errors.New("a deletion request is already open for this account")

// Request is one open deletion request. At most one exists per account.
type Request struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	RequestedBy uuid.UUID
	RequestedAt time.Time
	// Reason is whatever the customer wanted to say. Free text, shown to the
	// operator, never parsed.
	Reason string
}

// maxReasonLen bounds the free-text field. Generous for a sentence or two and
// short enough that the column cannot be used as storage.
const maxReasonLen = 2000

// New builds a request, verifying the typed name against the account's real one.
//
// The comparison trims and case-folds. An operator typing their own company name
// should not be defeated by a trailing space, and this is a confirmation of
// INTENT — proof that the person knows which account they are on — not a secret.
func New(accountID, requestedBy uuid.UUID, actualName, typedName, reason string) (*Request, error) {
	if !NameMatches(actualName, typedName) {
		return nil, ErrNameMismatch
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > maxReasonLen {
		// Cut on a RUNE boundary, not a raw byte one: slicing a multi-byte UTF-8
		// character in half produces invalid UTF-8, which Postgres rejects on a
		// text column, so a long reason ending mid-character 500'd instead of being
		// stored truncated. Back the cut off any continuation byte.
		cut := maxReasonLen
		for cut > 0 && !utf8.RuneStart(reason[cut]) {
			cut--
		}
		reason = reason[:cut]
	}
	return &Request{
		ID:          uuid.New(),
		AccountID:   accountID,
		RequestedBy: requestedBy,
		RequestedAt: time.Now().UTC(),
		Reason:      reason,
	}, nil
}

// NameMatches reports whether the typed name confirms the account.
func NameMatches(actual, typed string) bool {
	a := strings.ToLower(strings.TrimSpace(actual))
	t := strings.ToLower(strings.TrimSpace(typed))
	// An empty account name would otherwise be confirmed by an empty string,
	// which is the one input a caller supplies by accident.
	return a != "" && a == t
}
