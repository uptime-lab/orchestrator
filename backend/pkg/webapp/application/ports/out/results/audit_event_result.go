package results

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// AuditEventResult is one audit trail row in API responses. It mirrors the
// domain event verbatim — the trail is append-only and already contains no
// credential material (audit_event.AuditEvent.Detail), so nothing here is
// redacted.
type AuditEventResult struct {
	ID string `json:"id"`
	// ActorUserID is null for system-initiated actions (scheduler, engine,
	// platform self-heal); ActorKind says which.
	ActorUserID *string   `json:"actor_user_id,omitempty"`
	ActorKind   string    `json:"actor_kind"`
	Action      string    `json:"action"`
	TargetType  string    `json:"target_type"`
	TargetID    string    `json:"target_id"`
	Detail      string    `json:"detail,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// NewAuditEventResult converts a domain audit event.
func NewAuditEventResult(event *audit_event.AuditEvent) *AuditEventResult {
	result := &AuditEventResult{
		ID:         event.ID.String(),
		ActorKind:  string(event.ActorKind),
		Action:     event.Action,
		TargetType: event.TargetType,
		TargetID:   event.TargetID,
		Detail:     event.Detail,
		CreatedAt:  event.CreatedAt,
	}
	if event.ActorUserID != nil {
		id := event.ActorUserID.String()
		result.ActorUserID = &id
	}
	return result
}

// AuditEventListResult is the paged envelope of the audit events list, shaped
// like WorkflowExecutionListResult so paged lists stay one convention.
type AuditEventListResult struct {
	Events  []*AuditEventResult `json:"events"`
	Count   int                 `json:"count"` // rows in this page
	Total   int64               `json:"total"` // rows matching the filter
	Page    int                 `json:"page"`
	PerPage int                 `json:"per_page"`
}

// NewAuditEventListResult builds the paged list envelope.
func NewAuditEventListResult(events []audit_event.AuditEvent, total int64, page, perPage int) *AuditEventListResult {
	items := make([]*AuditEventResult, 0, len(events))
	for i := range events {
		items = append(items, NewAuditEventResult(&events[i]))
	}
	return &AuditEventListResult{
		Events:  items,
		Count:   len(items),
		Total:   total,
		Page:    page,
		PerPage: perPage,
	}
}
