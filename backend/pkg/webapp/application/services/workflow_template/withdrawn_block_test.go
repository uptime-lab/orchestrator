package workflow_template

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_import"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// bootOrderGraph is start -> "Update boot order", optionally followed by a
// reboot so a test can change the document while keeping that block.
func bootOrderGraph(withReboot bool) *workflow_template.WorkflowGraph {
	g := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "order", Type: "command.update_boot_order", Params: map[string]interface{}{
				"boot_order": []interface{}{"nvme", "network"},
			}},
		},
		Edges: []workflow_template.GraphEdge{{From: "start", To: "order"}},
	}
	if withReboot {
		g.Nodes = append(g.Nodes, workflow_template.GraphNode{ID: "reboot", Type: "command.reboot"})
		g.Edges = append(g.Edges, workflow_template.GraphEdge{From: "order", To: "reboot"})
	}
	return g
}

func rebootGraph() *workflow_template.WorkflowGraph {
	return &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "reboot", Type: "command.reboot"},
		},
		Edges: []workflow_template.GraphEdge{{From: "start", To: "reboot"}},
	}
}

// Every run of "Update boot order" is refused at dispatch, so a playbook built
// with it saves and activates cleanly and then fails on every node at that
// block. No new playbook may be created with one.
func TestCreateRefusesAWithdrawnBlock(t *testing.T) {
	repo := &stubTemplateRepo{}
	svc := importService(repo, &stubImageRepo{}, &stubSecretRepo{})
	_, err := svc.Create(context.Background(), workflow_template_create.WorkflowTemplateCreateCommand{
		ID:          uuid.New(),
		Name:        "set boot order",
		Graph:       bootOrderGraph(false),
		AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	})
	if !errors.Is(err, app_errors.ErrWorkflowTemplateInvalid) {
		t.Fatalf("create error = %v, want ErrWorkflowTemplateInvalid", err)
	}
	if repo.created != nil {
		t.Error("the playbook was stored despite the refusal")
	}
}

// An import is a new playbook too.
func TestImportRefusesAWithdrawnBlock(t *testing.T) {
	repo := &stubTemplateRepo{}
	svc := importService(repo, &stubImageRepo{}, &stubSecretRepo{})
	_, _, err := svc.Import(context.Background(), workflow_template_import.WorkflowTemplateImportCommand{
		ID: uuid.New(),
		Document: workflow_template.PortableDocument{
			Kind:          workflow_template.PortableKind,
			SchemaVersion: workflow_template.PortableSchemaVersion,
			Name:          "set boot order",
			Graph:         bootOrderGraph(false),
		},
		AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	})
	if !errors.Is(err, app_errors.ErrWorkflowTemplateInvalid) {
		t.Fatalf("import error = %v, want ErrWorkflowTemplateInvalid", err)
	}
	if repo.created != nil {
		t.Error("the playbook was stored despite the refusal")
	}
}

// A stored playbook that already holds the block must stay editable: refusing
// its save would strand it.
func TestUpdateKeepsAWithdrawnBlockTheStoredPlaybookHas(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	repo.stored.Graph = bootOrderGraph(false)
	cmd := updateCmd(accountID, templateID, nil)
	cmd.Steps = nil
	cmd.Graph = bootOrderGraph(true)
	if _, err := svc.Update(context.Background(), cmd, nil); err != nil {
		t.Fatalf("update of a playbook that already had the block: %v", err)
	}
	if repo.updates != 1 {
		t.Errorf("updates = %d, want 1", repo.updates)
	}
}

// An edit may not add one.
func TestUpdateRefusesAddingAWithdrawnBlock(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	repo.stored.Graph = rebootGraph()
	cmd := updateCmd(accountID, templateID, nil)
	cmd.Steps = nil
	cmd.Graph = bootOrderGraph(true)
	_, err := svc.Update(context.Background(), cmd, nil)
	if !errors.Is(err, app_errors.ErrWorkflowTemplateInvalid) {
		t.Fatalf("update error = %v, want ErrWorkflowTemplateInvalid", err)
	}
	if repo.updates != 0 {
		t.Error("the playbook was stored despite the refusal")
	}
}
