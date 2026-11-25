package workflow_execution

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// stubChecker answers the fire-time authorization re-check.
type stubChecker struct {
	allow  bool
	asked  int
	action authz.Action
	user   uuid.UUID
}

func (c *stubChecker) Can(_ context.Context, _, userID uuid.UUID, action authz.Action, _ *uuid.UUID) bool {
	c.asked++
	c.action = action
	c.user = userID
	return c.allow
}

func templateWithBlocks(authorID *uuid.UUID, blockTypes ...string) workflow_template.WorkflowTemplate {
	graph := &workflow_template.WorkflowGraph{}
	for _, blockType := range blockTypes {
		graph.Nodes = append(graph.Nodes, workflow_template.GraphNode{
			ID:   uuid.New().String(),
			Type: blockType,
		})
	}
	return workflow_template.WorkflowTemplate{
		ID:        uuid.New(),
		AccountID: uuid.New(),
		AuthorID:  authorID,
		Graph:     graph,
		Status:    workflow_template.StatusActive,
	}
}

func schedulerFor(checker authzChecker) *Service {
	return &Service{authzChecker: checker, logger: slog.Default()}
}

func TestNonPrivilegedPlaybookFiresWithoutAnAuthorCheck(t *testing.T) {
	checker := &stubChecker{allow: false}
	svc := schedulerFor(checker)
	tpl := templateWithBlocks(nil, "command.restart")

	if !svc.authorMayStillFire(context.Background(), tpl) {
		t.Fatal("a playbook with no privileged block must fire regardless of its author")
	}
	if checker.asked != 0 {
		t.Errorf("expected no authorization check, got %d", checker.asked)
	}
}

func TestPrivilegedPlaybookFiresWhileItsAuthorKeepsTheRight(t *testing.T) {
	author := uuid.New()
	checker := &stubChecker{allow: true}
	svc := schedulerFor(checker)
	tpl := templateWithBlocks(&author, "command.restart", "command.run_script")

	if !svc.authorMayStillFire(context.Background(), tpl) {
		t.Fatal("expected the playbook to fire")
	}
	if checker.asked != 1 {
		t.Fatalf("expected exactly one authorization check, got %d", checker.asked)
	}
	if checker.action != authz.ActionPlaybookAuthorPrivileged {
		t.Errorf("checked action = %q, want %q", checker.action, authz.ActionPlaybookAuthorPrivileged)
	}
	if checker.user != author {
		t.Errorf("checked user = %s, want the author %s", checker.user, author)
	}
}

func TestPrivilegedPlaybookStopsWhenItsAuthorIsDemoted(t *testing.T) {
	author := uuid.New()
	svc := schedulerFor(&stubChecker{allow: false})
	tpl := templateWithBlocks(&author, "server.secret_upsert")

	if svc.authorMayStillFire(context.Background(), tpl) {
		t.Fatal("a demoted author's privileged playbook must not fire")
	}
}

func TestPrivilegedLegacyPlaybookWithNoAuthorIsFailClosed(t *testing.T) {
	svc := schedulerFor(&stubChecker{allow: true})
	tpl := templateWithBlocks(nil, "command.install_os")

	if svc.authorMayStillFire(context.Background(), tpl) {
		t.Fatal("a privileged playbook with no recorded author must not fire")
	}
}

func TestPrivilegedPlaybookIsFailClosedWithNoCheckerWired(t *testing.T) {
	author := uuid.New()
	svc := schedulerFor(nil)
	tpl := templateWithBlocks(&author, "integration.http_request")

	if svc.authorMayStillFire(context.Background(), tpl) {
		t.Fatal("without an authorization checker a privileged playbook must not fire")
	}
}

// A tick that runs longer than a minute skips minutes nothing else reports —
// the detection must count them and stay quiet on the normal cadence.
func TestMissedMinutes(t *testing.T) {
	base := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		prev time.Time
		now  time.Time
		want int
	}{
		{"first tick", time.Time{}, base, 0},
		{"same minute", base, base, 0},
		{"adjacent minute", base, base.Add(time.Minute), 0},
		{"one skipped", base, base.Add(2 * time.Minute), 1},
		{"five skipped", base, base.Add(6 * time.Minute), 5},
	}
	for _, tc := range cases {
		if got := missedMinutes(tc.prev, tc.now); got != tc.want {
			t.Errorf("%s: missedMinutes = %d, want %d", tc.name, got, tc.want)
		}
	}
}
