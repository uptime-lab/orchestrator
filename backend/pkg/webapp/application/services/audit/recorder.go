// Package audit is the one way a service appends to the audit trail.
//
// Before it, the only writer was a private `audit()` helper on the member
// service. Every other service that should have been recording — access entries,
// secrets, node deletes, terminal opens, device commands — recorded nothing,
// because adding a writer meant copying that helper along with its repository
// field, its nil handling and its logging convention. A shared Recorder makes a
// writer one line, which is the difference between "we should audit that" and a
// row in the table.
package audit

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// Actor is who performed an action. The account is required — an event with no
// tenant cannot be read back, because every read is account-scoped — and a zero
// UserID means the platform acted on its own behalf (the workflow engine, the
// cron scheduler, a reconnect handler pushing config).
//
// Explicit rather than pulled from the context on purpose. The request scope in
// infrastructure/logging already carries account and user ids, and reading them
// there would have saved threading an actor through a dozen signatures — but
// that scope exists to decorate log lines and is documented as never being the
// thing that fails. An audit row silently attributed to "system" because a
// context value was missing is worse than a compile error, and the compiler is
// what a parameter buys.
type Actor struct {
	AccountID uuid.UUID
	UserID    uuid.UUID
}

// UserActor is the actor for an authenticated request.
func UserActor(accountID, userID uuid.UUID) Actor {
	return Actor{AccountID: accountID, UserID: userID}
}

// SystemActor is the actor for a platform-initiated action with no human caller.
func SystemActor(accountID uuid.UUID) Actor {
	return Actor{AccountID: accountID}
}

// isSystem reports whether the action had no human behind it.
func (a Actor) isSystem() bool { return a.UserID == uuid.Nil }

// Recorder appends audit events.
//
// Consumers hold the concrete *Recorder rather than an interface, deliberately.
// There is no alternative implementation to swap — the policy (best-effort,
// account-scoped, actor-explicit) is the whole of it — and a test that injects a
// fake AuditEventRepository into a real Recorder exercises the actor→event
// mapping, which a fake Recorder would stub out and stop checking.
type Recorder struct {
	repo   repository.AuditEventRepository
	logger *slog.Logger
}

// NewRecorder wires the recorder. The repository is required: a Recorder that
// silently discards is the state this package exists to end, so a missing
// dependency panics at composition time rather than at the first privileged
// action.
func NewRecorder(repo repository.AuditEventRepository, logger *slog.Logger) *Recorder {
	if repo == nil {
		panic("audit.Recorder requires an audit event repository")
	}
	if logger == nil {
		panic("audit.Recorder requires a logger")
	}
	return &Recorder{repo: repo, logger: logger.With("service", "audit")}
}

// Record appends an event. It never returns an error and never blocks the
// caller's outcome: the action being recorded has already happened, and failing
// a member removal because its audit row could not be written would turn a
// bookkeeping fault into an operational one. A failure is logged with the
// request scope attached (WarnContext), which is the signal an operator gets.
//
// The one deliberate exception in the codebase is the terms acknowledgement
// in user_service.go: there the audit row IS the consent record, so its write is
// checked and does fail the request. That service therefore calls the repository
// directly, not this method — if you find yourself wanting Record to return an
// error, you want that pattern instead.
func (r *Recorder) Record(ctx context.Context, actor Actor, action, targetType, targetID string) {
	r.RecordDetail(ctx, actor, action, targetType, targetID, "")
}

// RecordDetail appends an event carrying a short note about which part of the
// target changed. The note must never contain credential material — see
// audit_event.AuditEvent.Detail.
func (r *Recorder) RecordDetail(ctx context.Context, actor Actor, action, targetType, targetID, detail string) {
	if actor.AccountID == uuid.Nil {
		// Dropped rather than stored: reads filter on account_id, so a tenantless
		// row is invisible to every consumer while still looking like coverage in
		// a row count. Loud, because it means a caller has an actor it did not
		// populate.
		r.logger.ErrorContext(ctx, "dropping audit event with no account",
			"action", action, "target_type", targetType, "target_id", targetID)
		return
	}

	var event *audit_event.AuditEvent
	if actor.isSystem() {
		event = audit_event.NewSystemEvent(actor.AccountID, action, targetType, targetID)
	} else {
		event = audit_event.NewUserEvent(actor.AccountID, actor.UserID, action, targetType, targetID)
	}
	if detail != "" {
		event = event.WithDetail(detail)
	}

	if err := r.repo.Create(ctx, event); err != nil {
		r.logger.WarnContext(ctx, "failed to write audit event",
			"action", action, "target_type", targetType, "target_id", targetID, "error", err)
	}
}
