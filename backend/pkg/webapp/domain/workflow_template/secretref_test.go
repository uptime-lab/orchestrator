package workflow_template

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// The probe: trigger → condition on a secret → wait | wait. Nothing here
// touches a device and no block is privileged, so the type-based authoring gate
// sees nothing to object to — the branch that ran is the answer. Field-level
// redaction cannot close it either: `completed` vs `skipped` on the two
// downstream blocks is the same fact blanking ChosenPort was meant to hide, so
// Status has to be omitted downstream too.
func probeGraph(left string) *WorkflowGraph {
	return &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "cond", Type: workflow_block.TypeCondition, Params: map[string]interface{}{
				"left": left, "op": "contains", "right": "a",
			}},
			{ID: "yes", Type: workflow_block.TypeWait, Params: map[string]interface{}{"seconds": 1}},
			{ID: "no", Type: workflow_block.TypeWait, Params: map[string]interface{}{"seconds": 1}},
			{ID: "after", Type: workflow_block.TypeJoin},
		},
		Edges: []GraphEdge{
			{ID: "e0", From: "start", FromPort: "out", To: "cond"},
			{ID: "e1", From: "cond", FromPort: workflow_block.PortTrue, To: "yes"},
			{ID: "e2", From: "cond", FromPort: workflow_block.PortFalse, To: "no"},
			{ID: "e3", From: "yes", FromPort: "out", To: "after"},
		},
	}
}

func TestSecretFedUnprivilegedBlocksFindsTheConditionProbe(t *testing.T) {
	got := probeGraph("{{secrets.api_key}}").SecretFedUnprivilegedBlocks()

	if len(got) != 1 || got[0] != "cond" {
		t.Errorf("SecretFedUnprivilegedBlocks() = %v, want [cond]", got)
	}
}

// A node attribute IS a per-node secret, so it must not be the way around.
func TestSecretFedUnprivilegedBlocksCountsNodeAttributes(t *testing.T) {
	got := probeGraph("{{node.attributes.ci_token}}").SecretFedUnprivilegedBlocks()

	if len(got) != 1 {
		t.Errorf("SecretFedUnprivilegedBlocks() = %v, want the attribute reference caught", got)
	}
}

// An ordinary condition on a node fact stays unprivileged — the gate must not
// make every branching playbook admin-only.
func TestAConditionOnANodeFactIsNotPrivileged(t *testing.T) {
	if got := probeGraph("{{node.hostname}}").SecretFedUnprivilegedBlocks(); len(got) != 0 {
		t.Errorf("SecretFedUnprivilegedBlocks() = %v, want none", got)
	}
	if got := probeGraph("plain literal").SecretFedUnprivilegedBlocks(); len(got) != 0 {
		t.Errorf("SecretFedUnprivilegedBlocks() = %v, want none", got)
	}
}

// A block that legitimately consumes secrets is already privileged, so it must
// not be reported — otherwise this gate would re-refuse every real use.
func TestAPrivilegedBlockConsumingSecretsIsNotReported(t *testing.T) {
	g := &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "http", Type: workflow_block.TypeHTTPRequest, Params: map[string]interface{}{
				"method": "GET", "url": "https://x.test",
				"headers": "Authorization: Bearer {{secrets.api_key}}",
			}},
		},
		Edges: []GraphEdge{{ID: "e0", From: "start", FromPort: "out", To: "http"}},
	}

	if got := g.SecretFedUnprivilegedBlocks(); len(got) != 0 {
		t.Errorf("SecretFedUnprivilegedBlocks() = %v, want none: http_request is already privileged", got)
	}
}

// Everything the branch decision is visible in must be hidden, not just the
// condition itself: `completed` on one wait and `skipped` on the other is the
// same bit.
func TestBlocksDownstreamOfSecretConditionsCoversBothBranches(t *testing.T) {
	hidden := probeGraph("{{secrets.api_key}}").BlocksDownstreamOfSecretConditions()

	for _, id := range []string{"cond", "yes", "no", "after"} {
		if !hidden[id] {
			t.Errorf("block %q is not hidden; the branch taken is still readable", id)
		}
	}
	if hidden["start"] {
		t.Error("the trigger is upstream of the condition and need not be hidden")
	}
}

func TestBlocksDownstreamOfSecretConditionsIsEmptyForAnOrdinaryGraph(t *testing.T) {
	if hidden := probeGraph("{{node.hostname}}").BlocksDownstreamOfSecretConditions(); len(hidden) != 0 {
		t.Errorf("hidden = %v, want none — an ordinary run must stay fully readable", hidden)
	}
}
