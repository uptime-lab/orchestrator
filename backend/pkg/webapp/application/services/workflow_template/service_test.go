package workflow_template

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_import"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// Stubs embed the port interface and implement only what Import touches;
// anything else panics, which is the desired test behavior.

type stubTemplateRepo struct {
	repository.WorkflowTemplateRepository
	created *workflow_template.WorkflowTemplate
}

func (s *stubTemplateRepo) Create(ctx context.Context, t *workflow_template.WorkflowTemplate) error {
	s.created = t
	return nil
}

type stubImageRepo struct {
	repository.ImageRepository
	existing map[uuid.UUID]bool
}

func (s *stubImageRepo) GetByID(ctx context.Context, accountID, imageID uuid.UUID) (*image.Image, error) {
	if s.existing[imageID] {
		return &image.Image{}, nil
	}
	return nil, app_errors.ErrNotFound
}

type stubSecretRepo struct {
	repository.SecretRepository
	names map[string]bool
}

func (s *stubSecretRepo) GetByName(ctx context.Context, accountID uuid.UUID, name string) (*secret.Secret, error) {
	if s.names[name] {
		return &secret.Secret{}, nil
	}
	return nil, app_errors.ErrSecretNotFound
}

func importDocument(imageID string) workflow_template.PortableDocument {
	installParams := map[string]interface{}{"target_device": "/dev/nvme0n1"}
	if imageID != "" {
		installParams["image_id"] = imageID
	}
	return workflow_template.PortableDocument{
		Kind:          workflow_template.PortableKind,
		SchemaVersion: workflow_template.PortableSchemaVersion,
		Name:          "Imported playbook",
		Description:   "from a portable document",
		Graph: &workflow_template.WorkflowGraph{
			SchemaVersion: workflow_template.GraphSchemaVersion,
			Nodes: []workflow_template.GraphNode{
				{ID: "start", Type: workflow_block.TypeStart},
				{ID: "install", Type: "command.install_os", Params: installParams},
				{ID: "notify", Type: workflow_block.TypeHTTPRequest, Params: map[string]interface{}{
					"method":  "POST",
					"url":     "https://example.com/hook",
					"headers": "Authorization: Bearer {{secrets.hook_token}}",
				}},
			},
			Edges: []workflow_template.GraphEdge{
				{From: "start", To: "install"},
				{From: "install", To: "notify"},
			},
		},
	}
}

func importService(templateRepo *stubTemplateRepo, imageRepo repository.ImageRepository, secretRepo repository.SecretRepository) *Service {
	svc, _ := importServiceWithAudit(templateRepo, imageRepo, secretRepo)
	return svc
}

func importServiceWithAudit(templateRepo *stubTemplateRepo, imageRepo repository.ImageRepository, secretRepo repository.SecretRepository) (*Service, *audittest.Sink) {
	auditRecorder, sink := audittest.New()
	return &Service{
		templateRepo: templateRepo,
		imageRepo:    imageRepo,
		secretRepo:   secretRepo,
		audit:        auditRecorder,
		logger:       slog.Default(),
	}, sink
}

func TestImportCreatesDraftAndResolvesReferences(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()
	knownImage := uuid.New()

	templateRepo := &stubTemplateRepo{}
	svc := importService(templateRepo,
		&stubImageRepo{existing: map[uuid.UUID]bool{knownImage: true}},
		&stubSecretRepo{names: map[string]bool{"hook_token": true}},
	)

	cmd := workflow_template_import.WorkflowTemplateImportCommand{
		ID:          uuid.New(),
		Document:    importDocument(knownImage.String()),
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	created, warnings, err := svc.Import(context.Background(), cmd)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if created.Status != workflow_template.StatusDraft {
		t.Errorf("imported template status = %s, want draft", created.Status)
	}
	if created.AccountID != accountID {
		t.Errorf("account id not set")
	}
	if created.AuthorID == nil || *created.AuthorID != userID {
		t.Errorf("author id not stamped")
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if templateRepo.created == nil {
		t.Fatal("template was not persisted")
	}
	install, _ := templateRepo.created.Graph.NodeByID("install")
	if install.Params["image_id"] != knownImage.String() {
		t.Errorf("resolvable image reference must be kept, got %v", install.Params["image_id"])
	}
}

func TestImportClearsUnresolvableReferencesWithWarnings(t *testing.T) {
	accountID := uuid.New()

	templateRepo := &stubTemplateRepo{}
	svc := importService(templateRepo,
		&stubImageRepo{},  // no images exist
		&stubSecretRepo{}, // no secrets exist
	)

	cmd := workflow_template_import.WorkflowTemplateImportCommand{
		ID:          uuid.New(),
		Document:    importDocument(uuid.New().String()),
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}

	created, warnings, err := svc.Import(context.Background(), cmd)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	install, _ := created.Graph.NodeByID("install")
	if _, present := install.Params["image_id"]; present {
		t.Error("unresolvable image reference must be cleared")
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings (image + secret), got %v", warnings)
	}
	if !strings.Contains(warnings[0], "image_id") {
		t.Errorf("first warning should mention image_id: %s", warnings[0])
	}
	if !strings.Contains(warnings[1], "hook_token") {
		t.Errorf("second warning should mention the missing secret: %s", warnings[1])
	}
}

func TestImportRejectsInvalidDocuments(t *testing.T) {
	svc := importService(&stubTemplateRepo{}, nil, nil)

	badKind := importDocument("")
	badKind.Kind = "not.a.playbook"

	badGraph := importDocument("")
	badGraph.Graph.Nodes[1].Type = "command.no_such_block"

	for name, doc := range map[string]workflow_template.PortableDocument{
		"wrong kind":         badKind,
		"unknown block type": badGraph,
	} {
		cmd := workflow_template_import.WorkflowTemplateImportCommand{
			ID:          uuid.New(),
			Document:    doc,
			AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
		}
		_, _, err := svc.Import(context.Background(), cmd)
		if !errors.Is(err, app_errors.ErrWorkflowTemplateInvalid) {
			t.Errorf("%s: expected ErrWorkflowTemplateInvalid, got %v", name, err)
		}
	}
}
