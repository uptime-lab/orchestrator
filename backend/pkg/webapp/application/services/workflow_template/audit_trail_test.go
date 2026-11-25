package workflow_template

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_import"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// deletableTemplateRepo extends the import stub with what Delete touches.
type deletableTemplateRepo struct {
	stubTemplateRepo
	stored  *workflow_template.WorkflowTemplate
	deleted bool
}

func (r *deletableTemplateRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	copied := *r.stored
	return &copied, nil
}

func (r *deletableTemplateRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	r.deleted = true
	return nil
}

// A playbook mutation must land in the audit trail attributed to the calling
// user, and the detail must carry the NAME and flags only — never a block
// parameter, because params can hold literal credentials and audit_events is
// on no rewrap list.
func TestPlaybookMutationsAreAudited(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()
	// A value that would only reach the trail through a params leak.
	leak := "Bearer SUPERSECRET-hook-token"

	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: "trigger.start"},
			{ID: "notify", Type: "integration.http_request", Params: map[string]interface{}{
				"method":  "POST",
				"url":     "https://example.com/hook",
				"headers": leak,
			}},
		},
		Edges: []workflow_template.GraphEdge{{From: "start", To: "notify"}},
	}

	repo := &stubTemplateRepo{}
	svc, sink := importServiceWithAudit(repo, nil, nil)

	created, err := svc.Create(context.Background(), workflow_template_create.WorkflowTemplateCreateCommand{
		ID:          uuid.New(),
		Name:        "audited playbook",
		AuthContext: commands.NewAuthContext(accountID, userID),
		Graph:       graph,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionPlaybookCreate)
	if !ok {
		t.Fatalf("no %s event recorded; got %v", audit_event.ActionPlaybookCreate, sink.Actions())
	}
	if event.AccountID != accountID || event.ActorUserID == nil || *event.ActorUserID != userID {
		t.Errorf("create event misattributed: account=%s actor=%v", event.AccountID, event.ActorUserID)
	}
	if event.TargetType != audit_event.TargetPlaybook || event.TargetID != created.ID.String() {
		t.Errorf("create event target = %s/%s", event.TargetType, event.TargetID)
	}
	if !strings.Contains(event.Detail, "audited playbook") {
		t.Errorf("detail should carry the playbook name, got %q", event.Detail)
	}

	// Delete on a separate service+sink so the assertions are independent.
	delRepo := &deletableTemplateRepo{stored: created}
	delSvc, delSink := importServiceWithAudit(&delRepo.stubTemplateRepo, nil, nil)
	delSvc.templateRepo = delRepo
	if err := delSvc.Delete(context.Background(), accountID, userID, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := delSink.Find(audit_event.ActionPlaybookDelete); !ok {
		t.Fatalf("no %s event recorded; got %v", audit_event.ActionPlaybookDelete, delSink.Actions())
	}

	// The trail-safety rule: no recorded detail may carry a block parameter.
	for _, recorded := range append(sink.Events(), delSink.Events()...) {
		if strings.Contains(recorded.Detail, "SUPERSECRET") || strings.Contains(recorded.Detail, "example.com") {
			t.Errorf("audit detail leaked a block parameter: %q", recorded.Detail)
		}
	}
}

// An import is recorded as its own action, distinct from create, with the
// warnings COUNT (not the warning texts) in the detail.
func TestImportIsAudited(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()

	repo := &stubTemplateRepo{}
	svc, sink := importServiceWithAudit(repo,
		&stubImageRepo{existing: map[uuid.UUID]bool{}},
		&stubSecretRepo{names: map[string]bool{}},
	)

	// An image id that resolves nowhere, so the import lands with warnings.
	template, warnings, err := svc.Import(context.Background(), workflow_template_import.WorkflowTemplateImportCommand{
		ID:          uuid.New(),
		Document:    importDocument(uuid.New().String()),
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warnings from an unresolvable image reference")
	}

	event, ok := sink.Find(audit_event.ActionPlaybookImport)
	if !ok {
		t.Fatalf("no %s event recorded; got %v", audit_event.ActionPlaybookImport, sink.Actions())
	}
	if event.TargetID != template.ID.String() {
		t.Errorf("import event target = %s", event.TargetID)
	}
	if !strings.Contains(event.Detail, "warnings=") {
		t.Errorf("import detail should carry the warnings count, got %q", event.Detail)
	}
	// Warning TEXTS name cleared params and secret names; the detail must not
	// copy them (free text is one step from a pasted credential).
	for _, w := range warnings {
		if w != "" && strings.Contains(event.Detail, w) {
			t.Errorf("import detail copied a warning text: %q", event.Detail)
		}
	}
}
