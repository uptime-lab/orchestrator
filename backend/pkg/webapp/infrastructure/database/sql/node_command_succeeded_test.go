package sql

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The install-history question reads one node's newest successful command
// among the disk-writing slugs, inside its account. Rendered without a
// database.
func TestLatestSucceededSlugByNodeStatement(t *testing.T) {
	db, capture := capturingDryRunDB(t)
	repo := &NodeCommandRepository{db: db}
	if _, err := repo.LatestSucceededSlugByNode(context.Background(), uuid.New(), uuid.New(), []string{"install_os", "erase_drive"}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(capture.stmt) != 1 {
		t.Fatalf("issued %d statements, want 1: %v", len(capture.stmt), capture.stmt)
	}
	got := capture.stmt[0]
	for _, want := range []string{`FROM "node_commands"`, "account_id =", "node_id =", "slug IN ('install_os','erase_drive')", "status = 'success'", "ORDER BY created_at DESC", "LIMIT 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("statement lacks %q: %s", want, got)
		}
	}
}
