package sql

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The projection is the boundary: it must return the requesting account's
// rows only, read the serial out of the jsonb, and carry nothing about the
// owner. Expressed in SQL, so tested against Postgres:
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

func anomalyRepoForTest(t *testing.T) *InstanceAnomalyRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS instance_anomalies CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	repo := NewInstanceAnomalyRepository(db)
	if err := repo.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return repo
}

func TestListSerialBindingRefusalsProjectsOnlyTheAskingAccountsRows(t *testing.T) {
	repo := anomalyRepoForTest(t)
	ctx := context.Background()
	mine, other, owner := uuid.New(), uuid.New(), uuid.New()

	refusal := func(serial string, requesting uuid.UUID) *instance_anomaly.Anomaly {
		return instance_anomaly.New(instance_anomaly.KindSerialBindingConflict,
			instance_anomaly.SerialConflictKey(serial, requesting, owner),
			instance_anomaly.SeverityWarning, "refused",
			map[string]interface{}{
				"hardware_serial":       serial,
				"requesting_account_id": requesting.String(),
				"owner_account_id":      owner.String(),
				"owner_node_id":         uuid.New().String(),
			}, &requesting)
	}
	// Two refusals for my account, one folded twice; one for another account;
	// one of a different kind for my account that carries a serial-looking
	// detail and must not leak in.
	if err := repo.Record(ctx, refusal("1000000000000001", mine)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Record(ctx, refusal("1000000000000001", mine)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := repo.Record(ctx, refusal("1000000000000002", mine)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Record(ctx, refusal("1000000000000003", other)); err != nil {
		t.Fatal(err)
	}
	dup := instance_anomaly.New(instance_anomaly.KindSerialDuplicated, "dup-key", instance_anomaly.SeverityWarning, "dup",
		map[string]interface{}{"hardware_serial": "1000000000000004"}, &mine)
	if err := repo.Record(ctx, dup); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListSerialBindingRefusals(ctx, mine, 50)
	if err != nil {
		t.Fatalf("ListSerialBindingRefusals: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	// Newest activity first.
	if got[0].HardwareSerial != "1000000000000002" || got[1].HardwareSerial != "1000000000000001" {
		t.Errorf("order: got %s, %s", got[0].HardwareSerial, got[1].HardwareSerial)
	}
	if got[1].OccurrenceCount != 2 {
		t.Errorf("folded row must carry its count, got %d", got[1].OccurrenceCount)
	}
	if got[1].FirstSeenAt.After(got[1].LastSeenAt) {
		t.Errorf("first_seen %v after last_seen %v", got[1].FirstSeenAt, got[1].LastSeenAt)
	}

	if none, err := repo.ListSerialBindingRefusals(ctx, uuid.New(), 50); err != nil || len(none) != 0 {
		t.Errorf("an account with no refusals: got %v, %v", none, err)
	}
}
