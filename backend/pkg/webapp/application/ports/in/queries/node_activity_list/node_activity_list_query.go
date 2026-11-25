// Package node_activity_list carries the fleet activity log's read query.
package node_activity_list

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// Pagination bounds, matching the audit trail and the run list so a client
// pages all three the same way.
const (
	DefaultPerPage = 50
	MaxPerPage     = 200
)

// NodeActivityListQuery asks for one page of the account's device commands.
type NodeActivityListQuery struct {
	AuthContext commands.AuthContext
	// VisibleNodeIDs is the caller's authz visibility, resolved by the handler
	// before the query is built. Nil means account-wide; an EMPTY slice means
	// the caller may see no node and must get nothing back. The two are not
	// the same and must not be collapsed.
	VisibleNodeIDs []uuid.UUID
	// Statuses filters on the command lifecycle. Empty matches all.
	Statuses []string
	// Slug filters on one command kind. Empty matches all.
	Slug    string
	Since   *time.Time
	Until   *time.Time
	Page    int // 1-based
	PerPage int
}

// Validate normalizes pagination and refuses input that would silently widen
// the result.
//
// An unrecognised status is an ERROR, not an ignored filter: a client that
// asked for failures and got everything back would read the whole log as
// failed. The same reasoning the cron parser uses for an out-of-range field.
func (q *NodeActivityListQuery) Validate() error {
	if q.AuthContext.AccountID == uuid.Nil {
		return fmt.Errorf("account id is required")
	}
	for _, s := range q.Statuses {
		if !node_command.IsKnownStatus(s) {
			return fmt.Errorf("unknown status %q", s)
		}
	}
	if q.Since != nil && q.Until != nil && q.Until.Before(*q.Since) {
		return fmt.Errorf("until must not be before since")
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
