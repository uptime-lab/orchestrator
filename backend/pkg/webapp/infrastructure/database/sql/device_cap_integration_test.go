package sql

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	nodesvc "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The device entitlement's count and its locked admission, against a real
// Postgres: the predicate is SQL, the serialisation is pg_advisory_xact_lock,
// and neither can be proven by a fake.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

func deviceCapRepo(t *testing.T) *NodeRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	// Each test opens its own pool; left open, the package's pools add up
	// past the local Postgres's max_connections and the LAST tests fail to
	// connect, which reads as a fault in whichever test runs last.
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.Exec(`DROP TABLE IF EXISTS nodes CASCADE`).Error; err != nil {
		t.Fatalf("dropping nodes: %v", err)
	}
	if err := db.AutoMigrate(&NodeModel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewNodeRepository(db, passthroughEncryptor{})
}

func seedNode(t *testing.T, repo *NodeRepository, accountID uuid.UUID, mutate func(*node.Node)) *node.Node {
	t.Helper()
	n := &node.Node{
		ID:             uuid.New(),
		AccountID:      accountID,
		Name:           "n",
		UniqueToken:    uuid.New().String(),
		Status:         node.StatusOnline,
		ApprovalStatus: node.ApprovalStatusApproved,
	}
	if mutate != nil {
		mutate(n)
	}
	if err := repo.Create(context.Background(), n); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return n
}

// Only a device under management counts: a browser-flashed placeholder, an
// operator-created node waiting for its agent, a pending master-token device
// and a rejected one are rows, not devices. A leaked master token must not be
// able to fill a paid cap with rows that never connected. The SQL predicate
// and node.IsAdmitted must agree row for row. A restored node awaiting its
// device's rebind IS counted: the restore admitted it, under the cap, and
// it is counted from then.
func TestCountExcludesPlaceholders(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	account := uuid.New()

	rows := []struct {
		name   string
		mutate func(*node.Node)
	}{
		{"online admitted", nil},
		{"offline admitted", func(n *node.Node) { n.Status = node.StatusOffline }},
		{"maintenance admitted", func(n *node.Node) { n.Status = node.StatusMaintenance }},
		{"webusb placeholder", func(n *node.Node) { n.Status = node.StatusFlashing; n.AwaitingFirstConnect = true }},
		{"restored tombstone awaiting first connect", func(n *node.Node) { n.Status = node.StatusOffline; n.AwaitingFirstConnect = true }},
		{"operator-created, agent never connected", func(n *node.Node) { n.Status = node.StatusReadyForAdoption }},
		{"pending approval", func(n *node.Node) { n.ApprovalStatus = node.ApprovalStatusPendingApproval }},
		{"rejected", func(n *node.Node) { n.ApprovalStatus = node.ApprovalStatusRejected }},
	}
	var wantAdmitted int64
	for _, r := range rows {
		n := seedNode(t, repo, account, r.mutate)
		if n.IsAdmitted() {
			wantAdmitted++
		}
	}
	if wantAdmitted != 4 {
		t.Fatalf("the domain predicate admits %d of the fixtures, the test expects 4; the fixtures changed", wantAdmitted)
	}

	got, err := repo.CountAdmittedNodes(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != wantAdmitted {
		t.Errorf("CountAdmittedNodes = %d, want %d (the rows node.IsAdmitted accepts)", got, wantAdmitted)
	}
}

// The per-account count is what a tenant's subscription card shows: the
// same admission predicate, one account's rows only. Every not-admitted
// shape in the caller's account and every admitted row of another account
// must stay out of it.
func TestCountForAccountIsTheAccountsAdmittedRowsOnly(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	mine, other := uuid.New(), uuid.New()

	seedNode(t, repo, mine, nil)
	seedNode(t, repo, mine, func(n *node.Node) { n.Status = node.StatusOffline })
	seedNode(t, repo, mine, func(n *node.Node) { n.Status = node.StatusMaintenance })
	seedNode(t, repo, mine, func(n *node.Node) { n.ApprovalStatus = node.ApprovalStatusPendingApproval })
	seedNode(t, repo, mine, func(n *node.Node) { n.ApprovalStatus = node.ApprovalStatusRejected })
	seedNode(t, repo, mine, func(n *node.Node) { n.Status = node.StatusFlashing; n.AwaitingFirstConnect = true })
	seedNode(t, repo, mine, func(n *node.Node) { n.Status = node.StatusReadyForAdoption })
	for i := 0; i < 5; i++ {
		seedNode(t, repo, other, nil)
	}

	got, err := repo.CountAdmittedNodesForAccount(ctx, mine)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 3 {
		t.Errorf("CountAdmittedNodesForAccount = %d, want the account's 3 admitted rows", got)
	}
	if got, _ := repo.CountAdmittedNodesForAccount(ctx, uuid.New()); got != 0 {
		t.Errorf("an account with no nodes counts %d", got)
	}
	if got, _ := repo.CountAdmittedNodes(ctx); got != 8 {
		t.Errorf("the instance count = %d, want 8 (both accounts)", got)
	}
}

// The free cap is per INSTALL. Two accounts with twenty each are forty
// devices, not two fleets under the cap.
func TestCountIsInstanceWide(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	for i := 0; i < 20; i++ {
		seedNode(t, repo, a, nil)
		seedNode(t, repo, b, nil)
	}
	got, err := repo.CountAdmittedNodes(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 40 {
		t.Errorf("CountAdmittedNodes = %d across two accounts, want 40", got)
	}

	// And the admission reads the same number: account C, holding nothing,
	// is refused at a cap of 40 because the INSTALL is full.
	fresh := &node.Node{ID: uuid.New(), AccountID: uuid.New(), Name: "c", UniqueToken: uuid.New().String(),
		Status: node.StatusReadyForAdoption, ApprovalStatus: node.ApprovalStatusApproved}
	_, err = repo.CreateUnderDeviceCap(ctx, fresh, repository.InstanceCapOnly(40))
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("admission into an empty account on a full install: err = %v, want the cap refusal", err)
	}
	if capErr.Count != 40 || capErr.Cap != 40 {
		t.Errorf("refusal reports %d of %d", capErr.Count, capErr.Cap)
	}
}

type fixedDeviceCap int

func (c fixedDeviceCap) EffectiveDeviceCap(context.Context) (int, string) { return int(c), "" }

// The cap bounds ADMISSION through the real service over the real
// repository. Rows are created the way production creates them — an
// operator-created node waiting for its agent (ready_for_adoption) and a
// master-token device pending approval — all below the cap, since none of
// them is admitted. Then they are admitted at the same instant: first
// connects and approvals racing. Exactly cap minus the existing fleet may
// pass; every refused row must be left un-admitted, so it can be admitted
// later. The pre-fix shape gated only the row's creation and admitted all
// of them.
func TestConcurrentAdmissionAtCap(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	account := uuid.New()

	const (
		cap        = 5
		existing   = 3 // admitted already
		connecting = 8
		approving  = 4
	)
	for i := 0; i < existing; i++ {
		seedNode(t, repo, account, nil)
	}

	recorder, _ := audittest.New()
	svc := nodesvc.NewService(repo, nil, nil, nil, nil, nil, nil, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetDeviceCapResolver(fixedDeviceCap(cap))

	var jobs []func() error
	var created []*node.Node
	for i := 0; i < connecting; i++ {
		n := seedNode(t, repo, account, func(n *node.Node) {
			n.Status = node.StatusReadyForAdoption
		})
		created = append(created, n)
		serial := fmt.Sprintf("10000000%08x", i)
		jobs = append(jobs, func() error {
			_, err := svc.Connect(ctx, node_connect.NodeConnectCommand{NodeID: n.ID, AccountID: account,
				UniqueToken: n.UniqueToken,
				Fingerprint: node.HardwareFingerprint{HardwareSerial: serial, MacAddress: fmt.Sprintf("02:00:00:00:00:%02x", i)}})
			return err
		})
	}
	for i := 0; i < approving; i++ {
		n := seedNode(t, repo, account, func(n *node.Node) {
			n.Status = node.StatusOffline
			n.ApprovalStatus = node.ApprovalStatusPendingApproval
		})
		created = append(created, n)
		jobs = append(jobs, func() error {
			_, err := svc.ApproveNode(ctx, account, uuid.New(), n.ID)
			return err
		})
	}
	if count, _ := repo.CountAdmittedNodes(ctx); count != existing {
		t.Fatalf("the created rows already count (%d admitted); the fixture is not un-admitted", count)
	}

	var admitted, refused int32
	var wg sync.WaitGroup
	wg.Add(len(jobs))
	start := make(chan struct{})
	for _, job := range jobs {
		go func() {
			defer wg.Done()
			<-start
			err := job()
			var capErr *entitlement.DeviceCapError
			switch {
			case err == nil:
				atomic.AddInt32(&admitted, 1)
			case errors.As(err, &capErr):
				atomic.AddInt32(&refused, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	racers := int32(len(jobs))
	if admitted != cap-existing {
		t.Errorf("%d of %d concurrent admissions passed, want exactly %d (cap %d minus %d existing)", admitted, racers, cap-existing, cap, existing)
	}
	if refused != racers-(cap-existing) {
		t.Errorf("%d refused, want %d", refused, racers-(cap-existing))
	}
	count, err := repo.CountAdmittedNodes(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != cap {
		t.Errorf("the install holds %d admitted devices after the race, want exactly the cap %d", count, cap)
	}

	// Every refused row is still there and still un-admitted.
	stillWaiting := 0
	for _, n := range created {
		row, err := repo.GetByID(ctx, account, n.ID)
		if err != nil {
			t.Fatalf("a created row vanished: %v", err)
		}
		if !row.IsAdmitted() {
			stillWaiting++
		}
	}
	if int32(stillWaiting) != refused {
		t.Errorf("%d rows are left un-admitted, want the %d refused ones", stillWaiting, refused)
	}
}

// Against real Postgres: a count that fails inside the admission
// transaction aborts it (every later statement there answers SQLSTATE
// 25P02). The admission is refused with the unverified error and nothing is
// written — not in the aborted transaction, and not after it on its own. The
// count is made to fail with a statement on a relation that does not exist,
// inside the locked transaction.
func TestAdmissionIsRefusedWhenTheCountFailsInPostgres(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	repo.countAdmittedIn = func(tx *gorm.DB) (int64, error) {
		var n int64
		err := tx.Raw("SELECT count(*) FROM entitlement_probe_missing_relation").Scan(&n).Error
		return n, err
	}

	n := &node.Node{ID: uuid.New(), AccountID: uuid.New(), Name: "f", UniqueToken: uuid.New().String(),
		Status: node.StatusReadyForAdoption, ApprovalStatus: node.ApprovalStatusApproved}
	_, err := repo.CreateUnderDeviceCap(ctx, n, repository.InstanceCapOnly(1))
	if !errors.As(err, new(*entitlement.DeviceCapUnverifiedError)) {
		t.Fatalf("a failed count: err = %v, want *entitlement.DeviceCapUnverifiedError", err)
	}
	if _, err := repo.GetByID(ctx, n.AccountID, n.ID); err == nil {
		t.Error("a refused admission committed a row")
	}
	// The pool's connection is usable again: the aborted transaction was
	// rolled back, not left open.
	if _, err := repo.CountAdmittedNodes(ctx); err != nil {
		t.Errorf("counting after the refusal: %v", err)
	}
}

// A lock timeout on the connection must not lift the cap. One session holds
// the admission lock; the admitting session runs with a short lock_timeout,
// so its pg_advisory_xact_lock fails with SQLSTATE 55P03. The first connect
// is refused with the unverified error and the row is left exactly as it was
// — still waiting to be admitted — so it is admitted once the lock is free.
func TestLockTimeoutRefusesTheAdmissionInPostgres(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	account := uuid.New()
	waiting := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusReadyForAdoption })

	// The admitting session: one connection, so the session setting holds
	// for every statement the repository sends.
	admitDB, err := gorm.Open(postgres.Open(os.Getenv("BACKEND_TEST_DSN")),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	admitSQL, err := admitDB.DB()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() { _ = admitSQL.Close() })
	admitSQL.SetMaxOpenConns(1)
	if err := admitDB.Exec("SET lock_timeout = '200ms'").Error; err != nil {
		t.Fatalf("setting lock_timeout: %v", err)
	}
	admitRepo := NewNodeRepository(admitDB, passthroughEncryptor{})

	holder := repo.db.Begin()
	if err := holder.Exec("SELECT pg_advisory_xact_lock(?)", DeviceCapLockClass).Error; err != nil {
		holder.Rollback()
		t.Fatalf("holding the lock: %v", err)
	}

	recorder, _ := audittest.New()
	svc := nodesvc.NewService(admitRepo, nil, nil, nil, nil, nil, nil, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetDeviceCapResolver(fixedDeviceCap(25))
	connect := node_connect.NodeConnectCommand{NodeID: waiting.ID, AccountID: account, UniqueToken: waiting.UniqueToken,
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "10000000feedf00d", MacAddress: "02:00:00:00:00:01"}}

	_, err = svc.Connect(ctx, connect)
	holder.Rollback()
	if !errors.As(err, new(*entitlement.DeviceCapUnverifiedError)) {
		t.Fatalf("first connect under a lock timeout: err = %v, want *entitlement.DeviceCapUnverifiedError", err)
	}
	row, err := repo.GetByID(ctx, account, waiting.ID)
	if err != nil {
		t.Fatalf("reading the row back: %v", err)
	}
	if row.IsAdmitted() || row.Status != node.StatusReadyForAdoption || row.Fingerprint != nil {
		t.Errorf("a refused first connect wrote the row: status=%q admitted=%t fingerprint=%v",
			row.Status, row.IsAdmitted(), row.Fingerprint)
	}

	// The lock is free: the same connect admits.
	if _, err := svc.Connect(ctx, connect); err != nil {
		t.Fatalf("first connect once the lock is free: %v", err)
	}
	if row, err := repo.GetByID(ctx, account, waiting.ID); err != nil || !row.IsAdmitted() {
		t.Errorf("the row was not admitted once the lock was free: %+v, %v", row, err)
	}
}

// 0 is unlimited and a plain insert — it must not even take the lock's
// round trip, so a hosted install pays nothing for the cap it does not have.
func TestUnlimitedCapIsAPlainInsert(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		n := &node.Node{ID: uuid.New(), AccountID: uuid.New(), Name: "u", UniqueToken: uuid.New().String(),
			Status: node.StatusOnline, ApprovalStatus: node.ApprovalStatusApproved}
		if _, err := repo.CreateUnderDeviceCap(ctx, n, repository.InstanceCapOnly(0)); err != nil {
			t.Fatalf("unlimited insert %d: %v", i, err)
		}
	}
}

type fixedAccountLimit int

func (l fixedAccountLimit) AccountDeviceLimit(context.Context, uuid.UUID) (entitlement.AccountLimit, error) {
	return entitlement.AccountLimit{Limit: int(l), Plan: entitlement.AccountLimitPlanFree}, nil
}

// The per-account limit counts the ADMITTING account's admitted rows only,
// under the same lock, and binds even where the instance cap is unlimited:
// another account's fleet never fills it, and a full account is refused on
// an install with room.
func TestAccountLimitCountsTheAccountOnly(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	mine, other := uuid.New(), uuid.New()
	for i := 0; i < 3; i++ {
		seedNode(t, repo, mine, nil)
	}
	seedNode(t, repo, mine, func(n *node.Node) { n.ApprovalStatus = node.ApprovalStatusPendingApproval })
	for i := 0; i < 20; i++ {
		seedNode(t, repo, other, nil)
	}
	limits := func(account uuid.UUID, limit int) repository.AdmissionLimits {
		return repository.AdmissionLimits{AccountID: account, Account: entitlement.AccountLimit{Limit: limit}}
	}
	fresh := func(account uuid.UUID) *node.Node {
		return &node.Node{ID: uuid.New(), AccountID: account, Name: "f", UniqueToken: uuid.New().String(),
			Status: node.StatusOnline, ApprovalStatus: node.ApprovalStatusApproved}
	}
	_, err := repo.CreateUnderDeviceCap(ctx, fresh(mine), limits(mine, 3))
	var limitErr *entitlement.AccountDeviceLimitError
	if !errors.As(err, &limitErr) || limitErr.Count != 3 || limitErr.Limit != 3 {
		t.Fatalf("a full account on an unlimited install: err = %v, want 3 of 3", err)
	}
	if _, err := repo.CreateUnderDeviceCap(ctx, fresh(mine), limits(mine, 4)); err != nil {
		t.Errorf("an account below its limit, beside a larger one: %v", err)
	}
	if _, err := repo.CreateUnderDeviceCap(ctx, fresh(uuid.New()), limits(uuid.Nil, 1)); err != nil {
		t.Errorf("limits with no account bound something: %v", err)
	}
}

// Two admissions racing for the LAST slot of one account admit exactly one,
// on an install whose instance cap is unlimited: the account count is taken
// under the admission lock, so they cannot both read the free slot.
func TestConcurrentAdmissionAtTheAccountLimit(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	account := uuid.New()
	const limit = 4
	for i := 0; i < limit-1; i++ {
		seedNode(t, repo, account, nil)
	}
	recorder, _ := audittest.New()
	svc := nodesvc.NewService(repo, nil, nil, nil, nil, nil, nil, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetDeviceCapResolver(fixedDeviceCap(0))
	svc.SetAccountDeviceLimitResolver(fixedAccountLimit(limit))

	const racers = 6
	var jobs []func() error
	for i := 0; i < racers; i++ {
		n := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusReadyForAdoption })
		serial := fmt.Sprintf("20000000%08x", i)
		jobs = append(jobs, func() error {
			_, err := svc.Connect(ctx, node_connect.NodeConnectCommand{NodeID: n.ID, AccountID: account,
				UniqueToken: n.UniqueToken,
				Fingerprint: node.HardwareFingerprint{HardwareSerial: serial, MacAddress: fmt.Sprintf("02:00:00:00:01:%02x", i)}})
			return err
		})
	}
	var admitted, refused int32
	var wg sync.WaitGroup
	wg.Add(len(jobs))
	start := make(chan struct{})
	for _, job := range jobs {
		go func() {
			defer wg.Done()
			<-start
			err := job()
			switch {
			case err == nil:
				atomic.AddInt32(&admitted, 1)
			case errors.As(err, new(*entitlement.AccountDeviceLimitError)):
				atomic.AddInt32(&refused, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if admitted != 1 || refused != racers-1 {
		t.Errorf("%d admitted, %d refused racing for the last slot; want 1 and %d", admitted, refused, racers-1)
	}
	if own, _ := repo.CountAdmittedNodesForAccount(ctx, account); own != limit {
		t.Errorf("the account holds %d admitted devices after the race, want exactly its limit %d", own, limit)
	}
}

// The override columns read back as written, NULL as no override, and the
// CHECK refuses a negative limit.
func TestAccountDeviceLimitOverrideColumns(t *testing.T) {
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	repo := NewAccountRepository(db)
	if err := repo.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ctx := context.Background()
	id := uuid.New()
	if err := db.Exec("INSERT INTO accounts (id, name) VALUES (?, 'a')", id).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	if o, err := repo.DeviceLimitOverride(ctx, id); err != nil || o.Limit != nil || o.Until != nil {
		t.Fatalf("a new account's override = %+v, %v; want none", o, err)
	}
	if err := db.Exec("UPDATE accounts SET device_limit = 50, device_limit_until = '2027-06-30T00:00:00Z' WHERE id = ?", id).Error; err != nil {
		t.Fatalf("grant: %v", err)
	}
	o, err := repo.DeviceLimitOverride(ctx, id)
	if err != nil || o.Limit == nil || *o.Limit != 50 || o.Until == nil || !o.Until.Equal(mustTime(t, "2027-06-30T00:00:00Z")) {
		t.Errorf("override = %+v, %v", o, err)
	}
	if err := db.Exec("UPDATE accounts SET device_limit = -1 WHERE id = ?", id).Error; err == nil {
		t.Error("the CHECK accepted a negative device limit")
	}
	if _, err := repo.DeviceLimitOverride(ctx, uuid.New()); err == nil {
		t.Error("an unknown account answered an override")
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
