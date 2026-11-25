package results

import (
	"github.com/google/uuid"

	utility_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// UtilityDefinitionResult is one catalog entry. Params are served in the
// workflow-block shape so the frontend renders them with the same generated
// forms (CommandInputs).
type UtilityDefinitionResult struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Description     string                     `json:"description,omitempty"`
	Details         string                     `json:"details,omitempty"`
	Class           string                     `json:"class"`
	DependsOn       []string                   `json:"depends_on,omitempty"`
	ConflictsWith   []string                   `json:"conflicts_with,omitempty"`
	RequiresReboot  bool                       `json:"requires_reboot"`
	MinAgentVersion string                     `json:"min_agent_version,omitempty"`
	Params          []WorkflowBlockParamResult `json:"params"`
}

// NewUtilityDefinitionResults converts catalog definitions to API results.
func NewUtilityDefinitionResults(defs []utility.Definition) []UtilityDefinitionResult {
	out := make([]UtilityDefinitionResult, 0, len(defs))
	for _, d := range defs {
		params := make([]WorkflowBlockParamResult, 0, len(d.Params))
		for _, p := range d.Params {
			params = append(params, WorkflowBlockParamResult{
				Name:        p.Name,
				Label:       p.Label,
				Description: p.Description,
				Details:     p.Details,
				Type:        string(p.Type),
				Required:    p.Required,
				Default:     p.Default,
				EnumValues:  p.EnumValues,
				Placeholder: p.Placeholder,
				Min:         p.Min,
				Max:         p.Max,
			})
		}
		out = append(out, UtilityDefinitionResult{
			ID:              d.ID,
			Name:            d.Name,
			Description:     d.Description,
			Details:         d.Details,
			Class:           string(d.Class),
			DependsOn:       d.DependsOn,
			ConflictsWith:   d.ConflictsWith,
			RequiresReboot:  d.RequiresReboot,
			MinAgentVersion: d.MinAgentVersion,
			Params:          params,
		})
	}
	return out
}

// UtilityAssignmentResult is one assignment row. OUIDs is the unit set; the
// singular ou_id stays populated with the FIRST unit for one release so an
// older client still sees a scope (the playbook-export precedent).
type UtilityAssignmentResult struct {
	ID          uuid.UUID              `json:"id"`
	UtilityID   string                 `json:"utility_id"`
	Scope       string                 `json:"scope"` // account | ou | node | unassigned
	OUIDs       []uuid.UUID            `json:"ou_ids,omitempty"`
	OUID        *uuid.UUID             `json:"ou_id,omitempty"`
	NodeID      *uuid.UUID             `json:"node_id,omitempty"`
	AccountWide bool                   `json:"account_wide"`
	Config      map[string]interface{} `json:"config,omitempty"`
	Enabled     bool                   `json:"enabled"`
	CreatedAt   string                 `json:"created_at"`
	UpdatedAt   string                 `json:"updated_at"`
}

// NewUtilityAssignmentResult converts one assignment.
func NewUtilityAssignmentResult(a *utility.Assignment) UtilityAssignmentResult {
	res := UtilityAssignmentResult{
		ID:          a.ID,
		UtilityID:   a.UtilityID,
		Scope:       string(a.Scope()),
		OUIDs:       a.OUIDs,
		NodeID:      a.NodeID,
		AccountWide: a.AccountWide,
		Config:      a.Config,
		Enabled:     a.Enabled,
		CreatedAt:   a.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:   a.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if len(a.OUIDs) > 0 {
		first := a.OUIDs[0]
		res.OUID = &first
	}
	return res
}

// NewUtilityAssignmentResults converts a list.
func NewUtilityAssignmentResults(list []utility.Assignment) []UtilityAssignmentResult {
	out := make([]UtilityAssignmentResult, 0, len(list))
	for i := range list {
		out = append(out, NewUtilityAssignmentResult(&list[i]))
	}
	return out
}

// EffectiveUtilityResult is one utility of a node's effective set with its
// ladder rung and provenance.
type EffectiveUtilityResult struct {
	UtilityID    string                 `json:"utility_id"`
	Name         string                 `json:"name"`
	Class        string                 `json:"class"`
	AssignmentID uuid.UUID              `json:"assignment_id"`
	Source       string                 `json:"source"` // node | ou | account
	SourceOUID   *uuid.UUID             `json:"source_ou_id,omitempty"`
	ImpliedBy    []string               `json:"implied_by,omitempty"` // set when only a depends_on brought it in; no assignment row then
	Config       map[string]interface{} `json:"config,omitempty"`
	DesiredHash  string                 `json:"desired_hash"`
	AppliedHash  string                 `json:"applied_hash,omitempty"`
	Status       string                 `json:"status"` // declared | update_pending | applied | failed
	LastError    string                 `json:"last_error,omitempty"`
}

// NewEffectiveUtilityResults merges the resolved set with the stored ladder.
// A resolved utility with no stored rung reads as declared; a stored rung for
// a utility that left the set is omitted (the ladder prune owns it).
func NewEffectiveUtilityResults(state *utility_service.EffectiveState) []EffectiveUtilityResult {
	rungs := make(map[string]utility.NodeState, len(state.States))
	for _, s := range state.States {
		rungs[s.UtilityID] = s
	}
	out := make([]EffectiveUtilityResult, 0, len(state.Resolved))
	for _, r := range state.Resolved {
		res := EffectiveUtilityResult{
			UtilityID:    r.Definition.ID,
			Name:         r.Definition.Name,
			Class:        string(r.Definition.Class),
			AssignmentID: r.AssignmentID,
			Source:       string(r.Source),
			SourceOUID:   r.SourceOUID,
			ImpliedBy:    r.ImpliedBy,
			Config:       r.Config,
			DesiredHash:  r.ConfigHash,
			Status:       string(utility.StatusDeclared),
		}
		if rung, ok := rungs[r.Definition.ID]; ok {
			// The stored desired hash may lag the freshly resolved one; the
			// status must compare against what the platform wants NOW.
			derived := utility.NodeState{
				DesiredHash: r.ConfigHash,
				AppliedHash: rung.AppliedHash,
				LastError:   rung.LastError,
			}
			res.AppliedHash = rung.AppliedHash
			res.LastError = rung.LastError
			res.Status = string(derived.Status())
		}
		out = append(out, res)
	}
	return out
}

// UtilityApplyResult is one node's outcome of an apply request.
type UtilityApplyResult struct {
	NodeID    uuid.UUID  `json:"node_id"`
	NodeName  string     `json:"node_name"`
	CommandID *uuid.UUID `json:"command_id,omitempty"`
	Queued    bool       `json:"queued"`
	Skipped   bool       `json:"skipped"`
	Reason    string     `json:"reason,omitempty"`
}

// NewUtilityApplyResults converts apply outcomes.
func NewUtilityApplyResults(list []utility_service.NodeApplyResult) []UtilityApplyResult {
	out := make([]UtilityApplyResult, 0, len(list))
	for _, r := range list {
		out = append(out, UtilityApplyResult{
			NodeID:    r.NodeID,
			NodeName:  r.NodeName,
			CommandID: r.CommandID,
			Queued:    r.Queued,
			Skipped:   r.Skipped,
			Reason:    r.Reason,
		})
	}
	return out
}

// OUUtilityLadderResult is one utility's rollup across a unit's nodes, for the
// unit page's Utilities section.
//
// The four rungs sum to Nodes, and Nodes is deliberately not the unit's node
// count: a nearer assignment on a node can override the unit's, and a node in a
// child unit can resolve a different set entirely. The section says "applied on
// X of Y" where Y is this number, not the unit's size.
type OUUtilityLadderResult struct {
	UtilityID     string `json:"utility_id"`
	Name          string `json:"name"`
	Nodes         int    `json:"nodes"`
	Applied       int    `json:"applied"`
	UpdatePending int    `json:"update_pending"`
	Failed        int    `json:"failed"`
	Declared      int    `json:"declared"`
}

// NewOUUtilityLadderResults maps the service rollup.
func NewOUUtilityLadderResults(rows []utility_service.OUUtilityLadder) []OUUtilityLadderResult {
	out := make([]OUUtilityLadderResult, 0, len(rows))
	for _, r := range rows {
		out = append(out, OUUtilityLadderResult{
			UtilityID:     r.UtilityID,
			Name:          r.Name,
			Nodes:         r.Nodes,
			Applied:       r.Applied,
			UpdatePending: r.UpdatePending,
			Failed:        r.Failed,
			Declared:      r.Declared,
		})
	}
	return out
}
