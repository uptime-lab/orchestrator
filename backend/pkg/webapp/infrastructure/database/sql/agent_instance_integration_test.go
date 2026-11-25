package sql

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func agentInstanceRepo(t *testing.T) *NodeRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	// A previous package run may have left a minimal hand-built `nodes` with
	// rows AutoMigrate cannot add NOT NULL columns over — start from nothing.
	if err := db.Exec(`DROP TABLE IF EXISTS nodes CASCADE`).Error; err != nil {
		t.Fatalf("dropping nodes: %v", err)
	}
	if err := db.AutoMigrate(&NodeModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewNodeRepository(db, passthroughEncryptor{})
}

func seedNodeRow(t *testing.T, repo *NodeRepository) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := repo.db.Exec(
		`INSERT INTO nodes (id, account_id, name, unique_token, status, approval_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'offline', 'approved', now(), now())`,
		id, uuid.New(), "instance-test-"+id.String()[:8], uuid.New().String()).Error; err != nil {
		t.Fatalf("seeding node: %v", err)
	}
	return id
}

// The rotation is the ONLY writer of agent_instance_id, and its two rules are
// what the continuity verdict rests on: it returns what the PREVIOUS session
// reported, and an empty report never erases a stored id — "this agent does
// not report" must stay distinguishable from "a new process reported".
func TestRotateAgentInstance(t *testing.T) {
	repo := agentInstanceRepo(t)
	ctx := context.Background()
	nodeID := seedNodeRow(t, repo)

	// First reporting connect: nothing stored yet.
	prev, err := repo.RotateAgentInstance(ctx, nodeID, "proc-a")
	if err != nil {
		t.Fatalf("first rotate: %v", err)
	}
	if prev != "" {
		t.Errorf("first rotate returned %q, want empty", prev)
	}

	// The same process reconnecting: previous is itself.
	if prev, _ = repo.RotateAgentInstance(ctx, nodeID, "proc-a"); prev != "proc-a" {
		t.Errorf("same-process rotate returned %q, want proc-a", prev)
	}

	// An old agent (no id) connects: read-only, the stored id must survive.
	if prev, _ = repo.RotateAgentInstance(ctx, nodeID, ""); prev != "proc-a" {
		t.Errorf("empty rotate returned %q, want proc-a", prev)
	}

	// A restarted process: previous is the old id, and the new one is stored.
	if prev, _ = repo.RotateAgentInstance(ctx, nodeID, "proc-b"); prev != "proc-a" {
		t.Errorf("restart rotate returned %q, want proc-a", prev)
	}
	if prev, _ = repo.RotateAgentInstance(ctx, nodeID, "proc-b"); prev != "proc-b" {
		t.Errorf("post-restart rotate returned %q, want proc-b", prev)
	}
}
