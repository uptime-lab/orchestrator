package workflow_template

import (
	"fmt"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/google/uuid"
)

// CommandSpec represents a command specification (reuses node command structure)
type CommandSpec struct {
	Slug    string                 `json:"slug"`    // Command identifier like "reboot", "install_os", etc.
	Payload map[string]interface{} `json:"payload"` // Command parameters as JSON
}

// ToNodeCommand converts CommandSpec to node command fields
func (cs *CommandSpec) ToNodeCommand() (slug string, payload map[string]interface{}) {
	return cs.Slug, cs.Payload
}

// WorkflowStep represents a single step in a workflow
type WorkflowStep struct {
	Order       int         `json:"order"`       // Step order (0-indexed, sequential)
	Command     CommandSpec `json:"command"`     // Command specification (reuses node command structure)
	Name        string      `json:"name"`        // Optional human-readable step name
	Description string      `json:"description"` // Optional step description
}

// WorkflowTemplateStatus separates runnable playbooks from drafts.
type WorkflowTemplateStatus string

const (
	// StatusActive playbooks are offered in run pickers.
	StatusActive WorkflowTemplateStatus = "active"
	// StatusDraft playbooks are hidden from run pickers (work in progress).
	StatusDraft WorkflowTemplateStatus = "draft"
)

// WorkflowTemplate represents a reusable workflow template
type WorkflowTemplate struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	Name        string
	Description string
	IsShortcut  bool
	Steps       []WorkflowStep
	Graph       *WorkflowGraph // node-graph definition; nil for legacy step-only templates
	// OUIDs scope the template to a SET of organizational units (each unit and
	// its children); empty means the template is available for all nodes of the
	// account. A playbook that applies to two units is the ordinary case — the
	// same reasoning that gave access entries a set: a unit is an organizational
	// fact, and one procedure routinely belongs to several.
	//
	// The legacy single `ou_id` column is kept in the database, backfilled once
	// into the join table, and never read again (mirroring access_entry_ous).
	OUIDs  []uuid.UUID
	Status WorkflowTemplateStatus
	// AuthorID is the user who last passed the authoring gate for this content
	// (stamped on create and on every update). The cron scheduler re-checks
	// that author's privileged-authoring right at fire time, so a playbook
	// authored by a since-demoted admin stops firing on its own. Nil on legacy
	// rows written before the column existed.
	AuthorID  *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScheduledCandidate is the projection the cron scheduler scans each minute:
// just enough of a template to decide whether it is due and to report a skip.
//
// Cron here is the denormalized schedule_cron column, which is what makes the
// scan cheap. It is a CANDIDATE for exactly that reason — the graph remains the
// source of truth, so a template that matches on this value is re-checked
// against its graph before it fires.
type ScheduledCandidate struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	Name      string
	Cron      string
}

// NewWorkflowTemplate creates a new workflow template
func NewWorkflowTemplate(accountID uuid.UUID, name, description string, isShortcut bool, steps []WorkflowStep) *WorkflowTemplate {
	now := time.Now()
	return &WorkflowTemplate{
		ID:          uuid.New(),
		AccountID:   accountID,
		Name:        name,
		Description: description,
		IsShortcut:  isShortcut,
		Steps:       steps,
		Status:      StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// Normalize validates the graph (when present) and derives the executable
// step list from it, so graph and steps can never drift apart. Call before
// Validate when accepting client input. Drafts get the relaxed param check
// (structure always holds; required params may be blank until activation).
func (wt *WorkflowTemplate) Normalize() error {
	if wt.Graph == nil {
		return nil
	}
	var err error
	if wt.Status == StatusDraft {
		err = wt.Graph.ValidateDraft()
	} else {
		err = wt.Graph.Validate()
	}
	if err != nil {
		return fmt.Errorf("invalid graph: %w", err)
	}
	steps, err := wt.Graph.CommandSteps()
	if err != nil {
		return fmt.Errorf("invalid graph: %w", err)
	}
	wt.Steps = steps
	return nil
}

// IsScoped reports whether the template is restricted to particular units.
func (wt *WorkflowTemplate) IsScoped() bool { return len(wt.OUIDs) > 0 }

// PrimaryOUID returns a single representative unit, or nil when the template is
// unscoped OR spans several units.
//
// It exists for the two places that genuinely hold ONE unit: the OU an
// execution is pinned to for live-update visibility, and the target of an
// authorization check made against a single unit. Both must be conservative
// when a playbook spans several — a nil scope reaches only account-wide
// holders (narrower), never everyone.
func (wt *WorkflowTemplate) PrimaryOUID() *uuid.UUID {
	if len(wt.OUIDs) != 1 {
		return nil
	}
	ou := wt.OUIDs[0]
	return &ou
}

// EffectiveGraph returns the stored graph, or one synthesized from the step
// list for legacy templates, so every template renders in the canvas editor.
func (wt *WorkflowTemplate) EffectiveGraph() *WorkflowGraph {
	if wt.Graph != nil {
		return wt.Graph
	}
	return GraphFromSteps(wt.Steps)
}

// BlockTypes returns the set of block types the template's graph contains
// (deduplicated), used by the authorization layer to decide whether running
// or authoring the template is a privileged action.
func (wt *WorkflowTemplate) BlockTypes() []string {
	graph := wt.EffectiveGraph()
	if graph == nil {
		return nil
	}
	seen := make(map[string]bool)
	out := make([]string, 0, len(graph.Nodes))
	for _, n := range graph.Nodes {
		if !seen[n.Type] {
			seen[n.Type] = true
			out = append(out, n.Type)
		}
	}
	return out
}

// WithdrawnBlockCount counts the blocks of the template's graph that are no
// longer offered (workflow_block.BlockDefinition.Withdrawn). A save may keep
// the ones a stored playbook already has, never add one.
func (wt *WorkflowTemplate) WithdrawnBlockCount() int {
	graph := wt.EffectiveGraph()
	if graph == nil {
		return 0
	}
	count := 0
	for _, n := range graph.Nodes {
		if block, known := workflow_block.FindByType(n.Type); known && block.Withdrawn {
			count++
		}
	}
	return count
}

// Snapshot captures the template's executable definition for pinning into an
// execution: in-flight runs keep using it even if the template is edited.
func (wt *WorkflowTemplate) Snapshot() *TemplateSnapshot {
	return &TemplateSnapshot{
		TemplateID:  wt.ID,
		Name:        wt.Name,
		Description: wt.Description,
		Steps:       wt.Steps,
		Graph:       wt.EffectiveGraph(),
		AuthorID:    wt.AuthorID,
		TakenAt:     time.Now(),
	}
}

// TemplateSnapshot is a frozen copy of a template taken when an execution
// starts. Stored on the execution and used by the engine instead of the
// live template.
type TemplateSnapshot struct {
	TemplateID  uuid.UUID      `json:"template_id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Steps       []WorkflowStep `json:"steps"`
	Graph       *WorkflowGraph `json:"graph,omitempty"`
	// AuthorID is the template's author when the run started: the member a
	// SCHEDULED run acts for (the scheduler fires only while that author still
	// holds the right the content needs), named on the audit records the run
	// writes. Nil for templates with no recorded author.
	AuthorID *uuid.UUID `json:"author_id,omitempty"`
	TakenAt  time.Time  `json:"taken_at"`
}

// ToTemplate builds a transient template from the snapshot for code paths
// that operate on templates (e.g. the execution engine).
func (snap *TemplateSnapshot) ToTemplate(accountID uuid.UUID) *WorkflowTemplate {
	return &WorkflowTemplate{
		ID:          snap.TemplateID,
		AccountID:   accountID,
		Name:        snap.Name,
		Description: snap.Description,
		Steps:       snap.Steps,
		Graph:       snap.Graph,
		AuthorID:    snap.AuthorID,
	}
}

// Validate validates the workflow template
func (wt *WorkflowTemplate) Validate() error {
	if wt.Name == "" {
		return fmt.Errorf("workflow template name is required")
	}

	if wt.Status != StatusActive && wt.Status != StatusDraft {
		return fmt.Errorf("invalid status '%s' (must be active or draft)", wt.Status)
	}

	// With a graph, the graph is the source of truth (validated in Normalize)
	// and may legitimately contain only server-executed blocks, so an empty
	// derived step list is fine. Without a graph, the flat step list must be
	// non-empty as before.
	if wt.Graph == nil && len(wt.Steps) == 0 {
		return fmt.Errorf("workflow template must have at least one step")
	}

	// A document carrying the redaction placeholder is a redacted READ being
	// saved back: the caller opened the playbook without secret.read_value,
	// the API hid the free-text values, and storing the placeholder would
	// silently overwrite the real script/header/url. Refused for drafts too —
	// a blanked draft is still a blanked document.
	if wt.Graph.ContainsRedactionPlaceholder() || StepsContainRedactionPlaceholder(wt.Steps) {
		return fmt.Errorf("a parameter still carries the redaction placeholder %q — the values were hidden because your role lacks secret.read_value; re-enter them before saving", RedactedParamPlaceholder)
	}

	// Track seen orders to ensure uniqueness
	seenOrders := make(map[int]bool)

	for i, step := range wt.Steps {
		// Check if command is properly initialized
		if step.Command.Slug == "" {
			return fmt.Errorf("step %d: command.slug is required (step order=%d, name='%s', command=%+v)", i, step.Order, step.Name, step.Command)
		}

		// Slug and payload are validated against the block registry so an
		// invalid step is rejected at save time, not at execution time.
		// Drafts only type-check what is filled in (mirrors Normalize).
		block, known := workflow_block.FindBySlug(step.Command.Slug)
		if !known {
			return fmt.Errorf("step %d: invalid command.slug '%s'", i, step.Command.Slug)
		}
		stepErr := block.ValidateParamTypes(step.Command.Payload)
		if wt.Status != StatusDraft {
			stepErr = block.ValidateParams(step.Command.Payload)
		}
		if stepErr != nil {
			return fmt.Errorf("step %d (%s): %w", i, step.Command.Slug, stepErr)
		}

		if step.Order < 0 {
			return fmt.Errorf("step %d: order must be non-negative (got %d)", i, step.Order)
		}

		if seenOrders[step.Order] {
			return fmt.Errorf("step %d: duplicate order %d", i, step.Order)
		}
		seenOrders[step.Order] = true
	}

	return nil
}
