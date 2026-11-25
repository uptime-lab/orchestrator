package audit_event_list

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// Pagination bounds for the audit events list. The trail is append-only and
// unbounded by default (retention is deliberately off), so the page ceiling is
// what keeps one request from streaming months of history.
const (
	DefaultPerPage = 50
	MaxPerPage     = 200
)

// AuditEventListQuery lists an account's audit events, newest first, with
// optional exact-match filters and pagination.
type AuditEventListQuery struct {
	AuthContext commands.AuthContext
	// Action keeps only events with this exact action string
	// (e.g. "node.delete", "node_command.reboot"); empty = all.
	Action string
	// ActorUserID keeps only one user's events; nil = all actors.
	ActorUserID *uuid.UUID
	Page        int // 1-based
	PerPage     int
}

// Validate normalizes pagination and refuses an unusable auth context.
func (q *AuditEventListQuery) Validate() error {
	if q.AuthContext.AccountID == uuid.Nil {
		return fmt.Errorf("account id is required")
	}
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage <= 0 {
		q.PerPage = DefaultPerPage
	}
	if q.PerPage > MaxPerPage {
		q.PerPage = MaxPerPage
	}
	return nil
}
