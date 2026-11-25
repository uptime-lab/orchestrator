package access

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// countingOURepo answers every unit as owned and counts the reads.
type countingOURepo struct {
	repository.OrganizationalUnitRepository
	reads int
}

func (r *countingOURepo) GetByID(_ context.Context, accountID, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	r.reads++
	return &organizational_unit.OrganizationalUnit{ID: id, AccountID: accountID}, nil
}

// Ownership is asserted with one query per unit, so a request body repeating a
// unit id must not buy a round trip per repetition — the create paths pass the
// caller's list through verbatim.
func TestAssertTargetOwnedQueriesEachUnitOnce(t *testing.T) {
	ouRepo := &countingOURepo{}
	s := &Service{
		ouRepo: ouRepo,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	ouID := uuid.New()
	ouIDs := make([]uuid.UUID, 0, 100)
	for i := 0; i < 100; i++ {
		ouIDs = append(ouIDs, ouID)
	}

	if err := s.assertTargetOwned(context.Background(), uuid.New(), ouIDs, nil); err != nil {
		t.Fatalf("owned units must pass: %v", err)
	}
	if ouRepo.reads != 1 {
		t.Errorf("100 repeats of one unit id performed %d reads, want 1", ouRepo.reads)
	}
}
