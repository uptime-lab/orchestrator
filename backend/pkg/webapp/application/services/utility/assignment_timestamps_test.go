package utility

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// storingAssignments stores what it is given and, like the database, never
// writes a time back into the caller's entity.
type storingAssignments struct {
	settleFakeAssignments
	stored *utility.Assignment
}

func (f *storingAssignments) CountByAccount(context.Context, uuid.UUID) (int64, error) { return 0, nil }

func (f *storingAssignments) Create(_ context.Context, a *utility.Assignment) error {
	cp := *a
	f.stored = &cp
	return nil
}

func (f *storingAssignments) GetByID(context.Context, uuid.UUID, uuid.UUID) (*utility.Assignment, error) {
	cp := *f.stored
	return &cp, nil
}

func (f *storingAssignments) Update(_ context.Context, a *utility.Assignment) error {
	cp := *a
	f.stored = &cp
	return nil
}

// POST /utilities/assignments answered created_at and updated_at as
// 0001-01-01: the times the row got from its column defaults never reached
// the answer. The entity the service returns carries the stored times, and an
// update moves updated_at only.
func TestAssignmentAnswersCarryTheStoredTimes(t *testing.T) {
	recorder, _ := audittest.New()
	repo := &storingAssignments{}
	svc := &Service{
		catalog:     &utility.Catalog{Utilities: []utility.Definition{{ID: "docker", Name: "Docker"}}},
		assignments: repo,
		nodeRepo:    &settleFakeNodeRepo{},
		audit:       recorder,
		logger:      slog.Default(),
	}
	accountID, userID := uuid.New(), uuid.New()
	before := time.Now().Add(-time.Second)

	a, err := svc.CreateAssignment(context.Background(), accountID, userID, AssignmentInput{UtilityID: "docker", AccountWide: true})
	if err != nil {
		t.Fatalf("CreateAssignment: %v", err)
	}
	if a.CreatedAt.Before(before) || a.UpdatedAt.Before(before) {
		t.Fatalf("created answer carries created_at=%s updated_at=%s", a.CreatedAt, a.UpdatedAt)
	}
	if !repo.stored.CreatedAt.Equal(a.CreatedAt) {
		t.Fatalf("the answer's created_at %s is not the stored %s", a.CreatedAt, repo.stored.CreatedAt)
	}

	created := a.CreatedAt
	enabled := false
	time.Sleep(2 * time.Millisecond)
	u, err := svc.UpdateAssignment(context.Background(), accountID, userID, a.ID, AssignmentUpdate{Enabled: &enabled})
	if err != nil {
		t.Fatalf("UpdateAssignment: %v", err)
	}
	if !u.CreatedAt.Equal(created) || !u.UpdatedAt.After(created) {
		t.Fatalf("update answer: created_at=%s (want %s) updated_at=%s (want later)", u.CreatedAt, created, u.UpdatedAt)
	}
}
