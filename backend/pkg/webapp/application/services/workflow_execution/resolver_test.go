package workflow_execution

import (
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

func TestResolve(t *testing.T) {
	rc := resolveContext{
		nodeFacts: map[string]string{
			"model":                     "Cortex-A76 (CM5)",
			"hostname":                  "rpi-01",
			"attributes.ci_agent_token": "per-node-value",
		},
		secrets: map[string]string{"api_token": "abc123"},
		outputs: map[string]wen.GraphNodeState{
			"wait": {Outputs: map[string]string{"match": "tok-xyz"}},
		},
	}
	tests := []struct{ in, want string }{
		{"{{node.model}}", "Cortex-A76 (CM5)"},
		{"host={{node.hostname}}", "host=rpi-01"},
		{"Bearer {{secrets.api_token}}", "Bearer abc123"},
		{"{{node.attributes.ci_agent_token}}", "per-node-value"},
		{"{{blocks.wait.outputs.match}}", "tok-xyz"},
		{"{{node.unknown}}", ""},
		{"{{blocks.missing.outputs.x}}", ""},
		{"no refs here", "no refs here"},
		{"{{node.model}}/{{secrets.api_token}}", "Cortex-A76 (CM5)/abc123"},
	}
	for _, tt := range tests {
		if got := rc.resolve(tt.in); got != tt.want {
			t.Errorf("resolve(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestNodeFactsOfID pins the {{node.id}} fact: it is what lets a run name the
// node row it operates on (the attribute endpoints and dispatch key on it), and
// nothing else in the fact map carries the id.
func TestNodeFactsOfID(t *testing.T) {
	n := &node.Node{ID: uuid.New(), Name: "blade-1", Hostname: "rpi-01", Status: node.StatusOnline}
	facts := nodeFactsOf(n)
	if facts["id"] != n.ID.String() {
		t.Errorf("facts[id] = %q, want %q", facts["id"], n.ID.String())
	}
	rc := resolveContext{nodeFacts: facts}
	if got := rc.resolve("{{node.id}}"); got != n.ID.String() {
		t.Errorf("resolve({{node.id}}) = %q, want %q", got, n.ID.String())
	}
}

func TestResolveParams(t *testing.T) {
	rc := resolveContext{secrets: map[string]string{"t": "V"}}
	out := rc.resolveParams(map[string]interface{}{
		"content": "token={{secrets.t}}",
		"count":   float64(3),
		"nested":  map[string]interface{}{"h": "Bearer {{secrets.t}}"},
		"list":    []interface{}{"{{secrets.t}}", "plain"},
	})
	if out["content"] != "token=V" {
		t.Errorf("content = %v", out["content"])
	}
	if out["count"] != float64(3) {
		t.Errorf("count mutated: %v", out["count"])
	}
	if out["nested"].(map[string]interface{})["h"] != "Bearer V" {
		t.Errorf("nested = %v", out["nested"])
	}
	if out["list"].([]interface{})[0] != "V" {
		t.Errorf("list = %v", out["list"])
	}
}

func TestEvalCondition(t *testing.T) {
	tests := []struct {
		left, op, right string
		want            bool
	}{
		{"CM5", "eq", "CM5", true},
		{"CM4", "eq", "CM5", false},
		{"CM4", "ne", "CM5", true},
		{"Cortex (CM5)", "contains", "CM5", true},
		{"Cortex (CM4)", "contains", "CM5", false},
		{"Cortex (CM4)", "not_contains", "CM5", true},
		{"something", "exists", "", true},
		{"", "exists", "", false},
		{"", "not_exists", "", true},
		{"x", "bogus", "y", false},
	}
	for _, tt := range tests {
		if got := evalCondition(tt.left, tt.op, tt.right); got != tt.want {
			t.Errorf("eval(%q %s %q) = %v, want %v", tt.left, tt.op, tt.right, got, tt.want)
		}
	}
}

// A secret can be authored into ANY param, so the engine
// records where it actually went instead of relying on a list of params that
// "can" hold secrets. Read off the unresolved template — after substitution the
// value is indistinguishable from a typed literal.
func TestSecretDerivedParams(t *testing.T) {
	params := map[string]interface{}{
		"dest_path": "/srv/{{secrets.deploy_token}}/artifact",
		"env_key":   "{{ node.attributes.ci_token }}",          // attributes are secrets too
		"url":       "https://cdn.example/pkg.tar.gz",          // no reference at all
		"headers":   "Authorization: Bearer {{node.hostname}}", // non-secret ref
		"packages":  []interface{}{"acme=1.0+{{secrets.repo_key}}"},
		"nested":    map[string]interface{}{"owner": "{{secrets.svc_user}}"},
		"retries":   float64(3),
	}

	got := SecretDerivedParams(params)
	want := []string{"dest_path", "env_key", "nested", "packages"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSecretDerivedParamsIgnoresNonSecretReferences(t *testing.T) {
	// {{node.hostname}} is not credential material; redacting it would hide
	// ordinary params from operators for no gain.
	if got := SecretDerivedParams(map[string]interface{}{"name": "{{node.hostname}}-web"}); got != nil {
		t.Errorf("non-secret references must not mark a param: %v", got)
	}
}

// Every secret name the playbook export lists is one a run resolves, and the
// reverse: a playbook exported without a secret it uses arrives broken.
func TestExportListsExactlyTheSecretsARunResolves(t *testing.T) {
	text := "{{ secrets.Registry credentials }} {{secrets.a/b}} {{secrets.ci.token-1}} {{secrets.ключ}}"
	graph := &workflow_template.WorkflowGraph{Nodes: []workflow_template.GraphNode{{
		ID: "n", Params: map[string]interface{}{"headers": text},
	}}}
	listed := workflow_template.SecretReferences(graph)
	if len(listed) != 4 {
		t.Fatalf("export listed %q, want four names", listed)
	}
	for _, name := range listed {
		rc := resolveContext{secrets: map[string]string{name: "V"}}
		if got := rc.resolve(text); got == "" || got == text {
			t.Errorf("listed name %q does not resolve in %q", name, text)
		}
	}
}
