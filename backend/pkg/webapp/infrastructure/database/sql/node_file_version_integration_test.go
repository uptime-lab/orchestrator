package sql

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Version allocation and the dedupe migration are expressed in SQL, and SQL is
// where this feature's live bugs have come from — a subquery aliased `inner`
// (a reserved word) broke the Files list against a real Postgres while every
// unit test passed, because the fakes are not a database. These run against a real
// Postgres:
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/
func versionTestRepo(t *testing.T) (*NodeFileRepository, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true, // what turns a unique violation into gorm.ErrDuplicatedKey
	})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	stmts := []string{
		`DROP TABLE IF EXISTS node_files, nodes CASCADE`,
		`CREATE TABLE nodes (id uuid PRIMARY KEY, account_id uuid NOT NULL)`,
		`CREATE TABLE node_files (
		   id uuid PRIMARY KEY, account_id uuid, node_id uuid NOT NULL,
		   path text NOT NULL, content text NOT NULL DEFAULT '', hash text NOT NULL DEFAULT '',
		   content_enc int NOT NULL DEFAULT 0, version int NOT NULL,
		   status text NOT NULL DEFAULT 'applied', error_message text,
		   created_at timestamptz NOT NULL DEFAULT now(),
		   updated_at timestamptz NOT NULL DEFAULT now())`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	nodeID := uuid.New()
	if err := db.Exec(`INSERT INTO nodes (id, account_id) VALUES (?, ?)`, nodeID, uuid.New()).Error; err != nil {
		t.Fatalf("seeding node: %v", err)
	}
	return NewNodeFileRepository(db, nil), nodeID
}

// seedVersion writes a row directly, bypassing allocation, so a test can create
// the tie the migration exists to resolve.
func seedVersion(t *testing.T, r *NodeFileRepository, nodeID uuid.UUID, path string, version int, content string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := r.db.Exec(`INSERT INTO node_files (id, node_id, path, content, hash, version) VALUES (?, ?, ?, ?, ?, ?)`,
		id, nodeID, path, content, content, version).Error
	if err != nil {
		t.Fatalf("seeding version %d: %v", version, err)
	}
	return id
}

func TestCreateNextVersionAllocatesSequentially(t *testing.T) {
	repo, nodeID := versionTestRepo(t)
	ctx := context.Background()

	for want := 1; want <= 3; want++ {
		file := node_file.NewNodeFile(nodeID, "/etc/hosts", "body", "hash", 0, node_file.StatusSaved)
		if err := repo.CreateNextVersion(ctx, file); err != nil {
			t.Fatalf("creating version: %v", err)
		}
		if file.Version != want {
			t.Fatalf("allocated version %d, want %d", file.Version, want)
		}
	}
}

// Two paths of the same node allocate independently: the maximum is per path,
// not per node.
func TestCreateNextVersionIsPerPath(t *testing.T) {
	repo, nodeID := versionTestRepo(t)
	ctx := context.Background()

	first := node_file.NewNodeFile(nodeID, "/etc/hosts", "a", "a", 0, node_file.StatusSaved)
	if err := repo.CreateNextVersion(ctx, first); err != nil {
		t.Fatalf("creating: %v", err)
	}
	second := node_file.NewNodeFile(nodeID, "/etc/fstab", "b", "b", 0, node_file.StatusSaved)
	if err := repo.CreateNextVersion(ctx, second); err != nil {
		t.Fatalf("creating: %v", err)
	}
	if second.Version != 1 {
		t.Fatalf("second path allocated version %d, want 1", second.Version)
	}
}

func TestCreateNextVersionRejectsUnknownNode(t *testing.T) {
	repo, _ := versionTestRepo(t)
	file := node_file.NewNodeFile(uuid.New(), "/etc/hosts", "body", "hash", 0, node_file.StatusSaved)
	if err := repo.CreateNextVersion(context.Background(), file); err == nil {
		t.Fatal("expected an error for a node that does not exist")
	}
}

// The point of the whole change: concurrent saves of ONE path must not tie.
// Without the index every writer reads the same maximum and they all claim the
// same number; with it, the losers retry and the numbers come out distinct.
func TestConcurrentCreatesDoNotTie(t *testing.T) {
	repo, nodeID := versionTestRepo(t)
	if _, err := repo.EnsureVersionUniqueIndex(); err != nil {
		t.Fatalf("creating the index: %v", err)
	}

	const writers = 5
	var wg sync.WaitGroup
	versions := make([]int, writers)
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			file := node_file.NewNodeFile(nodeID, "/etc/hosts", "body", "hash", 0, node_file.StatusSaved)
			if err := repo.CreateNextVersion(context.Background(), file); err != nil {
				errs[i] = err
				return
			}
			versions[i] = file.Version
		}(i)
	}
	wg.Wait()

	seen := map[int]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
		if seen[versions[i]] {
			t.Fatalf("version %d was allocated twice", versions[i])
		}
		seen[versions[i]] = true
	}
}

// A live table can already hold a tie, which is why the index cannot be a
// struct tag: AutoMigrate would fail on it every start.
func TestEnsureVersionUniqueIndexResolvesExistingTies(t *testing.T) {
	repo, nodeID := versionTestRepo(t)

	seedVersion(t, repo, nodeID, "/etc/hosts", 1, "v1")
	keeps := seedVersion(t, repo, nodeID, "/etc/hosts", 2, "first-of-the-tie")
	moves := seedVersion(t, repo, nodeID, "/etc/hosts", 2, "second-of-the-tie")
	seedVersion(t, repo, nodeID, "/etc/fstab", 1, "untouched")

	renumbered, err := repo.EnsureVersionUniqueIndex()
	if err != nil {
		t.Fatalf("migrating: %v", err)
	}
	if renumbered != 1 {
		t.Fatalf("renumbered %d rows, want 1", renumbered)
	}

	versionOf := func(id uuid.UUID) int {
		var v int
		if err := repo.db.Raw(`SELECT version FROM node_files WHERE id = ?`, id).Scan(&v).Error; err != nil {
			t.Fatalf("reading back: %v", err)
		}
		return v
	}
	// The oldest row of the tie keeps the contested number; the other moves
	// above the path's maximum rather than renumbering untouched history.
	if got := versionOf(keeps); got != 2 {
		t.Fatalf("the first row of the tie moved to %d, want it to keep 2", got)
	}
	if got := versionOf(moves); got != 3 {
		t.Fatalf("the second row of the tie became %d, want 3", got)
	}

	// And the index is in place afterwards, which is the whole point: the
	// migration ran BEFORE it, so creation could not fail on the tie.
	var indexes int64
	if err := repo.db.Raw(`SELECT count(*) FROM pg_indexes WHERE tablename = 'node_files' AND indexname = ?`,
		versionUniqueIndexName).Scan(&indexes).Error; err != nil {
		t.Fatalf("checking the index: %v", err)
	}
	if indexes != 1 {
		t.Fatalf("found %d matching indexes, want 1", indexes)
	}
}

// Running it twice must be a no-op, because it runs on every backend start.
func TestEnsureVersionUniqueIndexIsIdempotent(t *testing.T) {
	repo, nodeID := versionTestRepo(t)
	seedVersion(t, repo, nodeID, "/etc/hosts", 1, "v1")
	seedVersion(t, repo, nodeID, "/etc/hosts", 1, "tied")

	if _, err := repo.EnsureVersionUniqueIndex(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	renumbered, err := repo.EnsureVersionUniqueIndex()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if renumbered != 0 {
		t.Fatalf("second run renumbered %d rows, want 0", renumbered)
	}
}
