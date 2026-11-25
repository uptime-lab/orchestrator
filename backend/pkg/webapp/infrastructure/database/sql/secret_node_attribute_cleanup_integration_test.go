package sql

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// DeleteNodeAttributes backs node deletion: it must take exactly the attribute
// secrets of THAT node (name attr:<nodeID>:*) and nothing else — not another
// node's attributes, not an ordinary secret, not another tenant's rows. Driven
// against real Postgres because the selection is a SQL LIKE on the reserved
// name prefix.
func TestDeleteNodeAttributesTakesOnlyThatNodesSecrets(t *testing.T) {
	repo := secretRepoForTest(t)
	ctx := context.Background()
	accountID, otherAccount := uuid.New(), uuid.New()
	nodeID, otherNode := uuid.New(), uuid.New()

	mustCreate := func(acct uuid.UUID, name string) uuid.UUID {
		t.Helper()
		sec, err := secret.NewSecret(acct, name, secret.KindEnv, "KEY", "value", false)
		if err != nil {
			t.Fatalf("new secret %q: %v", name, err)
		}
		if err := repo.Create(ctx, sec); err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
		return sec.ID
	}

	mustCreate(accountID, secret.NodeAttributeName(nodeID, "CI_TOKEN"))
	mustCreate(accountID, secret.NodeAttributeName(nodeID, "RACK_SLOT"))
	survivorOtherNode := mustCreate(accountID, secret.NodeAttributeName(otherNode, "CI_TOKEN"))
	survivorPlain := mustCreate(accountID, "ordinary-secret")
	// Same node id under another tenant: account scoping must hold even against
	// a name collision, because the delete runs with the deleting account's id.
	survivorOtherTenant := mustCreate(otherAccount, secret.NodeAttributeName(nodeID, "CI_TOKEN"))

	deleted, err := repo.DeleteNodeAttributes(ctx, accountID, nodeID)
	if err != nil {
		t.Fatalf("delete node attributes: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted %d rows, want 2", deleted)
	}

	for _, tc := range []struct {
		id   uuid.UUID
		acct uuid.UUID
		what string
	}{
		{survivorOtherNode, accountID, "another node's attribute"},
		{survivorPlain, accountID, "an ordinary secret"},
		{survivorOtherTenant, otherAccount, "another tenant's attribute"},
	} {
		if _, err := repo.GetByID(ctx, tc.acct, tc.id); err != nil {
			t.Errorf("%s did not survive: %v", tc.what, err)
		}
	}

	// Idempotent: a second sweep finds nothing and is not an error.
	deleted, err = repo.DeleteNodeAttributes(ctx, accountID, nodeID)
	if err != nil || deleted != 0 {
		t.Errorf("second sweep = (%d, %v), want (0, nil)", deleted, err)
	}
}
