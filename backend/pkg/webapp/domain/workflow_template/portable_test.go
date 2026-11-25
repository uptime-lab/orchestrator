package workflow_template

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/google/uuid"
)

func portableTestTemplate() *WorkflowTemplate {
	return &WorkflowTemplate{
		ID:          uuid.New(),
		AccountID:   uuid.New(),
		Name:        "Reinstall OS",
		Description: "Wipe and reinstall",
		Graph: &WorkflowGraph{
			SchemaVersion: GraphSchemaVersion,
			Nodes: []GraphNode{
				{ID: "start", Type: workflow_block.TypeStart},
				{ID: "install", Type: "command.install_os", Params: map[string]interface{}{
					"image_id":      uuid.New().String(),
					"target_device": "/dev/nvme0n1",
				}},
				{ID: "notify", Type: workflow_block.TypeHTTPRequest, Params: map[string]interface{}{
					"method":  "POST",
					"url":     "https://example.com/hook",
					"headers": "Authorization: Bearer {{secrets.hook_token}}\nX-Extra: {{ secrets.extra.name }}",
				}},
			},
			Edges: []GraphEdge{
				{From: "start", To: "install"},
				{From: "install", To: "notify"},
			},
		},
	}
}

func TestExportPortableStripsAccountScopedIDs(t *testing.T) {
	tmpl := portableTestTemplate()

	doc := tmpl.ExportPortable()

	if doc.Kind != PortableKind {
		t.Errorf("kind = %q, want %q", doc.Kind, PortableKind)
	}
	if doc.SchemaVersion != PortableSchemaVersion {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, PortableSchemaVersion)
	}
	if doc.Name != tmpl.Name || doc.Description != tmpl.Description {
		t.Errorf("name/description not carried over: %q / %q", doc.Name, doc.Description)
	}

	install, ok := doc.Graph.NodeByID("install")
	if !ok {
		t.Fatal("install node missing from exported graph")
	}
	if _, present := install.Params["image_id"]; present {
		t.Error("image_id must be stripped from the exported document")
	}
	if install.Params["target_device"] != "/dev/nvme0n1" {
		t.Error("non-id params must be preserved")
	}

	// The stored template must not be mutated by the export.
	orig, _ := tmpl.Graph.NodeByID("install")
	if _, present := orig.Params["image_id"]; !present {
		t.Error("export must not mutate the source template's graph")
	}
}

func TestValidateDocument(t *testing.T) {
	valid := portableTestTemplate().ExportPortable()
	if err := valid.ValidateDocument(); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*PortableDocument)
	}{
		{"wrong kind", func(d *PortableDocument) { d.Kind = "something.else" }},
		{"wrong schema version", func(d *PortableDocument) { d.SchemaVersion = 99 }},
		{"missing name", func(d *PortableDocument) { d.Name = "" }},
		{"missing graph", func(d *PortableDocument) { d.Graph = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := portableTestTemplate().ExportPortable()
			tc.mutate(doc)
			if err := doc.ValidateDocument(); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestAccountScopedParams(t *testing.T) {
	tmpl := portableTestTemplate()
	refs := AccountScopedParams(tmpl.Graph)
	if len(refs) != 1 {
		t.Fatalf("expected 1 account-scoped param, got %d", len(refs))
	}
	if refs[0].NodeID != "install" || refs[0].Param != "image_id" || refs[0].Type != workflow_block.ParamOSImage {
		t.Errorf("unexpected ref: %+v", refs[0])
	}
}

func TestSecretReferences(t *testing.T) {
	tmpl := portableTestTemplate()
	refs := SecretReferences(tmpl.Graph)
	want := []string{"extra.name", "hook_token"}
	if len(refs) != len(want) {
		t.Fatalf("secret refs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("secret refs = %v, want %v", refs, want)
			break
		}
	}
}

// A draft may be saved with required params still blank; the same template
// must fail validation the moment it is activated.
func TestDraftAllowsMissingRequiredParams(t *testing.T) {
	incomplete := func(status WorkflowTemplateStatus) *WorkflowTemplate {
		return &WorkflowTemplate{
			Name:   "incomplete",
			Status: status,
			Graph: &WorkflowGraph{
				SchemaVersion: GraphSchemaVersion,
				Nodes: []GraphNode{
					{ID: "start", Type: workflow_block.TypeStart},
					// install_agent: target_device and agent_version required, both absent
					{ID: "agent", Type: "command.install_agent", Params: map[string]interface{}{
						"target_device": "",
					}},
				},
				Edges: []GraphEdge{{From: "start", To: "agent"}},
			},
		}
	}

	draft := incomplete(StatusDraft)
	if err := draft.Normalize(); err != nil {
		t.Fatalf("draft with missing required params must normalize: %v", err)
	}
	if err := draft.Validate(); err != nil {
		t.Fatalf("draft with missing required params must validate: %v", err)
	}

	active := incomplete(StatusActive)
	if err := active.Normalize(); err == nil {
		t.Fatal("active template with missing required params must be rejected")
	}

	// Present values are still type-checked even in a draft.
	badType := incomplete(StatusDraft)
	badType.Graph.Nodes[1].Params["root_partition"] = "not-a-number"
	if err := badType.Normalize(); err == nil {
		t.Fatal("draft with a wrongly-typed param must be rejected")
	}
}

func TestSecretReferencesNestedValues(t *testing.T) {
	graph := &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{{
			ID: "n", Type: workflow_block.TypeHTTPRequest,
			Params: map[string]interface{}{
				"nested": map[string]interface{}{"a": "{{secrets.deep}}"},
				"list":   []interface{}{"{{secrets.listed}}"},
				"number": 5,
			},
		}},
	}
	refs := SecretReferences(graph)
	if len(refs) != 2 || refs[0] != "deep" || refs[1] != "listed" {
		t.Errorf("secret refs = %v, want [deep listed]", refs)
	}
}

// The export lists every name a run resolves: a name is anything without "}"
// (the run-time grammar), not only letters, digits, dot, dash and underscore.
func TestSecretReferencesUseTheResolverGrammar(t *testing.T) {
	graph := &WorkflowGraph{
		SchemaVersion: GraphSchemaVersion,
		Nodes: []GraphNode{{
			ID: "n", Type: workflow_block.TypeHTTPRequest,
			Params: map[string]interface{}{
				"headers": "Authorization: {{ secrets.Registry credentials }}\nX-A: {{secrets.a/b}}",
				"body":    "{{secrets.ключ}} {{secrets.x:y}} {{secrets.}} {{ node.name }}",
			},
		}},
	}
	refs := SecretReferences(graph)
	want := []string{"Registry credentials", "a/b", "x:y", "ключ"}
	if len(refs) != len(want) {
		t.Fatalf("secret refs = %q, want %q", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("secret refs = %q, want %q", refs, want)
		}
	}
}
