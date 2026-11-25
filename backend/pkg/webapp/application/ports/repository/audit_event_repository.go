package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// AuditEventListFilter narrows the audit read route. Empty/nil fields mean
// "no filter". Both are equality matches — cheap against the account index —
// and free-text search is deliberately absent.
type AuditEventListFilter struct {
	// Action is the exact action string (e.g. "node.delete",
	// "node_command.reboot"); "" matches all.
	Action string
	// ActorUserID keeps only one user's events; nil matches all actors.
	ActorUserID *uuid.UUID
}

// AuditEventRepository persists the append-only audit log. Events are never
// updated, and are deleted only by the age-based retention sweep
// (AuditRetentionRepository) — never
// through any request path, so nothing a user or an operator does through the
// API can erase what they did.
type AuditEventRepository interface {
	// Create appends an audit event.
	Create(ctx context.Context, event *audit_event.AuditEvent) error
	// List returns a page of an account's audit events, newest first, plus the
	// total matching the filter (for pagination). Filters are exact matches
	// only — the read route is an oversight tool, not a search engine.
	List(ctx context.Context, accountID uuid.UUID, filter AuditEventListFilter, offset, limit int) ([]audit_event.AuditEvent, int64, error)
	// CountByAccountAndActionsSince counts an account's events with any of the
	// given actions since a cutoff.
	//
	// This exists so a rate limit can be counted off the trail rather than off
	// the working table it bounds. Invites are the case: an invite row is
	// deleted by revoking or by re-sending, so a cap counted on account_invites
	// is defeated by a revoke-and-retry loop, while an audit event is written by
	// no path that can also remove it.
	CountByAccountAndActionsSince(ctx context.Context, accountID uuid.UUID, actions []string, since time.Time) (int64, error)
}

// AuditRetentionOverride is one account's own audit retention as stored
// (accounts.audit_retention_days), before the floor and ceiling apply.
type AuditRetentionOverride struct {
	AccountID uuid.UUID
	Days      int
}

// AuditRetentionRepository is the retention sweep's view of the audit log:
// the only deletion path for audit_events, reachable from no request handler.
// Every delete removes at most limit rows of one account per call, so a first
// sweep over years of history is many short statements, never one long lock,
// and no statement reads rows it cannot delete.
type AuditRetentionRepository interface {
	// ListAuditRetentionOverrides returns every account whose own retention
	// is set (non-NULL).
	ListAuditRetentionOverrides(ctx context.Context) ([]AuditRetentionOverride, error)
	// ListInstallDefaultAuditAccounts returns the account ids the install
	// default governs: every account without its own retention, and every id
	// the log carries whose account no longer exists.
	ListInstallDefaultAuditAccounts(ctx context.Context) ([]uuid.UUID, error)
	// DeleteAccountOlderThanAtInstallDefault removes one such account's events
	// older than cutoff, only while the account still has no retention of its
	// own, so an account given one since it was listed is never swept at the
	// install default.
	DeleteAccountOlderThanAtInstallDefault(ctx context.Context, accountID uuid.UUID, cutoff time.Time, limit int) (int64, error)
	// DeleteAccountOlderThan removes one account's events older than cutoff,
	// only while that account's stored retention is still override.Days, so
	// a value changed since it was listed is never applied with the old one.
	DeleteAccountOlderThan(ctx context.Context, override AuditRetentionOverride, cutoff time.Time, limit int) (int64, error)
}
