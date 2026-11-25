package sql

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
)

// The self-registration ceiling against a real Postgres: the serialization
// is pg_advisory_xact_lock inside the transaction that counts and inserts,
// and a fake cannot prove that N concurrent first sign-ups collapse to one.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/ -run Ceiling

func accountCeilingRepo(t *testing.T) *AccountRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS accounts CASCADE`).Error; err != nil {
		t.Fatalf("dropping accounts: %v", err)
	}
	repo := NewAccountRepository(db)
	if err := repo.Setup(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

// ceilingOf is the shape the auth service's admission takes for a bare
// ceiling: closed at `limit` accounts, admitted below it.
func ceilingOf(limit int64) repository.AccountAdmission {
	return func(c repository.AccountCounts, _ *account.Account) error {
		if c.Settled >= limit {
			return &app_errors.RegistrationClosedError{Reason: app_errors.ReasonAccountCeiling}
		}
		return nil
	}
}

// Sixteen sign-ups arrive together on an EMPTY instance whose ceiling is one
// account. Without the lock every one of them reads zero and every one
// becomes "the first owner"; with it exactly one row exists afterwards and
// the other fifteen were refused as closed — the race where two
// simultaneous first sign-ups both pass the account ceiling.
//
// A count-then-insert transaction is sub-millisecond, so sixteen goroutines
// alone do not reliably overlap and the test passed with the lock removed.
// The admission therefore HOLDS the window open: each one waits until all
// sixteen have read their count, or a short timeout. With the lock only one
// admission can ever be inside, so it waits out the timeout and the rest
// serialize behind it; without the lock all sixteen arrive within
// microseconds, all holding a count of zero, and all insert. Verified red
// against the lock-less transaction before the lock went in.
func TestConcurrentFirstSignupRespectsTheCeiling(t *testing.T) {
	repo := accountCeilingRepo(t)
	const attempts = 16
	const window = 150 * time.Millisecond

	var arrived atomic.Int32
	allIn := make(chan struct{})
	holdOpen := func(c repository.AccountCounts, candidate *account.Account) error {
		if arrived.Add(1) == attempts {
			close(allIn)
		}
		select {
		case <-allIn:
		case <-time.After(window):
		}
		return ceilingOf(1)(c, candidate)
	}

	var wg sync.WaitGroup
	results := make([]error, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = repo.CreateAdmitted(context.Background(), account.NewAccount("first"), time.Time{}, holdOpen)
		}(i)
	}
	close(start)
	wg.Wait()

	var admitted, refused int
	for i, err := range results {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, app_errors.ErrRegistrationClosed):
			refused++
		default:
			t.Errorf("attempt %d: unexpected error %v", i, err)
		}
	}
	if admitted != 1 || refused != attempts-1 {
		t.Fatalf("admitted=%d refused=%d, want exactly one owner and %d refusals", admitted, refused, attempts-1)
	}
	counts, err := repo.Counts(context.Background(), time.Time{})
	count := counts.Total
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("the table holds %d accounts after a ceiling of 1", count)
	}
}

// A refused admission rolls the transaction back and inserts nothing; an
// admitted one is visible to the next caller's count. Sequential, so the
// contract is pinned independently of the race above.
func TestCreateAdmittedInsertsOnlyWhenAdmitted(t *testing.T) {
	repo := accountCeilingRepo(t)
	ctx := context.Background()

	var seen []int64
	record := func(c repository.AccountCounts, _ *account.Account) error { seen = append(seen, c.Total); return nil }
	if err := repo.CreateAdmitted(ctx, account.NewAccount("one"), time.Time{}, record); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateAdmitted(ctx, account.NewAccount("two"), time.Time{}, record); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 0 || seen[1] != 1 {
		t.Fatalf("the admission saw counts %v, want [0 1]", seen)
	}

	refusal := errors.New("not today")
	err := repo.CreateAdmitted(ctx, account.NewAccount("three"), time.Time{}, func(repository.AccountCounts, *account.Account) error { return refusal })
	if !errors.Is(err, refusal) {
		t.Fatalf("the admission's error must come back unchanged, got %v", err)
	}
	counts, err := repo.Counts(ctx, time.Time{})
	count := counts.Total
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("a refused admission inserted a row: count=%d", count)
	}
}

// An open sign-up is written as the admission marked it, counted as pending
// (not settled) while inside the window, and settled only under the lock and
// only when the admission accepts.
func TestOpenSignupIsCountedPendingAndSettlesUnderTheCeiling(t *testing.T) {
	repo := accountCeilingRepo(t)
	ctx := context.Background()
	now := time.Now()
	window := now.Add(-48 * time.Hour)

	if err := repo.CreateAdmitted(ctx, account.NewAccount("owner"), window, ceilingOf(10)); err != nil {
		t.Fatalf("owner: %v", err)
	}
	pending := account.NewAccount("open")
	markPending := func(c repository.AccountCounts, candidate *account.Account) error {
		candidate.OpenSignupPendingSince = &now
		return nil
	}
	if err := repo.CreateAdmitted(ctx, pending, window, markPending); err != nil {
		t.Fatalf("open sign-up: %v", err)
	}
	stale := account.NewAccount("abandoned")
	old := now.Add(-72 * time.Hour)
	if err := repo.CreateAdmitted(ctx, stale, window, func(c repository.AccountCounts, candidate *account.Account) error {
		candidate.OpenSignupPendingSince = &old
		return nil
	}); err != nil {
		t.Fatalf("abandoned sign-up: %v", err)
	}

	c, err := repo.Counts(ctx, window)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if c.Total != 3 || c.Settled != 1 || c.Pending != 1 {
		t.Fatalf("counts = %+v, want total 3, settled 1, pending 1 (the abandoned one is outside the window)", c)
	}

	refusal := errors.New("at the ceiling")
	if ok, err := repo.SettleOpenSignup(ctx, pending.ID, window, func(repository.AccountCounts, *account.Account) error { return refusal }); ok || !errors.Is(err, refusal) {
		t.Fatalf("a refused settle: settled %v, err %v", ok, err)
	}
	if ok, err := repo.SettleOpenSignup(ctx, pending.ID, window, ceilingOf(10)); !ok || err != nil {
		t.Fatalf("settle: settled %v, err %v", ok, err)
	}
	got, err := repo.GetByID(ctx, pending.ID)
	if err != nil || got.OpenSignupPendingSince != nil {
		t.Fatalf("after settling: %+v, %v; want the mark cleared", got, err)
	}

	// Update never writes the mark back from a stale copy.
	pending.Name = "renamed"
	if err := repo.Update(ctx, pending); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ := repo.GetByID(ctx, pending.ID); got.OpenSignupPendingSince != nil {
		t.Fatal("Update wrote the pending mark back")
	}
}
