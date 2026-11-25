package results

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// WorkflowBlockParamResult represents a block parameter spec in API responses
type WorkflowBlockParamResult struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Details is the optional expanded help behind the description — the
	// tooltip/details tier the UI shows on demand.
	Details     string      `json:"details,omitempty"`
	Type        string      `json:"type"`
	Required    bool        `json:"required"`
	Default     interface{} `json:"default,omitempty"`
	EnumValues  []string    `json:"enum_values,omitempty"`
	Placeholder string      `json:"placeholder,omitempty"`
	// Min/Max are a numeric parameter's bounds, for the editor to render.
	// Pointers so a bound of zero survives omitempty.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

// WorkflowBlockPortResult represents a block port in API responses
type WorkflowBlockPortResult struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
}

// WorkflowBlockResult represents a workflow block definition in API responses
type WorkflowBlockResult struct {
	Type        string `json:"type"`
	Slug        string `json:"slug,omitempty"`
	Executor    string `json:"executor"`
	Category    string `json:"category"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Details is the optional expanded help behind the description — the
	// tooltip/details tier the UI shows on demand.
	Details     string                     `json:"details,omitempty"`
	Params      []WorkflowBlockParamResult `json:"params"`
	Inputs      []WorkflowBlockPortResult  `json:"inputs"`
	Outputs     []WorkflowBlockPortResult  `json:"outputs"`
	Dangerous   bool                       `json:"dangerous"`
	RebootsNode bool                       `json:"reboots_node"`
	// Boot is the device boot state this block needs: "" (either), "managed_os",
	// "deferred_to_managed_os" or "recovery". See workflow_block.BootRequirement
	// — in particular that managed_os is a CLAIM nothing enforces.
	Boot string `json:"boot_requirement,omitempty"`
	// BootNote is the case the requirement cannot express, for a block whose
	// real precondition is a parameter rather than the boot mode.
	BootNote string `json:"boot_note,omitempty"`
}

// NewWorkflowBlockResults converts block definitions to API results
func NewWorkflowBlockResults(defs []workflow_block.BlockDefinition) []WorkflowBlockResult {
	out := make([]WorkflowBlockResult, 0, len(defs))
	for _, def := range defs {
		params := make([]WorkflowBlockParamResult, 0, len(def.Params))
		for _, p := range def.Params {
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
		out = append(out, WorkflowBlockResult{
			Type:        def.Type,
			Slug:        def.Slug,
			Executor:    string(def.Executor),
			Category:    def.Category,
			Name:        def.Name,
			Description: def.Description,
			Details:     def.Details,
			Params:      params,
			Inputs:      newWorkflowBlockPortResults(def.Inputs),
			Outputs:     newWorkflowBlockPortResults(def.Outputs),
			Dangerous:   def.Dangerous,
			RebootsNode: def.RebootsNode,
			Boot:        string(def.Boot),
			BootNote:    def.BootNote,
		})
	}
	return out
}

func newWorkflowBlockPortResults(ports []workflow_block.Port) []WorkflowBlockPortResult {
	out := make([]WorkflowBlockPortResult, 0, len(ports))
	for _, p := range ports {
		out = append(out, WorkflowBlockPortResult{Name: p.Name, Label: p.Label})
	}
	return out
}
