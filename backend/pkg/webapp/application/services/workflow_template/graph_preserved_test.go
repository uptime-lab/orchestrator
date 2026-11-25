package workflow_template

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// savingTemplateRepo records what was actually written, which is the whole
// question here: the response body looks the same either way.
type savingTemplateRepo struct {
	repository.WorkflowTemplateRepository
	stored *workflow_template.WorkflowTemplate
	saved  *workflow_template.WorkflowTemplate
}

func (r *savingTemplateRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	copied := *r.stored
	return &copied, nil
}

func (r *savingTemplateRepo) Update(_ context.Context, tmpl *workflow_template.WorkflowTemplate) error {
	copied := *tmpl
	r.saved = &copied
	return nil
}

// scheduledGraph is a cron-triggered playbook with a condition — i.e. a
// document that GraphFromSteps could never rebuild.
func scheduledGraph() *workflow_template.WorkflowGraph {
	return &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: "trigger.schedule", Params: map[string]any{"cron": "0 3 * * *"}},
			{ID: "n1", Type: "command.reboot", Params: map[string]any{"delay_seconds": float64(5)}},
		},
		Edges: []workflow_template.GraphEdge{{ID: "e1", From: "start", FromPort: "out", To: "n1"}},
	}
}

func savingFixture(graph *workflow_template.WorkflowGraph) (*Service, *savingTemplateRepo, uuid.UUID, uuid.UUID) {
	accountID, templateID := uuid.New(), uuid.New()
	stored := &workflow_template.WorkflowTemplate{
		ID:        templateID,
		AccountID: accountID,
		Name:      "nightly",
		Status:    workflow_template.StatusActive,
		Graph:     graph,
		UpdatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
	if graph != nil {
		steps, err := graph.CommandSteps()
		if err == nil {
			stored.Steps = steps
		}
	}
	repo := &savingTemplateRepo{stored: stored}
	auditRecorder, _ := audittest.New()
	svc := &Service{templateRepo: repo, audit: auditRecorder, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return svc, repo, accountID, templateID
}

// A rename that carries no graph must not silently unschedule the playbook.
// This was live: the assignment was unconditional, so an omitted graph wrote
// NULL, read back as a bare trigger.start chain, and the cron trigger — with
// every condition, join, wait and per-block policy — was gone for good.
func TestUpdateWithoutAGraphKeepsTheStoredOne(t *testing.T) {
	svc, repo, accountID, templateID := savingFixture(scheduledGraph())

	// Steps but no graph: exactly what a client that round-trips the documented
	// step list sends, and the shape that used to write NULL over the graph.
	cmd := workflow_template_update.WorkflowTemplateUpdateCommand{
		ID:          templateID,
		Name:        "nightly renamed",
		Steps:       repo.stored.Steps,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}
	if _, err := svc.Update(context.Background(), cmd, nil); err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}

	if repo.saved == nil {
		t.Fatal("nothing was written")
	}
	if repo.saved.Graph == nil {
		t.Fatal("stored graph was destroyed by an update that did not mention it")
	}
	if got := repo.saved.Graph.ScheduleCron(); got != "0 3 * * *" {
		t.Errorf("schedule_cron = %q, want %q: the playbook silently stopped firing", got, "0 3 * * *")
	}
	if len(repo.saved.Steps) != 1 {
		t.Errorf("steps = %d, want 1 re-derived from the preserved graph", len(repo.saved.Steps))
	}
	if repo.saved.Name != "nightly renamed" {
		t.Errorf("name = %q, want the update to still apply", repo.saved.Name)
	}
}

// A submitted graph is still the source of truth — preserving an omitted one
// must not turn the editor's save into a no-op.
func TestUpdateWithAGraphStillReplacesIt(t *testing.T) {
	svc, repo, accountID, templateID := savingFixture(scheduledGraph())

	replacement := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: "trigger.start"},
			{ID: "n1", Type: "command.reboot", Params: map[string]any{"delay_seconds": float64(9)}},
		},
		Edges: []workflow_template.GraphEdge{{ID: "e1", From: "start", FromPort: "out", To: "n1"}},
	}
	cmd := workflow_template_update.WorkflowTemplateUpdateCommand{
		ID:          templateID,
		Name:        "nightly",
		Graph:       replacement,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}
	if _, err := svc.Update(context.Background(), cmd, nil); err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}
	if repo.saved.Graph.ScheduleCron() != "" {
		t.Errorf("schedule_cron = %q, want empty: the submitted graph must win",
			repo.saved.Graph.ScheduleCron())
	}
}

// A genuinely legacy row — no stored graph — keeps the old steps-only contract.
func TestUpdateOfALegacyStepsOnlyTemplateStillWritesSteps(t *testing.T) {
	svc, repo, accountID, templateID := savingFixture(nil)

	cmd := workflow_template_update.WorkflowTemplateUpdateCommand{
		ID:   templateID,
		Name: "legacy",
		Steps: []workflow_template.WorkflowStep{{
			Order:   0,
			Command: workflow_template.CommandSpec{Slug: "reboot"},
		}},
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}
	if _, err := svc.Update(context.Background(), cmd, nil); err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}
	if repo.saved.Graph != nil {
		t.Errorf("graph = %v, want nil: a legacy row must stay legacy", repo.saved.Graph)
	}
	if len(repo.saved.Steps) != 1 {
		t.Errorf("steps = %d, want 1", len(repo.saved.Steps))
	}
}

// The regression the fix above left open: with the graph
// preserved, the handler's pre-check inspects an EMPTY document while a
// privileged stored graph survives under whatever ou_id and status the request
// carries. A playbook.author holder could therefore re-scope and publish a
// privileged playbook nothing had examined — the exact operation the dedicated
// re-target route refuses. So the gate now runs again on the MERGED document.
func TestUpdateAuthorizesTheMergedDocumentNotTheSubmittedOne(t *testing.T) {
	privileged := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: "trigger.start"},
			{ID: "n1", Type: "server.set_boot_policy", Params: map[string]interface{}{
				"boot_to_os": "on", "network_first": "unchanged",
			}},
		},
		Edges: []workflow_template.GraphEdge{{ID: "e1", From: "start", FromPort: "out", To: "n1"}},
	}
	svc, repo, accountID, templateID := savingFixture(privileged)

	var sawBlockTypes []string
	refuse := errors.New("privileged authoring refused")
	cmd := workflow_template_update.WorkflowTemplateUpdateCommand{
		ID:          templateID,
		Name:        "nightly",
		Status:      "active",
		Steps:       repo.stored.Steps,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
		// No graph — the shape that exposed the gap.
	}

	_, err := svc.Update(context.Background(), cmd,
		func(graph *workflow_template.WorkflowGraph, _ []workflow_template.WorkflowStep, _ []uuid.UUID) error {
			tmpl := &workflow_template.WorkflowTemplate{Graph: graph}
			sawBlockTypes = tmpl.BlockTypes()
			return refuse
		})

	if !errors.Is(err, refuse) {
		t.Fatalf("Update() = %v, want the authorizer's refusal to propagate", err)
	}
	if repo.saved != nil {
		t.Error("a refused update must not write")
	}
	found := false
	for _, blockType := range sawBlockTypes {
		if blockType == "server.set_boot_policy" {
			found = true
		}
	}
	if !found {
		t.Errorf("authorizer saw %v, want the STORED privileged block — it was inspecting the submitted document, which is empty here",
			sawBlockTypes)
	}
}
