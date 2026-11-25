package sql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// seedBootRow inserts a node with a stored serial and, optionally, the
// HTTP_PATH its agent read back from the EEPROM.
func seedBootRow(t *testing.T, repo *NodeRepository, accountID uuid.UUID, serial, bootPath string, createdAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	systemInfo := `{}`
	if bootPath != "" {
		systemInfo = `{"hardware":{"boot":{"boot_http_path":"` + bootPath + `"}}}`
	}
	if err := repo.db.Exec(
		`INSERT INTO nodes (id, account_id, name, unique_token, status, approval_status, fingerprint, system_info, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'offline', 'approved', jsonb_build_object('hardware_serial', ?::text), ?::jsonb, ?, now())`,
		id, accountID, "n-"+id.String()[:8], uuid.New().String(), serial, systemInfo, createdAt).Error; err != nil {
		t.Fatalf("seeding %s: %v", serial, err)
	}
	return id
}

// The boot lookup must see the short exact stamp AND the full-serial rows that
// end in it in one candidate set: an exact stamp in one account used to hide
// the suffix row of the account whose device was actually asking. Run with
// BACKEND_TEST_DSN (see serial_lease_integration_test.go).
func TestGetByHardwareSerialGlobalWeighsExactAndSuffixTogether(t *testing.T) {
	repo := leaseRepo(t)
	ctx := context.Background()
	owner, other := uuid.New(), uuid.New()
	now := time.Now()

	device := seedBootRow(t, repo, owner, "10000000abcd0001", "http-boot/abcd0001", now.Add(-time.Hour))
	// Newer, exact, in another account, no readback: a placeholder stamp. Not
	// admitted — another account's ADMITTED node with no readback would make
	// the request ambiguous rather than lose it to a readback from elsewhere.
	squat := seedBootRow(t, repo, other, "abcd0001", "", now)
	if err := repo.db.Exec(`UPDATE nodes SET status = 'ready_for_adoption', awaiting_first_connect = true WHERE id = ?`, squat).Error; err != nil {
		t.Fatalf("marking the placeholder: %v", err)
	}

	got, err := repo.GetByHardwareSerialGlobal(ctx, "abcd0001")
	if err != nil || got.ID != device {
		t.Fatalf("got (%v, %v), want the owner's node %s whose EEPROM asks for this path", got, err, device)
	}

	// Without a readback naming the path, two accounts are a refusal.
	repo.db.Exec(`UPDATE nodes SET system_info = '{}'::jsonb WHERE id = ?`, device)
	if _, err := repo.GetByHardwareSerialGlobal(ctx, "abcd0001"); !errors.Is(err, app_errors.ErrNodeNotFound) {
		t.Fatalf("got %v, want ErrNodeNotFound when the candidates span accounts", err)
	}

	// A full-serial request is still exact only.
	if got, err := repo.GetByHardwareSerialGlobal(ctx, "10000000abcd0001"); err != nil || got.ID != device {
		t.Fatalf("full serial: got (%v, %v), want %s", got, err, device)
	}
}

// The low-word collision the bind guard refuses, in both directions, and the
// two shapes it must leave alone: two short stamps, and the asking account's
// own rows.
func TestLowWordHolderInAnotherAccount(t *testing.T) {
	repo := leaseRepo(t)
	ctx := context.Background()
	owner, other := uuid.New(), uuid.New()
	fullRow := seedBootRow(t, repo, owner, "10000000ABCD0001", "", time.Now())
	shortRow := seedBootRow(t, repo, owner, "dead0002", "", time.Now())

	cases := []struct {
		name    string
		account uuid.UUID
		serial  string
		want    uuid.UUID // uuid.Nil: no holder
	}{
		{"a short stamp whose full form another account holds", other, "abcd0001", fullRow},
		{"a full serial sharing another account's low word", other, "00000000abcd0001", fullRow},
		{"a full serial ending in another account's short stamp", other, "10000000dead0002", shortRow},
		{"the same full serial (the exact guard's)", other, "10000000abcd0001", uuid.Nil},
		{"the asking account's own rows", owner, "00000000abcd0001", uuid.Nil},
		{"short against short", other, "dead0002", uuid.Nil},
		{"nobody's low word", other, "10000000abcd0002", uuid.Nil},
		{"below the low-word floor", other, "abcd", uuid.Nil},
	}
	for _, c := range cases {
		got, err := repo.LowWordHolderInAnotherAccount(ctx, c.account, c.serial)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		switch {
		case c.want == uuid.Nil && got != nil:
			t.Errorf("%s: got holder %v, want none", c.name, got.NodeID)
		case c.want != uuid.Nil && (got == nil || got.NodeID != c.want || got.AccountID != owner):
			t.Errorf("%s: got %v, want node %s of %s", c.name, got, c.want, owner)
		}
	}
}
