package sql

import (
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The node repository is set up BEFORE the command repository at server
// start, so on a new install its Setup runs against a database that has no
// command table yet. A backfill in it that names that table refused the
// first boot of every fresh install while every existing one kept starting.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/ -run EmptyDatabase
func TestNodeRepositorySetupOnAnEmptyDatabase(t *testing.T) {
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	for _, table := range []string{"node_commands", "nodes"} {
		if err := db.Exec(`DROP TABLE IF EXISTS ` + table + ` CASCADE`).Error; err != nil {
			t.Fatalf("dropping %s: %v", table, err)
		}
	}
	// The tests that share this database expect the command table; put it
	// back whatever this one concludes.
	t.Cleanup(func() {
		if err := NewNodeCommandRepository(db, passthroughEncryptor{}).Setup(); err != nil {
			t.Errorf("restoring the command table: %v", err)
		}
	})

	if err := NewNodeRepository(db, passthroughEncryptor{}).Setup(); err != nil {
		t.Fatalf("Setup on a database with no command table: %v", err)
	}
	// And again once the command table exists, which is every later start.
	if err := NewNodeCommandRepository(db, passthroughEncryptor{}).Setup(); err != nil {
		t.Fatalf("command repository Setup: %v", err)
	}
	if err := NewNodeRepository(db, passthroughEncryptor{}).Setup(); err != nil {
		t.Fatalf("Setup with the command table present: %v", err)
	}
}
