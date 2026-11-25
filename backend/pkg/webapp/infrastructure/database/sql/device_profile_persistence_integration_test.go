package sql

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
)

// Both repositories write through EXPLICIT column lists (so nil policy
// pointers become NULL rather than being skipped), and both lists silently
// dropped `device_profile` when the field was added — the OU platform picker
// returned 200, echoed the in-memory object, and persisted nothing. A fake
// cannot catch a missed column; only the real statement can, so these
// round-trip set → read → clear → read through a real Postgres.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" \
//	  go test ./pkg/webapp/infrastructure/database/sql/ -run TestDeviceProfilePersistence
func deviceProfileTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

func testEquipage() *device_profile.Equipage {
	return &device_profile.Equipage{
		ProfileID: "compute-blade",
		Modules:   []device_profile.EquippedModule{{ModuleID: "ds3231", PortID: "blade-40pin"}},
	}
}

func TestDeviceProfilePersistenceOnOUUpdate(t *testing.T) {
	db := deviceProfileTestDB(t)
	repo := NewOrganizationalUnitRepository(db, crypto.NewEncryptor("test-key"))
	if err := repo.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	accountID := uuid.New()

	ou, err := organizational_unit.NewOrganizationalUnit(accountID, "persistence-probe", nil)
	if err != nil {
		t.Fatalf("new ou: %v", err)
	}
	ctx := context.Background()
	if err := repo.Create(ctx, ou); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, accountID, ou.ID) })

	ou.DeviceProfile = testEquipage()
	if err := repo.Update(ctx, ou); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := repo.GetByID(ctx, accountID, ou.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DeviceProfile == nil || got.DeviceProfile.ProfileID != "compute-blade" || len(got.DeviceProfile.Modules) != 1 {
		t.Fatalf("device_profile did not survive the OU update round-trip: %+v", got.DeviceProfile)
	}

	// Clearing must persist too — NULL means "inherit", and a clear that
	// silently keeps the old block is the same defect in the other direction.
	ou.DeviceProfile = nil
	if err := repo.Update(ctx, ou); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = repo.GetByID(ctx, accountID, ou.ID)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.DeviceProfile != nil {
		t.Fatalf("cleared device_profile still stored: %+v", got.DeviceProfile)
	}
}

func TestDeviceProfilePersistenceOnAccountPolicyUpsert(t *testing.T) {
	db := deviceProfileTestDB(t)
	repo := NewAccountPolicyRepository(db, crypto.NewEncryptor("test-key"))
	if err := repo.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	accountID := uuid.New()
	ctx := context.Background()
	t.Cleanup(func() {
		db.Exec("DELETE FROM account_policies WHERE account_id = ?", accountID)
	})

	// First write goes down the INSERT path and always worked; the defect was
	// the conflict path, so upsert twice.
	if err := repo.Upsert(ctx, &organizational_unit.AccountPolicy{AccountID: accountID}); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}
	if err := repo.Upsert(ctx, &organizational_unit.AccountPolicy{AccountID: accountID, DeviceProfile: testEquipage()}); err != nil {
		t.Fatalf("conflict-path upsert: %v", err)
	}
	got, err := repo.Get(ctx, accountID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.DeviceProfile == nil || got.DeviceProfile.ProfileID != "compute-blade" {
		t.Fatalf("device_profile did not survive the conflict-path upsert: %+v", got)
	}

	if err := repo.Upsert(ctx, &organizational_unit.AccountPolicy{AccountID: accountID}); err != nil {
		t.Fatalf("clearing upsert: %v", err)
	}
	got, err = repo.Get(ctx, accountID)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got != nil && got.DeviceProfile != nil {
		t.Fatalf("cleared device_profile still stored: %+v", got.DeviceProfile)
	}
}
