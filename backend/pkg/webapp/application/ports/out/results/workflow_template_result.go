package results

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/cronexpr"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// WorkflowTemplateResult represents a workflow template in API responses
type WorkflowTemplateResult struct {
	ID          string                           `json:"id"`
	AccountID   string                           `json:"account_id"`
	Name        string                           `json:"name"`
	Description string                           `json:"description"`
	IsShortcut  bool                             `json:"is_shortcut"`
	Steps       []workflow_template.WorkflowStep `json:"steps"`
	// Graph is the node-graph definition; synthesized from steps for legacy
	// templates so every template opens in the canvas editor
	Graph *workflow_template.WorkflowGraph `json:"graph"`
	// OUID is the LEGACY single-unit field, populated only when the playbook is
	// scoped to exactly ONE unit. A playbook spanning several reports it EMPTY
	// rather than picking one — an old client reading "all nodes" is wrong in
	// the safe direction (it offers the playbook nowhere it is not offered),
	// while naming an arbitrary member would be wrong in the other.
	OUID string `json:"ou_id,omitempty"`
	// OUIDs is the scope: the units this playbook is available to, each with
	// its child units. Empty = every node in the account.
	OUIDs []string `json:"ou_ids"`
	// Status is active or draft
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// ScheduleCron is the trigger's cron expression, empty for a
	// manual-only playbook. It is already in Graph; it is lifted out so a list
	// view does not have to walk the document to answer "is this scheduled".
	ScheduleCron string `json:"schedule_cron,omitempty"`
	// ScheduleDescription and NextRunAt are the SERVER's reading of that
	// expression.
	//
	// They are computed here rather than in the client for the reason
	// POST /schedule/preview exists: cronexpr is the implementation the
	// scheduler itself uses, and a second one in TypeScript would be free to
	// disagree with the thing that actually fires. A playbook whose row
	// promises Tuesday while the scheduler means Wednesday is worse than a row
	// with no schedule column at all.
	//
	// Both stay EMPTY for an expression that is valid but can never fire
	// (`0 0 30 2 *` — February has no 30th). That saves cleanly today, and a
	// row reading "next run: —" beside a schedule is the only place an
	// operator can notice it.
	ScheduleDescription string     `json:"schedule_description,omitempty"`
	NextRunAt           *time.Time `json:"next_run_at,omitempty"`
}

// NewWorkflowTemplateResult creates a new WorkflowTemplateResult from a domain model
func NewWorkflowTemplateResult(template *workflow_template.WorkflowTemplate) *WorkflowTemplateResult {
	result := &WorkflowTemplateResult{
		ID:          template.ID.String(),
		AccountID:   template.AccountID.String(),
		Name:        template.Name,
		Description: template.Description,
		IsShortcut:  template.IsShortcut,
		Steps:       template.Steps,
		Graph:       template.EffectiveGraph(),
		OUID:        ouIDString(template.PrimaryOUID()),
		OUIDs:       ouIDStrings(template.OUIDs),
		Status:      string(template.Status),
		CreatedAt:   template.CreatedAt,
		UpdatedAt:   template.UpdatedAt,
	}
	applySchedule(result, template.EffectiveGraph())
	return result
}

// applySchedule fills the three schedule fields from the graph's trigger.
//
// An expression that no longer compiles leaves all three empty rather than
// failing the response: the row still exists, the operator still has to be able
// to open and fix it, and refusing to LIST a playbook because its schedule is
// unreadable would hide the only thing that can repair it. The save path is
// where a bad cron is refused.
func applySchedule(result *WorkflowTemplateResult, graph *workflow_template.WorkflowGraph) {
	if graph == nil {
		return
	}
	expr := graph.ScheduleCron()
	if expr == "" {
		return
	}
	result.ScheduleCron = expr
	schedule, err := cronexpr.Compile(expr)
	if err != nil {
		return
	}
	result.ScheduleDescription = schedule.Describe()
	if runs := schedule.NextRuns(time.Now().UTC(), 1); len(runs) > 0 {
		next := runs[0]
		result.NextRunAt = &next
	}
}

func ouIDString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// ouIDStrings renders a scope set for the API. Always a list, never null, so a
// client can iterate it without a nil check.
func ouIDStrings(ouIDs []uuid.UUID) []string {
	out := make([]string, 0, len(ouIDs))
	for _, ouID := range ouIDs {
		out = append(out, ouID.String())
	}
	return out
}
