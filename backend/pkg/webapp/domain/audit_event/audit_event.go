package audit_event

import (
	"time"

	"github.com/google/uuid"
)

// ActorKind distinguishes human actors from system-initiated actions
// (scheduler, engine-internal dispatch).
type ActorKind string

const (
	ActorUser   ActorKind = "user"
	ActorSystem ActorKind = "system"
)

// AuditEvent is one append-only record of a privileged action.
//
// WHAT the trail covers is the list of Action* constants in actions.go, not a
// sentence here. This comment used to enumerate the coverage in prose and was
// wrong for months — it described a dozen categories while eight action strings
// in two files were the whole implementation. A reader who needs to know what is
// recorded must be sent somewhere a compiler can keep honest.
type AuditEvent struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	ActorUserID *uuid.UUID // nil for system actors
	ActorKind   ActorKind
	Action      string
	TargetType  string
	TargetID    string
	// Detail is a short, human-readable note about WHICH part of the target
	// changed, for the cases where the target id alone is ambiguous: which key
	// was detached from an entry, which attribute key was set, which run
	// dispatched a command. Optional, and truncated on write.
	//
	// It must NEVER carry credential material — no secret value, no password, no
	// private key, no token. A secret's name and kind are identifiers and are
	// fine; its value is the thing the rest of the system encrypts at rest, and
	// an audit table nobody thought of as a secret store is exactly where it
	// would survive a key rotation unnoticed.
	Detail    string
	CreatedAt time.Time
}

// DetailMaxLen bounds Detail so a caller cannot turn an audit row into a log
// dump. Truncation is silent because a clipped note is still worth having and a
// rejected audit write is not.
const DetailMaxLen = 512

// truncateDetail clips a detail note to DetailMaxLen runes (not bytes — cutting
// mid-rune would store invalid UTF-8).
func truncateDetail(detail string) string {
	runes := []rune(detail)
	if len(runes) <= DetailMaxLen {
		return detail
	}
	return string(runes[:DetailMaxLen]) + "…"
}

// NewUserEvent records an action performed by a user.
func NewUserEvent(accountID, actorUserID uuid.UUID, action, targetType, targetID string) *AuditEvent {
	return &AuditEvent{
		ID:          uuid.New(),
		AccountID:   accountID,
		ActorUserID: &actorUserID,
		ActorKind:   ActorUser,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		CreatedAt:   time.Now(),
	}
}

// NewSystemEvent records an action performed by the system itself.
func NewSystemEvent(accountID uuid.UUID, action, targetType, targetID string) *AuditEvent {
	return &AuditEvent{
		ID:         uuid.New(),
		AccountID:  accountID,
		ActorKind:  ActorSystem,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		CreatedAt:  time.Now(),
	}
}

// WithDetail attaches a truncated detail note and returns the event, so a
// writer stays one expression.
func (e *AuditEvent) WithDetail(detail string) *AuditEvent {
	e.Detail = truncateDetail(detail)
	return e
}
