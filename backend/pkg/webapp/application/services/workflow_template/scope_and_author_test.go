package workflow_template

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// A full-document PUT that carries no scope field must leave the scope ALONE.
//
// The same method already treats an omitted GRAPH as unchanged, and for exactly
// the client this hurt: an old integration or a script that assembles the
// document by hand and holds no scope. Scope got no such treatment, so
// `existingTemplate.OUIDs = cmd.ScopeOUIDs()` wrote nil over the join table and
// a playbook scoped to one unit became fleet-wide. On a cron playbook that is
// the next minute's target set — silently, with nothing reporting it.
func TestUpdateWithNoScopeFieldLeavesTheScopeAlone(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	scoped := uuid.New()
	repo.stored.OUIDs = []uuid.UUID{scoped}

	cmd := updateCmd(accountID, templateID, nil) // carries neither ou_id nor ou_ids
	updated, err := svc.Update(context.Background(), cmd, nil)
	if err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}
	if len(updated.OUIDs) != 1 || updated.OUIDs[0] != scoped {
		t.Fatalf("scope = %v after a PUT that never mentioned it, want [%s] unchanged; the playbook just went fleet-wide", updated.OUIDs, scoped)
	}
}

// An EXPLICIT empty set still unscopes. That is a caller saying so, the editor
// sends the field on every save, and turning it into "unchanged" would make
// unscoping impossible through the document route.
func TestUpdateWithAnExplicitEmptyScopeUnscopes(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	repo.stored.OUIDs = []uuid.UUID{uuid.New()}

	cmd := updateCmd(accountID, templateID, nil)
	cmd.OUIDs = []uuid.UUID{} // present and empty, which JSON distinguishes from absent

	updated, err := svc.Update(context.Background(), cmd, nil)
	if err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}
	if len(updated.OUIDs) != 0 {
		t.Fatalf("scope = %v after an explicit empty ou_ids, want unscoped", updated.OUIDs)
	}
}

// A submitted scope still replaces the stored one.
func TestUpdateWithASubmittedScopeReplacesIt(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	repo.stored.OUIDs = []uuid.UUID{uuid.New()}
	target := uuid.New()

	cmd := updateCmd(accountID, templateID, nil)
	cmd.OUIDs = []uuid.UUID{target}

	updated, err := svc.Update(context.Background(), cmd, nil)
	if err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}
	if len(updated.OUIDs) != 1 || updated.OUIDs[0] != target {
		t.Fatalf("scope = %v, want [%s]", updated.OUIDs, target)
	}
}

// The privileged-authoring gate must see the scope that will be STORED.
//
// The merge is what makes this a real question: with an omitted scope the
// submission says "no units" and the stored document says otherwise, and a check
// written against the submission authorizes a document that is not the one being
// saved.
func TestUpdateAuthorizesTheMergedScopeNotTheSubmittedOne(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	scoped := uuid.New()
	repo.stored.OUIDs = []uuid.UUID{scoped}

	var sawOUs []uuid.UUID
	authorize := func(_ *workflow_template.WorkflowGraph, _ []workflow_template.WorkflowStep, ouIDs []uuid.UUID) error {
		sawOUs = ouIDs
		return nil
	}

	if _, err := svc.Update(context.Background(), updateCmd(accountID, templateID, nil), authorize); err != nil {
		t.Fatalf("Update() = %v, want nil", err)
	}
	if len(sawOUs) != 1 || sawOUs[0] != scoped {
		t.Fatalf("the authoring gate saw %v, want the merged scope [%s]", sawOUs, scoped)
	}
}

// Re-targeting must PERSIST the new author, not only report one.
//
// The scheduler re-checks the AUTHOR's privileged-authoring right before every
// unattended fire. UpdateOUIDs wrote ou_id and updated_at, and the service set
// AuthorID on the copy it returned — so a privileged cron playbook whose author
// had since lost playbook.author_privileged stayed permanently silent after an
// admin re-targeted it to fix exactly that, while the response and the audit row
// both said it had a new author.
func TestSetOUsPersistsTheNewAuthor(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()
	editor := uuid.New()

	if _, err := svc.SetOUs(context.Background(), accountID, editor, templateID, []uuid.UUID{uuid.New()}, nil); err != nil {
		t.Fatalf("SetOUs() = %v, want nil", err)
	}
	if repo.authorWrite == nil {
		t.Fatal("the re-target wrote no author; the scheduler keeps re-checking the old one, and the audit row says otherwise")
	}
	if *repo.authorWrite != editor {
		t.Fatalf("author written = %s, want the operator who re-targeted (%s)", *repo.authorWrite, editor)
	}
}

// A system path with no user must not blank the column: nil means "leave it".
func TestSetOUsFromASystemContextLeavesTheAuthorAlone(t *testing.T) {
	svc, repo, accountID, templateID := staleFixture()

	if _, err := svc.SetOUs(context.Background(), accountID, uuid.Nil, templateID, []uuid.UUID{uuid.New()}, nil); err != nil {
		t.Fatalf("SetOUs() = %v, want nil", err)
	}
	if repo.authorWrite != nil {
		t.Fatalf("author written = %s from a system context, want no write at all", *repo.authorWrite)
	}
}
