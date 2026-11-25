package sql

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
)

// The visibility set is nil-versus-empty, and collapsing the two hands an
// OU-scoped member the whole account's activity. The guard runs before the
// query is built, which is why a repository with no database can prove it: if
// the early return were removed this test would panic on the nil handle rather
// than quietly pass.
func TestListActivityRefusesAnEmptyVisibilitySet(t *testing.T) {
	// fakeEncryptor satisfies the constructor's required dependency; the
	// database handle stays nil because the guard must return before any query.
	repo := NewNodeCommandRepository(nil, &fakeEncryptor{})
	cmds, total, err := repo.ListActivity(context.Background(), uuid.New(),
		repository.NodeCommandActivityFilter{NodeIDs: []uuid.UUID{}}, 0, 50)
	if err != nil {
		t.Fatalf("ListActivity() error = %v, want nil", err)
	}
	if len(cmds) != 0 || total != 0 {
		t.Errorf("got %d rows (total %d), want nothing for a caller who may see no node", len(cmds), total)
	}
}
