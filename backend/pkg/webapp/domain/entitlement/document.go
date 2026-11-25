// Package entitlement is the licence-key functionality of the platform: a
// signed document that raises the number of devices one install may admit
// above the free cap. It is the "license key functionality" the Fair Core
// License refers to, and it is deliberately small: a file format, a signature
// check against an embedded public key, and one arithmetic question — how many
// devices may this install admit right now.
//
// What an entitlement does NOT do is as important as what it does. It never
// locks the UI, never withholds a command, a recovery boot or an update, and
// never touches a device already admitted. Its one effect is on ADMISSION: a
// new device is refused once the install holds as many admitted devices as the
// effective cap allows. An expired file keeps every device it admitted.
package entitlement

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// FreeDeviceCap is how many devices an install may admit with no entitlement
// at all, or with one that has lapsed. It is instance-wide: the public promise
// is "free up to this many devices per install", so it is not multiplied by
// the number of accounts an install hosts.
const FreeDeviceCap = 25

// DefaultGraceDays is the grace a signer applies when none is given: after
// expiry the cap holds for this many more days before falling to the free cap.
// A short free key sets it low or to 0 so the grace does not double the term.
const DefaultGraceDays = 30

// Version is the only document version this verifier accepts. A document
// carrying another version is rejected rather than half-interpreted.
const Version = 1

// Kind says what the file is for. It changes nothing in the arithmetic; the
// card shows it so an operator can tell an evaluation from a subscription.
type Kind string

const (
	KindEvaluation   Kind = "evaluation"
	KindSubscription Kind = "subscription"
	// KindHosted marks the file the hosted install runs under. The hosted
	// install runs exactly the same code as every other install, with no
	// build flag and no environment exemption; its file is simply one with no
	// device cap and a long expiry.
	KindHosted Kind = "hosted"
)

// Kinds lists every accepted kind, for validation and for the signer's help.
var Kinds = []Kind{KindEvaluation, KindSubscription, KindHosted}

func (k Kind) valid() bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// Bounds on the free-text fields. The document is stored and shown on a card;
// it is not a place for prose, and a bound keeps a pasted file from carrying
// an arbitrary payload into the database and the audit trail.
const (
	MaxCustomerIDLength = 128
	MaxNoteLength       = 512
	// MaxGraceDays bounds the grace a file may declare, so a signing mistake
	// cannot grant a decade by the back door.
	MaxGraceDays = 3650
)

// Document is the signed payload. Every term is a signed field chosen per file
// at signing time; nothing about an entitlement is configured on the install.
//
// Field order is the canonical JSON order: the signer marshals this struct and
// signs those bytes, and the paste form carries those exact bytes, so a
// verifier never has to re-canonicalise.
type Document struct {
	Version    int       `json:"version"`
	CustomerID string    `json:"customer_id"`
	DeviceCap  int       `json:"device_cap"`
	IssuedAt   time.Time `json:"issued_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Kind       Kind      `json:"kind"`
	GraceDays  int       `json:"grace_days"`
	Note       string    `json:"note"`
}

// Validation sentinels. Each names the field, because the person reading the
// refusal is the one who ran the signer.
var (
	ErrUnsupportedVersion  = errors.New("entitlement: unsupported document version")
	ErrCustomerIDMissing   = errors.New("entitlement: customer_id is required")
	ErrCustomerIDTooLong   = errors.New("entitlement: customer_id is too long")
	ErrDeviceCapNegative   = errors.New("entitlement: device_cap must be 0 (unlimited) or positive")
	ErrKindUnknown         = errors.New("entitlement: kind must be evaluation, subscription or hosted")
	ErrGraceDaysOutOfRange = errors.New("entitlement: grace_days must be between 0 and 3650")
	ErrNoteTooLong         = errors.New("entitlement: note is too long")
	ErrDatesMissing        = errors.New("entitlement: issued_at and expires_at are required")
	ErrExpiryBeforeIssue   = errors.New("entitlement: expires_at must be after issued_at")
	ErrIssuedInFuture      = errors.New("entitlement: issued_at is more than 24 hours in the future")
)

// MaxIssueSkew is how far in the future a file's issued_at may lie. The
// newest valid file wins by its signed issue date, so a date far ahead —
// a signing slip — would outrank every later file until it lapsed; a day
// absorbs any honest clock difference between the signer and the install.
const MaxIssueSkew = 24 * time.Hour

// CheckIssuedAt refuses a document issued more than MaxIssueSkew after now.
// It is the one clock-dependent check, kept out of Validate (which says
// nothing about the clock) and applied wherever a file is accepted: on paste
// and on every resolution.
func (d Document) CheckIssuedAt(now time.Time) error {
	if d.IssuedAt.After(now.Add(MaxIssueSkew)) {
		return ErrIssuedInFuture
	}
	return nil
}

// Validate checks the document's own consistency. It says nothing about the
// clock: an expired document is VALID, it has merely lapsed, and the two are
// kept apart because an expired file still names the fleet it admitted.
func (d Document) Validate() error {
	if d.Version != Version {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, d.Version)
	}
	if strings.TrimSpace(d.CustomerID) == "" {
		return ErrCustomerIDMissing
	}
	if utf8.RuneCountInString(d.CustomerID) > MaxCustomerIDLength {
		return ErrCustomerIDTooLong
	}
	if d.DeviceCap < 0 {
		return ErrDeviceCapNegative
	}
	if !d.Kind.valid() {
		return fmt.Errorf("%w: %q", ErrKindUnknown, d.Kind)
	}
	if d.GraceDays < 0 || d.GraceDays > MaxGraceDays {
		return ErrGraceDaysOutOfRange
	}
	if utf8.RuneCountInString(d.Note) > MaxNoteLength {
		return ErrNoteTooLong
	}
	if d.IssuedAt.IsZero() || d.ExpiresAt.IsZero() {
		return ErrDatesMissing
	}
	if !d.ExpiresAt.After(d.IssuedAt) {
		return ErrExpiryBeforeIssue
	}
	return nil
}

// Phase is where a document stands against the clock.
type Phase string

const (
	// PhaseActive: before expiry. The document's cap applies.
	PhaseActive Phase = "active"
	// PhaseGrace: expired, inside the signed grace. The document's cap still
	// applies; the card says so.
	PhaseGrace Phase = "grace"
	// PhaseLapsed: past expiry and grace. New admissions fall to the free cap;
	// the fleet already admitted is untouched.
	PhaseLapsed Phase = "lapsed"
)

// GraceUntil is the instant the grace ends: expiry plus the signed grace days.
// Counted from the SIGNED expiry, never from any clock the install keeps, so
// a host whose clock predates issued_at cannot be short-changed or extended.
func (d Document) GraceUntil() time.Time {
	return d.ExpiresAt.Add(time.Duration(d.GraceDays) * 24 * time.Hour)
}

// PhaseAt reports the phase at the given instant.
func (d Document) PhaseAt(now time.Time) Phase {
	switch {
	case now.Before(d.ExpiresAt):
		return PhaseActive
	case now.Before(d.GraceUntil()):
		return PhaseGrace
	default:
		return PhaseLapsed
	}
}

// CapBelowFreeFloor reports whether the signed cap is positive but below
// FreeDeviceCap. Such a file is genuine and is accepted; it simply cannot
// lower the floor, so it acts as the free cap (see EffectiveCap).
func (d Document) CapBelowFreeFloor() bool {
	return d.DeviceCap > 0 && d.DeviceCap < FreeDeviceCap
}

// EffectiveCap is the number of devices this document lets the install admit
// at the given instant: its own cap while active or in grace (0 = unlimited),
// the free cap once lapsed.
//
// A positive signed cap below FreeDeviceCap is CLAMPED up to it rather than
// refused in Validate. The free cap is promised as a floor, and clamping keeps
// that promise literally while a strange-but-genuine file still verifies: a
// refusal would make the file "not accepted", which is loud on the card for a
// document that grants nothing worse than the free tier. The signer refuses to
// issue such a cap in the first place; this is the server's half of the same
// rule, so a file from any other issuer cannot lower the floor either.
func (d Document) EffectiveCap(now time.Time) int {
	if d.PhaseAt(now) == PhaseLapsed || d.CapBelowFreeFloor() {
		return FreeDeviceCap
	}
	return d.DeviceCap
}

// Unlimited reports whether a cap value means "no bound". The signed field
// uses 0 for it, and so does every cap the resolver hands out.
func Unlimited(cap int) bool { return cap <= 0 }
