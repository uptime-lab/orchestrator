package sql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The scheduler's per-account bound is a window function and the completion
// lookup is a three-column predicate; neither is expressible in a fake, and the
// failure mode of getting one wrong is not a compile error — it is a playbook
// that silently stops firing, or a workflow step that never advances. So they
// run against a real Postgres:
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

func scanTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	return db
}

func scheduledTemplateRepo(t *testing.T) *WorkflowTemplateRepository {
	t.Helper()
	db := scanTestDB(t)
	// The scope join table comes along: every read path fills the OU set, so a
	// schema with only workflow_templates fails at the first GetByIDs. This
	// listed the tables by hand and therefore did not follow the model — which
	// is exactly how it broke.
	if err := db.Exec(`DROP TABLE IF EXISTS workflow_template_ous, workflow_templates CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	if err := db.AutoMigrate(&WorkflowTemplateModel{}, &WorkflowTemplateOUModel{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return NewWorkflowTemplateRepository(db)
}

// seedTemplate writes a row with a chosen created_at, which is what the
// per-account cap orders by.
func seedTemplate(t *testing.T, repo *WorkflowTemplateRepository, accountID uuid.UUID, name, status, cron string, createdAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := repo.db.Exec(`INSERT INTO workflow_templates
		 (id, account_id, name, description, is_shortcut, status, steps, graph, schedule_cron, created_at, updated_at)
		 VALUES (?, ?, ?, '', false, ?, '[]'::jsonb, NULL, ?, ?, ?)`,
		id, accountID, name, status, cron, createdAt, createdAt).Error
	if err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
	return id
}

// The cap has to bind in the QUERY. Applying it in the caller would still pull
// everything a tenant owns into the scheduler's memory once a minute.
func TestListScheduledCandidatesBoundsEachAccountIndependently(t *testing.T) {
	repo := scheduledTemplateRepo(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-24 * time.Hour)
	noisy, quiet := uuid.New(), uuid.New()

	var noisyIDs []uuid.UUID
	for i := 0; i < 5; i++ {
		noisyIDs = append(noisyIDs, seedTemplate(t, repo, noisy, "noisy", "active", "* * * * *", base.Add(time.Duration(i)*time.Minute)))
	}
	quietID := seedTemplate(t, repo, quiet, "quiet", "active", "0 3 * * *", base)

	candidates, err := repo.ListScheduledCandidates(ctx, 2)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}

	perAccount := map[uuid.UUID][]uuid.UUID{}
	for _, c := range candidates {
		perAccount[c.AccountID] = append(perAccount[c.AccountID], c.ID)
	}
	if got := len(perAccount[noisy]); got != 2 {
		t.Errorf("the noisy account contributed %d candidates, want 2 — one tenant is not bounded", got)
	}
	// Oldest first, deterministically: the honored set must not change from
	// tick to tick, or a playbook fires only on the minutes it wins a race.
	if len(perAccount[noisy]) == 2 &&
		(perAccount[noisy][0] != noisyIDs[0] || perAccount[noisy][1] != noisyIDs[1]) {
		t.Errorf("the cap kept %v, want the two oldest %v", perAccount[noisy], noisyIDs[:2])
	}
	// A small account is not squeezed out by a large one.
	if len(perAccount[quiet]) != 1 || perAccount[quiet][0] != quietID {
		t.Errorf("the quiet account got %v, want its single playbook", perAccount[quiet])
	}
}

// Drafts and playbooks with no schedule must never reach the scan, capped or
// not: the cap counts what it returns, so anything spurious eats a slot a real
// scheduled playbook needs.
func TestListScheduledCandidatesExcludesDraftsAndUnscheduled(t *testing.T) {
	repo := scheduledTemplateRepo(t)
	ctx := context.Background()
	accountID := uuid.New()
	now := time.Now().UTC()

	wanted := seedTemplate(t, repo, accountID, "scheduled", "active", "*/5 * * * *", now)
	seedTemplate(t, repo, accountID, "draft", "draft", "*/5 * * * *", now)
	seedTemplate(t, repo, accountID, "manual", "active", "", now)

	for _, limit := range []int{0, 10} {
		candidates, err := repo.ListScheduledCandidates(ctx, limit)
		if err != nil {
			t.Fatalf("listing (limit %d): %v", limit, err)
		}
		if len(candidates) != 1 || candidates[0].ID != wanted {
			t.Fatalf("limit %d returned %+v, want only the active scheduled playbook", limit, candidates)
		}
		if candidates[0].Cron != "*/5 * * * *" || candidates[0].Name != "scheduled" {
			t.Errorf("limit %d lost projection fields: %+v", limit, candidates[0])
		}
	}
}

// A NULL schedule_cron (a row predating the column, before the backfill) must
// be excluded rather than surface as an empty-cron candidate.
func TestListScheduledCandidatesIgnoresNullCron(t *testing.T) {
	repo := scheduledTemplateRepo(t)
	accountID := uuid.New()
	id := uuid.New()
	err := repo.db.Exec(`INSERT INTO workflow_templates
		 (id, account_id, name, description, is_shortcut, status, steps, graph, schedule_cron, created_at, updated_at)
		 VALUES (?, ?, 'legacy', '', false, 'active', '[]'::jsonb, NULL, NULL, now(), now())`,
		id, accountID).Error
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	for _, limit := range []int{0, 5} {
		candidates, err := repo.ListScheduledCandidates(context.Background(), limit)
		if err != nil {
			t.Fatalf("listing (limit %d): %v", limit, err)
		}
		if len(candidates) != 0 {
			t.Errorf("limit %d returned %+v for a NULL cron", limit, candidates)
		}
	}
}

func TestGetByIDsReturnsWholeTemplates(t *testing.T) {
	repo := scheduledTemplateRepo(t)
	ctx := context.Background()
	accountID := uuid.New()
	now := time.Now().UTC()

	first := seedTemplate(t, repo, accountID, "first", "active", "* * * * *", now)
	second := seedTemplate(t, repo, accountID, "second", "active", "* * * * *", now)
	seedTemplate(t, repo, accountID, "third", "active", "* * * * *", now)

	templates, err := repo.GetByIDs(ctx, []uuid.UUID{first, second})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if len(templates) != 2 {
		t.Fatalf("loaded %d templates, want 2", len(templates))
	}
	for _, tpl := range templates {
		if tpl.ID != first && tpl.ID != second {
			t.Errorf("loaded an unrequested template %s", tpl.ID)
		}
		if tpl.AccountID != accountID || tpl.Name == "" {
			t.Errorf("template %s came back hollow: %+v", tpl.ID, tpl)
		}
	}

	empty, err := repo.GetByIDs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("GetByIDs(nil) = %v, %v; want no rows and no error", empty, err)
	}
}

// --- the device-command completion lookup ---------------------------------

func executionNodeRepoForScan(t *testing.T) *WorkflowExecutionNodeRepository {
	t.Helper()
	db := scanTestDB(t)
	if err := db.Exec(`DROP TABLE IF EXISTS workflow_execution_nodes CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	if err := db.AutoMigrate(&WorkflowExecutionNodeModel{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return NewWorkflowExecutionNodeRepository(db, passthroughEncryptor{})
}

func seedExecutionNode(t *testing.T, repo *WorkflowExecutionNodeRepository, accountID, nodeID uuid.UUID, status workflow_execution_node.WorkflowExecutionNodeStatus, commandID *uuid.UUID, createdAt time.Time) uuid.UUID {
	t.Helper()
	execNode := workflow_execution_node.NewWorkflowExecutionNode(uuid.New(), nodeID, accountID)
	execNode.Status = status
	execNode.CurrentCommandID = commandID
	if err := repo.Create(context.Background(), execNode); err != nil {
		t.Fatalf("seeding execution node: %v", err)
	}
	if err := repo.db.Exec(`UPDATE workflow_execution_nodes SET created_at = ? WHERE id = ?`, createdAt, execNode.ID).Error; err != nil {
		t.Fatalf("backdating execution node: %v", err)
	}
	return execNode.ID
}

func TestFindIDByCurrentCommandIDAnswersTheCompletionLookup(t *testing.T) {
	repo := executionNodeRepoForScan(t)
	ctx := context.Background()
	accountID, nodeID := uuid.New(), uuid.New()
	commandID := uuid.New()
	now := time.Now().UTC()

	waiting := seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusRunning, &commandID, now)
	other := uuid.New()
	seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusRunning, &other, now)
	seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusPending, nil, now)

	got, err := repo.FindIDByCurrentCommandID(ctx, accountID, nodeID, commandID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got != waiting {
		t.Errorf("found %s, want the run waiting on the command (%s)", got, waiting)
	}

	// The common case by far: a manual reboot or scan belongs to no run, and
	// the answer must be "none" rather than an error.
	none, err := repo.FindIDByCurrentCommandID(ctx, accountID, nodeID, uuid.New())
	if err != nil {
		t.Fatalf("lookup for an unrelated command: %v", err)
	}
	if none != uuid.Nil {
		t.Errorf("found %s for a command no run is waiting on", none)
	}
}

// Scoped by account AND node: the lookup is on the completion path of every
// device command in the instance, so a missing predicate would re-enqueue
// another tenant's run.
func TestFindIDByCurrentCommandIDIsScoped(t *testing.T) {
	repo := executionNodeRepoForScan(t)
	ctx := context.Background()
	accountID, nodeID := uuid.New(), uuid.New()
	commandID := uuid.New()
	seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusRunning, &commandID, time.Now().UTC())

	if got, _ := repo.FindIDByCurrentCommandID(ctx, uuid.New(), nodeID, commandID); got != uuid.Nil {
		t.Errorf("another account's run was returned: %s", got)
	}
	if got, _ := repo.FindIDByCurrentCommandID(ctx, accountID, uuid.New(), commandID); got != uuid.Nil {
		t.Errorf("another node's run was returned: %s", got)
	}
}

// Only unfinished rows: the finished ones are exactly what grows without bound
// on a node running a frequent playbook, and no caller wants them.
func TestGetActiveByNodeIDReturnsOnlyUnfinishedRuns(t *testing.T) {
	repo := executionNodeRepoForScan(t)
	ctx := context.Background()
	accountID, nodeID := uuid.New(), uuid.New()
	now := time.Now().UTC()

	running := seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusRunning, nil, now)
	pending := seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusPending, nil, now)
	seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusCompleted, nil, now)
	seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusFailed, nil, now)

	active, err := repo.GetActiveByNodeID(ctx, accountID, nodeID)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("returned %d rows, want the 2 unfinished ones", len(active))
	}
	seen := map[uuid.UUID]bool{}
	for _, n := range active {
		seen[n.ID] = true
	}
	if !seen[running] || !seen[pending] {
		t.Errorf("unfinished runs missing from %v", seen)
	}

	if other, _ := repo.GetActiveByNodeID(ctx, uuid.New(), nodeID); len(other) != 0 {
		t.Errorf("another account's runs were returned: %d", len(other))
	}
}

// UpdateActive is the atomic guard that stops a worker holding a pre-cancel copy
// from resurrecting a run that CancelExecution/AbandonNodeExecutions already
// settled. It must write while the stored row is pending/running and
// refuse — reporting wrote=false, changing nothing — once the row is terminal.
func TestUpdateActiveRefusesToResurrectASettledNode(t *testing.T) {
	repo := executionNodeRepoForScan(t)
	ctx := context.Background()
	accountID, nodeID := uuid.New(), uuid.New()
	now := time.Now().UTC()

	id := seedExecutionNode(t, repo, accountID, nodeID, workflow_execution_node.StatusRunning, nil, now)

	// A worker advancing a still-running node writes through.
	running, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	wrote, err := repo.UpdateActive(ctx, running)
	if err != nil {
		t.Fatalf("UpdateActive on a running row: %v", err)
	}
	if !wrote {
		t.Fatal("UpdateActive must write while the row is still running")
	}

	// A cancel settles it to a terminal state through the ordinary Update.
	settled, _ := repo.GetByID(ctx, id)
	settled.Fail("the run was cancelled by an operator")
	if err := repo.Update(ctx, settled); err != nil {
		t.Fatalf("settling: %v", err)
	}

	// The worker's stale copy — still 'running' in memory — now tries to advance.
	// UpdateActive must refuse and leave the terminal outcome intact.
	stale := running
	stale.Status = workflow_execution_node.StatusRunning
	wrote, err = repo.UpdateActive(ctx, stale)
	if err != nil {
		t.Fatalf("UpdateActive on a settled row: %v", err)
	}
	if wrote {
		t.Error("UpdateActive resurrected a settled node — the cancel would be a no-op")
	}
	after, _ := repo.GetByID(ctx, id)
	if after.Status != workflow_execution_node.StatusFailed {
		t.Errorf("settled node was overwritten to %s; want failed", after.Status)
	}
}
