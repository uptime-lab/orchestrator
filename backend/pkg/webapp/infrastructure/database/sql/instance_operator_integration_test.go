package sql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// The instance operator is the oldest account, the id breaking a tie, so
// every read names the same one account; an install with none says so.
func TestInstanceOperatorIsTheOldestAccount(t *testing.T) {
	repo := accountCeilingRepo(t)
	ctx := context.Background()

	if _, err := repo.InstanceOperatorAccountID(ctx); !errors.Is(err, app_errors.ErrAccountNotFound) {
		t.Fatalf("empty install: err = %v, want ErrAccountNotFound", err)
	}

	first := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	lowID := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	highID := uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff")
	rows := []AccountModel{
		{ID: uuid.New(), Name: "tenant", CreatedAt: first.Add(time.Hour), UpdatedAt: first},
		{ID: highID, Name: "tie-high", CreatedAt: first, UpdatedAt: first},
		{ID: lowID, Name: "tie-low", CreatedAt: first, UpdatedAt: first},
	}
	for i := range rows {
		if err := repo.db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	got, err := repo.InstanceOperatorAccountID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != lowID {
		t.Errorf("operator = %s, want the oldest account with the lowest id %s", got, lowID)
	}
}
