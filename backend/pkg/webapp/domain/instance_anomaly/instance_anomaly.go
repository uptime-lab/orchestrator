// Package instance_anomaly models operational conditions that the person
// running the INSTANCE needs to see, as opposed to anything a tenant can act
// on themselves.
//
// The motivating case: a cross-account hardware-serial conflict. The guard that
// refuses it is doing its job, but a refusal that reaches only a log line is
// found by reading logs after a user complains. An anomaly is the durable,
// reviewable form of that signal.
//
// Two properties matter more than the schema:
//
//   - Deduplication. A refused device retries every second or two, so an
//     append-only log would be thousands of rows an hour and unreadable. One
//     row per distinct condition, with a counter and a last-seen stamp.
//   - Acknowledgement. An operator who has dealt with something needs it to
//     stop demanding attention without losing the record that it happened.
package instance_anomaly

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// Kind identifies what was detected. Kinds are stable strings because they are
// stored, filtered on, and used to decide who gets notified.
type Kind string

const (
	// KindSerialBindingConflict: a device tried to adopt into one account while
	// its hardware serial is registered to another.
	KindSerialBindingConflict Kind = "serial_binding_conflict"
	// KindSerialDuplicated: the same serial exists in two accounts at once —
	// the guard allows this when one of them is the requesting account, and
	// flags it for manual de-duplication.
	KindSerialDuplicated Kind = "serial_duplicated"
	// KindHostDiskLow / KindHostCPULoad are recorded by an operator console
	// outside this tree, never by the backend.
	KindHostDiskLow Kind = "host_disk_low"
	KindHostCPULoad Kind = "host_cpu_load"
	// KindAuthFailureBurst: one client has failed authentication many times in
	// a short window — the shape of someone working through a credential list.
	// Recorded by the backend, since it is the only side that sees the attempts.
	KindAuthFailureBurst Kind = "auth_failure_burst"
	// KindSessionGrowth: one user's logins are creating sessions faster than
	// they are used, so a single sign-in had to evict several at once. The
	// benign reading is a client looping on login; the other is one credential
	// being used from several places.
	KindSessionGrowth Kind = "session_growth"
	// KindAdoptionRejectionBurst: one device is failing the gRPC handshake over
	// and over. Deliberately NOT folded into KindAuthFailureBurst, whose wording
	// is "someone working through a credential list": the overwhelmingly common
	// cause here is a device holding a master token that expired or was revoked,
	// and describing the operator's own node as an attacker is a false alarm
	// that teaches the operator to ignore the real one.
	//
	// It earns a kind of its own because the failure is INVISIBLE otherwise. The
	// device cannot report it (it is not authenticated, so it has no node to
	// report as), the agent retries on a ~1s loop forever, and the only symptom
	// is a log line per attempt — which at ~50/min becomes the log, drowning the
	// evidence any other incident would be diagnosed from.
	KindAdoptionRejectionBurst Kind = "adoption_rejection_burst"
	// KindScheduledPlaybookSkipped: the cron scheduler deliberately did not fire
	// a playbook that was due. Every one of these used to be a bare log line, so
	// the observable behaviour was "the playbook stopped running and nobody was
	// told" — which is worse than a failure, because a failed run at least
	// leaves an execution row to find.
	//
	// One kind rather than five, with the specific cause in Details: to an
	// operator they are the same question ("why did this not run?"), and five
	// kinds would need five registry entries and five notification rules to
	// answer it. The permanent causes (cap exceeded, unknown author) and the
	// transient ones (no target nodes) differ in severity, not in kind.
	KindScheduledPlaybookSkipped Kind = "scheduled_playbook_skipped"
	// KindMasterTokenIdentityMismatch: a master-token handshake named the serial
	// of an ADMITTED node in the token's own account, but the rest of the
	// fingerprint did not match the stored one, or the node was holding a live
	// stream at the time. The connection was refused and the stored fingerprint
	// left alone: accepting it would hand the node's permanent token to whoever
	// held the master token and the serial, and evict the real device.
	//
	// Deliberately NOT serial_binding_conflict: that kind is projected to the
	// tenant by GET /nodes/adoption-refusals with a cross-account meaning
	// ("another account owns this serial"), and this is the opposite shape —
	// same account, wrong device. Account-scoped, because the benign reading is
	// an operator who swapped a board and needs to Delete and Restore the node.
	KindMasterTokenIdentityMismatch Kind = "master_token_identity_mismatch"
)

// Class separates the two kinds of thing this table holds, because they behave
// differently and conflating them is what produces both chronic alert spam and
// useless "back to normal" mail.
//
//   - A CONDITION has a truth value right now (disk full, database unreachable)
//     and can therefore resolve.
//   - An EVENT happened (a device was refused). It never resolves; it ages out.
type Class string

const (
	ClassCondition Class = "condition"
	ClassEvent     Class = "event"
)

// State is where a condition sits in its lifecycle. Events are recorded
// directly as StateFiring and never leave it.
//
// Notification is tied to the TRANSITION into firing, never to the existence of
// a row — that distinction is what stops a value oscillating around a threshold
// from mailing on every crossing forever.
type State string

const (
	// StatePending: the predicate is true but has not held long enough yet.
	StatePending State = "pending"
	// StateFiring: it held. This is the state that notifies, once.
	StateFiring State = "firing"
	// StateResolved: the predicate has been false for the clear duration. Kept
	// rather than deleted — "the disk was full last Tuesday" is exactly what an
	// operator looks for afterwards.
	StateResolved State = "resolved"
)

// Severity orders anomalies for display and for notification thresholds.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Anomaly is one distinct condition, however many times it has occurred.
type Anomaly struct {
	ID   uuid.UUID
	Kind Kind
	// DedupeKey collapses repeats of the SAME condition into one row. Two
	// occurrences share a key exactly when an operator would consider them the
	// same problem.
	DedupeKey string
	Severity  Severity
	// Summary is a human sentence, written at detection time where the context
	// is known rather than reassembled in the UI.
	Summary string
	// Details carries the structured specifics (serials, account ids, measured
	// values) for an operator who needs to act.
	Details map[string]interface{}
	// Class and State drive the lifecycle; see their type docs.
	Class Class
	State State
	// StateSince stamps the last transition, so "firing for 20 minutes" is
	// answerable without inferring it from FirstSeenAt — which never resets and
	// is therefore permanently stale for anything that has ever flapped.
	StateSince time.Time
	// AccountID is the tenant the anomaly concerns, when it concerns one.
	AccountID       *uuid.UUID
	OccurrenceCount int64
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	// AcknowledgedAt silences the anomaly without deleting it.
	//
	// It survives recurrence on purpose. Clearing it on every re-detection
	// meant acknowledging a chronic problem bought exactly one tick of silence
	// — the refused device re-fires every second or two, and a disk sitting at
	// its threshold re-detects every minute. A condition that genuinely goes
	// away and comes back raises a NEW cycle (resolve then fire), which is the
	// signal worth un-silencing on.
	AcknowledgedAt *time.Time
	// NotifiedAt records when an alert was last sent, so a retry storm cannot
	// turn into a mail storm.
	NotifiedAt *time.Time
}

// New builds a first occurrence.
func New(kind Kind, dedupeKey string, severity Severity, summary string, details map[string]interface{}, accountID *uuid.UUID) *Anomaly {
	return NewWithClass(kind, dedupeKey, severity, summary, details, accountID, ClassEvent, StateFiring)
}

// NewWithClass builds an occurrence with an explicit lifecycle position.
func NewWithClass(kind Kind, dedupeKey string, severity Severity, summary string, details map[string]interface{}, accountID *uuid.UUID, class Class, state State) *Anomaly {
	now := time.Now()
	return &Anomaly{
		Class:           class,
		State:           state,
		StateSince:      now,
		ID:              uuid.New(),
		Kind:            kind,
		DedupeKey:       dedupeKey,
		Severity:        severity,
		Summary:         summary,
		Details:         details,
		AccountID:       accountID,
		OccurrenceCount: 1,
		FirstSeenAt:     now,
		LastSeenAt:      now,
	}
}

// SerialConflictKey identifies "this serial, refused for this account, because
// that account owns it".
//
// Hashed so a serial — which identifies a customer's physical hardware — is not
// sitting in an index that the whole operator surface reads. The readable value
// is still in Details for whoever is actually investigating.
func SerialConflictKey(serial string, requestingAccount, ownerAccount uuid.UUID) string {
	sum := sha256.Sum256([]byte(string(KindSerialBindingConflict) + "|" + serial + "|" +
		requestingAccount.String() + "|" + ownerAccount.String()))
	return hex.EncodeToString(sum[:])
}

// MasterTokenIdentityMismatchKey identifies "this node, claimed by a device
// that is not it" — one row per node however many times the claim is retried,
// with the occurrence count carrying the rest. Hashed like the serial key: the
// node id is a customer identifier and this index is read across the whole
// operator surface.
func MasterTokenIdentityMismatchKey(accountID, nodeID uuid.UUID) string {
	sum := sha256.Sum256([]byte(string(KindMasterTokenIdentityMismatch) + "|" + accountID.String() + "|" + nodeID.String()))
	return hex.EncodeToString(sum[:])
}

// SessionGrowthKey identifies runaway session creation for ONE user, so a
// client stuck in a login loop collapses into a single row with a rising
// occurrence count instead of a row per login.
//
// Hashed like the serial key: the user id is a customer identifier and this
// index is read across the whole operator surface. Details carries the readable
// value for whoever investigates.
func SessionGrowthKey(userID uuid.UUID) string {
	sum := sha256.Sum256([]byte(string(KindSessionGrowth) + "|" + userID.String()))
	return hex.EncodeToString(sum[:])
}

// AuthFailureBurstKey identifies sustained credential failures from ONE client,
// so an attacker's thousands of attempts collapse into a single row whose
// occurrence count is the interesting number.
//
// Hashed like the others: this index is read across the whole operator surface,
// and a source address is personal data when the source is a person's home
// connection. The readable value stays in Details for whoever is investigating,
// which is the point at which looking is justified.
func AuthFailureBurstKey(sourceIP string) string {
	sum := sha256.Sum256([]byte(string(KindAuthFailureBurst) + "|" + sourceIP))
	return hex.EncodeToString(sum[:])
}

// AdoptionRejectionBurstKey identifies one device failing the handshake, so a
// looping device's thousands of attempts collapse into the one problem they are.
//
// The subject is the hardware serial where the device sent one, and its source
// address otherwise. Preferring the serial matters on any install where gRPC is
// TLS-terminated at a proxy: there every agent shares the proxy's address, so an
// address key would merge unrelated devices and answer "something is looping"
// without answering which. A rejected peer has no node id and no account — that
// is what makes it invisible to everything else — so these two are the only
// identifiers that exist at this point.
//
// Hashed like the others: this index is read across the whole operator surface,
// and the readable value stays in Details for whoever is investigating.
func AdoptionRejectionBurstKey(subject string) string {
	sum := sha256.Sum256([]byte(string(KindAdoptionRejectionBurst) + "|" + subject))
	return hex.EncodeToString(sum[:])
}

// ScheduledPlaybookSkipKey identifies "this playbook, not firing for this
// reason". One row per playbook per cause, not per tick: a playbook over the
// per-account cron cap is skipped every single minute, and a row per skip would
// bury the operator surface under one tenant's misconfiguration within a day.
//
// The reason is part of the key on purpose. A playbook that stops firing
// because its author was demoted and then also loses its last target node has
// had two different things go wrong, and collapsing them would let the second
// hide behind the first's acknowledgement.
//
// Not hashed, unlike the serial keys: a template id and a cause name are
// already opaque identifiers, and keeping them readable makes the row
// self-explanatory in the console without a lookup.
func ScheduledPlaybookSkipKey(templateID uuid.UUID, reason string) string {
	return string(KindScheduledPlaybookSkipped) + "|" + templateID.String() + "|" + reason
}

// HostConditionKey identifies a host-level condition. One row per condition:
// a disk that has been over its threshold for a week is one problem, not a
// week's worth of them.
func HostConditionKey(kind Kind) string {
	return string(kind)
}
