package workflow_template

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// PortableKind identifies an exported playbook document.
const PortableKind = "orchestrator.playbook"

// PortableSchemaVersion is the current portable document schema version,
// stamped on export and checked on import for forward compatibility.
const PortableSchemaVersion = 1

// PortableDocument is the account-independent representation of a playbook
// used for export/import and for the built-in starter catalog. It carries the
// graph (block types + params) but none of the account-specific identifiers
// (template/account/OU/author ids, image or SSH-key ids).
type PortableDocument struct {
	Kind          string         `json:"kind"`
	SchemaVersion int            `json:"schema_version"`
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	Graph         *WorkflowGraph `json:"graph"`
	ExportedAt    *time.Time     `json:"exported_at,omitempty"`
}

// ValidateDocument checks the portable envelope. Graph content is validated
// separately through the normal template Normalize/Validate path so import
// and save-time validation can never diverge.
func (d *PortableDocument) ValidateDocument() error {
	if d.Kind != PortableKind {
		return fmt.Errorf("unsupported document kind '%s' (expected '%s')", d.Kind, PortableKind)
	}
	if d.SchemaVersion != PortableSchemaVersion {
		return fmt.Errorf("unsupported document schema_version %d (expected %d)", d.SchemaVersion, PortableSchemaVersion)
	}
	if d.Name == "" {
		return fmt.Errorf("document name is required")
	}
	if d.Graph == nil {
		return fmt.Errorf("document graph is required")
	}
	return nil
}

// ExportPortable builds the portable document for a template. Account-scoped
// id parameters (OS images, SSH keys) are stripped: they are meaningless in
// another account or install and must be re-picked after import.
func (wt *WorkflowTemplate) ExportPortable() *PortableDocument {
	graph := cloneGraph(wt.EffectiveGraph())
	for _, ref := range AccountScopedParams(graph) {
		delete(graph.Nodes[ref.NodeIndex].Params, ref.Param)
	}
	now := time.Now().UTC()
	return &PortableDocument{
		Kind:          PortableKind,
		SchemaVersion: PortableSchemaVersion,
		Name:          wt.Name,
		Description:   wt.Description,
		Graph:         graph,
		ExportedAt:    &now,
	}
}

// AccountScopedParam points at a graph-node parameter whose value is an
// account-scoped identifier (an OS image or SSH key id).
type AccountScopedParam struct {
	NodeIndex int
	NodeID    string
	NodeLabel string // block name or node id, for human-readable warnings
	Param     string
	Type      workflow_block.ParamType
	Value     string
}

// AccountScopedParams returns the graph parameters holding account-scoped ids
// (present and non-empty), resolved against the block registry.
func AccountScopedParams(g *WorkflowGraph) []AccountScopedParam {
	if g == nil {
		return nil
	}
	var out []AccountScopedParam
	for i, node := range g.Nodes {
		block, known := workflow_block.FindByType(node.Type)
		if !known {
			continue
		}
		for _, spec := range block.Params {
			if spec.Type != workflow_block.ParamOSImage && spec.Type != workflow_block.ParamSSHKey {
				continue
			}
			value, _ := node.Params[spec.Name].(string)
			if value == "" {
				continue
			}
			label := node.Name
			if label == "" {
				label = block.Name
			}
			out = append(out, AccountScopedParam{
				NodeIndex: i,
				NodeID:    node.ID,
				NodeLabel: label,
				Param:     spec.Name,
				Type:      spec.Type,
				Value:     value,
			})
		}
	}
	return out
}

// SecretReferences returns the distinct secret names referenced by the
// graph's parameters via {{secrets.<name>}}, sorted for determinism. A name is
// read with the run-time resolver's grammar (SecretReferenceName), so every
// secret a run would resolve is listed — a narrower pattern here exports a
// playbook without the secrets it needs.
func SecretReferences(g *WorkflowGraph) []string {
	if g == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, node := range g.Nodes {
		collectSecretRefs(node.Params, seen)
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func collectSecretRefs(value interface{}, seen map[string]bool) {
	switch v := value.(type) {
	case string:
		for _, match := range ReferencePattern.FindAllStringSubmatch(v, -1) {
			if name, ok := SecretReferenceName(ReferenceExpression(match)); ok {
				seen[name] = true
			}
		}
	case map[string]interface{}:
		for _, item := range v {
			collectSecretRefs(item, seen)
		}
	case []interface{}:
		for _, item := range v {
			collectSecretRefs(item, seen)
		}
	}
}

// cloneGraph deep-copies a graph through its JSON form so export/import
// sanitization never mutates the stored template.
func cloneGraph(g *WorkflowGraph) *WorkflowGraph {
	if g == nil {
		return nil
	}
	raw, err := json.Marshal(g)
	if err != nil {
		// A graph that came from JSON always marshals back; this is unreachable
		// for real data and a programming error otherwise.
		panic(fmt.Sprintf("clone workflow graph: %v", err))
	}
	var out WorkflowGraph
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(fmt.Sprintf("clone workflow graph: %v", err))
	}
	return &out
}
