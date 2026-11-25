package workflow_template

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// staleTemplateRepo embeds its interface so an unexpected call panics rather
// than quietly returning a zero value.
type staleTemplateRepo struct {
	repository.WorkflowTemplateRepository
	stored  *workflow_template.WorkflowTemplate
	updates int
	ouWrite []uuid.UUID
	ouCalls int
	// authorWrite records what the scope write did to the author column, which
	// is the half the re-target route used to drop.
	authorWrite *uuid.UUID
}

func (r *staleTemplateRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	copied := *r.stored
	return &copied, nil
}

func (r *staleTemplateRepo) Update(context.Context, *workflow_template.WorkflowTemplate) error {
	r.updates++
	return nil
}

func (r *staleTemplateRepo) UpdateOUIDs(_ context.Context, _, _ uuid.UUID, ouIDs []uuid.UUID, authorID *uuid.UUID) error {
	r.ouCalls++
	r.ouWrite = ouIDs
	r.authorWrite = authorID
	return nil
}

func staleFixture() (*Service, *staleTemplateRepo, uuid.UUID, uuid.UUID) {
	accountID, templateID := uuid.New(), uuid.New()
	stored := &workflow_template.WorkflowTemplate{
		ID:        templateID,
		AccountID: accountID,
		Name:      "nightly",
		Status:    workflow_template.StatusActive,
		UpdatedAt: time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
	repo := &staleTemplateRepo{stored: stored}
	auditRecorder, _ := audittest.New()
	svc := &Service{templateRepo: repo, audit: auditRecorder, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return svc, repo, accountID, templateID
}

func updateCmd(accountID, templateID uuid.UUID, expected *time.Time) workflow_template_update.WorkflowTemplateUpdateCommand {
	return workflow_template_update.WorkflowTemplateUpdateCommand{
		ID:   templateID,
		Name: "nightly",
		// One real step: Update validates the document, and this test is about
		// the precondition, not about validation.
		Steps: []workflow_template.WorkflowStep{{
			Order:   0,
			Command: workflow_template.CommandSpec{Slug: "reboot"},
		}},
		ExpectedUpdatedAt: expected,
		AuthContext:       commands.NewAuthContext(accountID, uuid.New()),
	}
}

// The point of the precondition: a save built on a copy the other operator has
// already superseded is refused instead of reverting their work.
func TestUpdateRefusesAStalePrecondition(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	stale := repo.stored.UpdatedAt.Add(-time.Hour)

	_, err := svc.Update(context.Background(), updateCmd(accountID, templateID, &stale), nil)

	if !errors.Is(err, app_errors.ErrWorkflowTemplateStale) {
		t.Fatalf("Update() = %v, want ErrWorkflowTemplateStale", err)
	}
	if repo.updates != 0 {
		t.Errorf("updates = %d, want 0: a refused save must not write", repo.updates)
	}
}

// Sub-second representation differences must not refuse every save: the column
// round-trips through Postgres and JSON.
func TestUpdateAcceptsAMatchingPreconditionAcrossSubSecondNoise(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	noisy := repo.stored.UpdatedAt.Add(400 * time.Millisecond)

	if _, err := svc.Update(context.Background(), updateCmd(accountID, templateID, &noisy), nil); err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}
	if repo.updates != 1 {
		t.Errorf("updates = %d, want 1", repo.updates)
	}
}

// Omitting it keeps the old contract: last-write-wins, so existing clients are
// not broken by the new field.
func TestUpdateWithoutAPreconditionStillWrites(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()

	if _, err := svc.Update(context.Background(), updateCmd(accountID, templateID, nil), nil); err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}
	if repo.updates != 1 {
		t.Errorf("updates = %d, want 1", repo.updates)
	}
}

// Re-targeting writes the scope column and never the document.
func TestSetOUWritesOnlyTheScope(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	target := uuid.New()

	if _, err := svc.SetOUs(context.Background(), accountID, uuid.New(), templateID, []uuid.UUID{target}, nil); err != nil {
		t.Fatalf("SetOU() = %v, want nil", err)
	}
	if repo.updates != 0 {
		t.Errorf("full-document updates = %d, want 0", repo.updates)
	}
	if repo.ouCalls != 1 || len(repo.ouWrite) != 1 || repo.ouWrite[0] != target {
		t.Errorf("scope write = %v after %d calls, want %s", repo.ouWrite, repo.ouCalls, target)
	}
}
