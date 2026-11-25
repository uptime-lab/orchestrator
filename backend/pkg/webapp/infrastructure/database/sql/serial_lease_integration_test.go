package sql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The lease's eligibility is one SQL predicate, and getting any clause of it
// wrong is not a compile error: too wide and an admitted device that has been
// offline for a month loses the identity its EEPROM still carries; too narrow
// and the cheapest squat holds forever. Hence a real Postgres:
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

func leaseRepo(t *testing.T) *NodeRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS nodes CASCADE`).Error; err != nil {
		t.Fatalf("dropping nodes: %v", err)
	}
	repo := NewNodeRepository(db, passthroughEncryptor{})
	if err := repo.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return repo
}

type leaseRow struct {
	name          string
	serial        string
	status        string // "" seeds 'offline'
	awaitingFirst bool
	approval      string
	lastSeen      *time.Time
	lastHeartbeat *time.Time
	createdAt     time.Time
	wantReleased  bool
}

func seedLeaseRow(t *testing.T, repo *NodeRepository, accountID uuid.UUID, row leaseRow) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var fp interface{}
	if row.serial != "" {
		fp = gorm.Expr("jsonb_build_object('hardware_serial', ?::text)", row.serial)
	}
	status := row.status
	if status == "" {
		status = "offline"
	}
	if err := repo.db.Exec(
		`INSERT INTO nodes (id, account_id, name, unique_token, status, approval_status, awaiting_first_connect, fingerprint, last_seen_at, last_heartbeat, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, now())`,
		id, accountID, row.name, uuid.New().String(), status, row.approval, row.awaitingFirst, fp, row.lastSeen, row.lastHeartbeat, row.createdAt).Error; err != nil {
		t.Fatalf("seeding %s: %v", row.name, err)
	}
	return id
}

func TestReleaseUnclaimedSerialLeasesEligibility(t *testing.T) {
	repo := leaseRepo(t)
	ctx := context.Background()
	account := uuid.New()
	now := time.Now()
	old := now.Add(-40 * 24 * time.Hour)
	fresh := now.Add(-time.Hour)
	cutoff := now.Add(-30 * 24 * time.Hour)

	rows := []leaseRow{
		// The cheapest squat: a WebUSB placeholder nobody ever connected. A real
		// placeholder sits in a pre-connect status.
		{name: "stale-placeholder", serial: "1000000000000001", status: "flashing", awaitingFirst: true, approval: "approved", createdAt: old, wantReleased: true},
		// Restored from an admitted tombstone: approved, offline and awaiting the
		// device's rebind. Admitted, so NEVER eligible — HTTP-boot finds the
		// device only by this serial.
		{name: "restored-admitted", serial: "1000000000000007", awaitingFirst: true, approval: "approved", createdAt: old},
		// A master-token adopt the operator never accepted and whose device went quiet.
		{name: "stale-pending", serial: "1000000000000002", approval: "pending_approval", lastSeen: &old, createdAt: old, wantReleased: true},
		// Still heartbeating while pending: the device is real and present.
		{name: "live-pending", serial: "1000000000000003", approval: "pending_approval", lastSeen: &old, lastHeartbeat: &fresh, createdAt: old},
		// A placeholder created last week: the hardware may still be in the post.
		{name: "young-placeholder", serial: "1000000000000004", status: "flashing", awaitingFirst: true, approval: "approved", createdAt: fresh},
		// Admitted and merely offline for a long time: NEVER eligible.
		{name: "admitted-offline", serial: "1000000000000005", approval: "approved", lastSeen: &old, createdAt: old},
		// Short serial: the guard does not protect it, so it cannot be squatted.
		{name: "short-placeholder", serial: "abcd0004", status: "flashing", awaitingFirst: true, approval: "approved", createdAt: old},
		// Rejected by the operator: their decision, not the sweep's.
		{name: "stale-rejected", serial: "1000000000000006", approval: "rejected", lastSeen: &old, createdAt: old},
		// Upper-case stored serial still matches the shape.
		{name: "stale-upper", serial: "ABCDEF0123456789", status: "ready_for_adoption", awaitingFirst: true, approval: "approved", createdAt: old, wantReleased: true},
	}
	ids := map[string]uuid.UUID{}
	for _, row := range rows {
		ids[row.name] = seedLeaseRow(t, repo, account, row)
	}

	released, err := repo.ReleaseUnclaimedSerialLeases(ctx, cutoff)
	if err != nil {
		t.Fatalf("ReleaseUnclaimedSerialLeases: %v", err)
	}
	got := map[uuid.UUID]string{}
	for _, r := range released {
		got[r.NodeID] = r.HardwareSerial
		if r.AccountID != account {
			t.Errorf("released row carries account %s, want %s", r.AccountID, account)
		}
	}
	for _, row := range rows {
		serial, was := got[ids[row.name]]
		if was != row.wantReleased {
			t.Errorf("%s: released=%v, want %v", row.name, was, row.wantReleased)
		}
		if was && serial == "" {
			t.Errorf("%s: the released serial must be reported (RETURNING must read the pre-update value)", row.name)
		}
	}
	if serial := got[ids["stale-upper"]]; serial != "abcdef0123456789" {
		t.Errorf("released serial must be normalized, got %q", serial)
	}

	// The rows survive; only the fingerprint is gone.
	for _, name := range []string{"stale-placeholder", "stale-pending"} {
		var count int64
		var fp *string
		if err := repo.db.Raw(`SELECT fingerprint FROM nodes WHERE id = ?`, ids[name]).Scan(&fp).Error; err != nil {
			t.Fatal(err)
		}
		repo.db.Raw(`SELECT count(*) FROM nodes WHERE id = ?`, ids[name]).Scan(&count)
		if count != 1 || fp != nil {
			t.Errorf("%s: row count %d, fingerprint %v; want 1 and NULL", name, count, fp)
		}
	}
	// Idempotent: a second sweep finds nothing.
	again, err := repo.ReleaseUnclaimedSerialLeases(ctx, cutoff)
	if err != nil || len(again) != 0 {
		t.Errorf("second sweep: got %d rows, err %v", len(again), err)
	}
}
