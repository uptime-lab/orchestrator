package results

import (
	"encoding/json"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// Template AUTHORING redaction: what a caller without secret.read_value may
// see of a playbook's own document (graph, steps, execution snapshot). The
// run-side redaction covers what a run RESOLVED; this covers what an author
// TYPED — literal credentials in a script, a header, a url. {{secrets.*}}
// references are already only names, but a literal is indistinguishable from
// prose, so the rule follows the parameter TYPE:
//
//   - Free-text params (string, text, expr) are replaced with the placeholder:
//     they are exactly where a credential can be typed.
//   - Save-time-validated types (int, bool, enum, os_image, ssh_key,
//     boot_order, cron) are kept: their values cannot be credentials — an
//     enum is from a fixed list, a cron must compile, an image id must
//     resolve — and losing them would blind the viewer to the playbook's
//     structure for no secrecy gain.
//   - Unknown block kinds and undeclared params fail CLOSED: any string (or
//     string found inside a list or nested map) is replaced.
//
// The save path refuses a document that still carries the placeholder
// (workflow_template.Validate), which is what makes handing a redacted
// document to an editor safe: it cannot be silently written back.
var authoringKeepTypes = map[workflow_block.ParamType]bool{
	workflow_block.ParamInt:       true,
	workflow_block.ParamBool:      true,
	workflow_block.ParamEnum:      true,
	workflow_block.ParamOSImage:   true,
	workflow_block.ParamSSHKey:    true,
	workflow_block.ParamBootOrder: true,
	workflow_block.ParamCron:      true,
}

// RedactAuthoring redacts the template document for a caller without
// secret.read_value. The result's Graph/Steps are replaced with deep copies —
// the originals may be shared with the domain object.
func (r *WorkflowTemplateResult) RedactAuthoring() {
	r.Graph = redactGraphAuthoring(r.Graph)
	r.Steps = redactStepsAuthoring(r.Steps)
}

// RedactAuthoring redacts the pinned template snapshot the execution carries.
// Execution-node STATE goes through RedactGraphStateForGraph separately; this
// covers the document the run was started from.
func (r *WorkflowExecutionResult) RedactAuthoring() {
	if r.TemplateSnapshot == nil {
		return
	}
	snap := *r.TemplateSnapshot
	snap.Graph = redactGraphAuthoring(snap.Graph)
	snap.Steps = redactStepsAuthoring(snap.Steps)
	r.TemplateSnapshot = &snap
}

// RedactGraphAuthoring is the exported form for payloads that are not result
// DTOs (the portable export document serves the raw graph).
func RedactGraphAuthoring(g *workflow_template.WorkflowGraph) *workflow_template.WorkflowGraph {
	return redactGraphAuthoring(g)
}

func redactGraphAuthoring(g *workflow_template.WorkflowGraph) *workflow_template.WorkflowGraph {
	if g == nil {
		return nil
	}
	copied := deepCopyGraph(g)
	for i := range copied.Nodes {
		node := &copied.Nodes[i]
		if len(node.Params) == 0 {
			continue
		}
		def, known := workflow_block.FindByType(node.Type)
		specs := map[string]workflow_block.ParamSpec{}
		if known {
			for _, p := range def.Params {
				specs[p.Name] = p
			}
		}
		for key, value := range node.Params {
			if spec, declared := specs[key]; declared && authoringKeepTypes[spec.Type] {
				continue
			}
			node.Params[key] = redactAuthoringValue(value)
		}
	}
	return copied
}

// redactStepsAuthoring reuses the run-side payload redaction: steps are
// derived command payloads, so the fixed secret-bearing field list plus the
// engine's derived-params bookkeeping is exactly the coverage the command
// endpoints already give those same bytes.
func redactStepsAuthoring(steps []workflow_template.WorkflowStep) []workflow_template.WorkflowStep {
	if len(steps) == 0 {
		return steps
	}
	out := make([]workflow_template.WorkflowStep, len(steps))
	copy(out, steps)
	for i := range out {
		out[i].Command.Payload = RedactCommandPayload(out[i].Command.Payload)
	}
	return out
}

// redactAuthoringValue replaces every string found in the value — including
// inside lists and nested maps — with the placeholder. Numbers and booleans
// are kept: they cannot carry a credential.
func redactAuthoringValue(v interface{}) interface{} {
	switch tv := v.(type) {
	case string:
		if tv == "" {
			return tv
		}
		return redactedPlaceholder
	case []interface{}:
		out := make([]interface{}, len(tv))
		for i, inner := range tv {
			out[i] = redactAuthoringValue(inner)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(tv))
		for k, inner := range tv {
			out[k] = redactAuthoringValue(inner)
		}
		return out
	default:
		return v
	}
}

// deepCopyGraph copies via the JSON round-trip the graph already survives at
// rest, so no field can be forgotten by a hand-written copy.
func deepCopyGraph(g *workflow_template.WorkflowGraph) *workflow_template.WorkflowGraph {
	raw, err := json.Marshal(g)
	if err != nil {
		// A graph that cannot marshal cannot be served either; an empty graph
		// is the fail-closed answer.
		return &workflow_template.WorkflowGraph{SchemaVersion: g.SchemaVersion}
	}
	var out workflow_template.WorkflowGraph
	if err := json.Unmarshal(raw, &out); err != nil {
		return &workflow_template.WorkflowGraph{SchemaVersion: g.SchemaVersion}
	}
	return &out
}
