package secret_tag

import (
	"context"
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret_tag"
	"github.com/google/uuid"
)

// fakeTagRepo is an in-memory SecretTagRepository.
type fakeTagRepo struct {
	rows      map[string]secret_tag.Tag // keyed by name
	renamedTo string
	deleted   string
	nameTaken map[string]bool
}

var _ repository.SecretTagRepository = (*fakeTagRepo)(nil)

func newFakeTagRepo() *fakeTagRepo {
	return &fakeTagRepo{rows: map[string]secret_tag.Tag{}, nameTaken: map[string]bool{}}
}

func (f *fakeTagRepo) List(_ context.Context, _ uuid.UUID) ([]secret_tag.Tag, error) {
	out := make([]secret_tag.Tag, 0, len(f.rows))
	for _, t := range f.rows {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeTagRepo) GetByName(_ context.Context, _ uuid.UUID, name string) (*secret_tag.Tag, error) {
	if t, ok := f.rows[name]; ok {
		return &t, nil
	}
	return nil, app_errors.ErrSecretTagNotFound
}

func (f *fakeTagRepo) Upsert(_ context.Context, tag *secret_tag.Tag) error {
	f.rows[tag.Name] = *tag
	return nil
}

func (f *fakeTagRepo) Rename(_ context.Context, _ uuid.UUID, oldName, newName string) error {
	if f.nameTaken[newName] {
		return app_errors.ErrSecretTagNameTaken
	}
	if t, ok := f.rows[oldName]; ok {
		delete(f.rows, oldName)
		t.Name = newName
		f.rows[newName] = t
	}
	f.renamedTo = newName
	return nil
}

func (f *fakeTagRepo) Delete(_ context.Context, _ uuid.UUID, name string) error {
	delete(f.rows, name)
	f.deleted = name
	return nil
}

func newService() (*Service, *fakeTagRepo) {
	repo := newFakeTagRepo()
	return NewService(repo, slog.Default()), repo
}

func TestUpsertCreatesAndRecolors(t *testing.T) {
	svc, repo := newService()
	acc := uuid.New()

	tag, err := svc.Upsert(context.Background(), acc, "Infra", "blue")
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if tag.Name != "infra" || tag.Color != "blue" {
		t.Fatalf("unexpected tag: %+v", tag)
	}
	if repo.rows["infra"].Color != "blue" {
		t.Fatal("expected row persisted with color")
	}

	// Recolor.
	if _, err := svc.Upsert(context.Background(), acc, "infra", "lime"); err != nil {
		t.Fatalf("recolor: %v", err)
	}
	if repo.rows["infra"].Color != "lime" {
		t.Errorf("expected recolored to lime, got %q", repo.rows["infra"].Color)
	}
}

func TestUpsertRejectsInvalidColor(t *testing.T) {
	svc, _ := newService()
	if _, err := svc.Upsert(context.Background(), uuid.New(), "infra", "neon"); err != secret_tag.ErrInvalidColor {
		t.Fatalf("expected ErrInvalidColor, got %v", err)
	}
}

func TestRenameCascadesAndRejectsTaken(t *testing.T) {
	svc, repo := newService()
	acc := uuid.New()
	if _, err := svc.Upsert(context.Background(), acc, "old", "blue"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := svc.Rename(context.Background(), acc, "old", "New-Name"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, ok := repo.rows["new-name"]; !ok {
		t.Fatalf("expected renamed row, rows=%v", repo.rows)
	}

	repo.nameTaken["taken"] = true
	if err := svc.Rename(context.Background(), acc, "new-name", "taken"); err != app_errors.ErrSecretTagNameTaken {
		t.Fatalf("expected ErrSecretTagNameTaken, got %v", err)
	}
}

func TestDeleteNormalizesName(t *testing.T) {
	svc, repo := newService()
	acc := uuid.New()
	if _, err := svc.Upsert(context.Background(), acc, "infra", "blue"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := svc.Delete(context.Background(), acc, "  Infra  "); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if repo.deleted != "infra" {
		t.Fatalf("expected normalized delete of 'infra', got %q", repo.deleted)
	}
}
