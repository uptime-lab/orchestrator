package workflow_template

import (
	"testing"

	"github.com/google/uuid"
)

// The snapshot carries the author so a scheduled run can name the member it
// acts for on the records it writes, frozen at start like the rest of the run.
func TestSnapshotCarriesTheAuthor(t *testing.T) {
	author := uuid.New()
	tpl := &WorkflowTemplate{ID: uuid.New(), Name: "p", AuthorID: &author}
	got := tpl.Snapshot().ToTemplate(uuid.New())
	if got.AuthorID == nil || *got.AuthorID != author {
		t.Fatalf("author lost through the snapshot: %v", got.AuthorID)
	}
}
