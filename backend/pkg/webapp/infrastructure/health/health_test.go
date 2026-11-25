package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func okPing(context.Context) error { return nil }

// newTestChecker builds a checker with both dependencies described rather than
// real: a full disk cannot be produced in a unit test, and the whole point of
// the storage check is what it does when the disk is full.
func newTestChecker(t *testing.T, ping PingFunc, freeBytes, freeInodes, totalInodes uint64, statErr error) *Checker {
	t.Helper()
	c := New(ping, "/var/lib/orchestrator", quietLogger())
	c.stat = func(string) (uint64, uint64, uint64, error) {
		return freeBytes, freeInodes, totalInodes, statErr
	}
	return c
}

func TestReadyWhenBothDependenciesAnswer(t *testing.T) {
	c := newTestChecker(t, okPing, 10<<30, 1_000_000, 2_000_000, nil)

	report := c.Check(context.Background())

	if !report.Ready() {
		t.Fatalf("expected ready, got %s", report)
	}
	if report.Checks["database"].Status != StatusOK || report.Checks["storage"].Status != StatusOK {
		t.Fatalf("expected both checks ok, got %s", report)
	}
}

// The regression this whole endpoint exists for: the old /health returned 200
// through a total database failure, so an external probe reported an outage as
// health.
func TestNotReadyWhenTheDatabaseIsDown(t *testing.T) {
	c := newTestChecker(t, func(context.Context) error {
		return errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")
	}, 10<<30, 1_000_000, 2_000_000, nil)

	report := c.Check(context.Background())

	if report.Ready() {
		t.Fatal("reported ready with an unreachable database — this is the failure the probe exists to catch")
	}
	if report.Checks["database"].Status != StatusFail {
		t.Fatalf("expected the database check to fail, got %s", report)
	}
	// The driver error carries the database host and user; a public endpoint
	// must not repeat it.
	if detail := report.Checks["database"].Detail; detail != "unreachable" {
		t.Fatalf("detail %q leaks more than the coarse verdict", detail)
	}
}

func TestNotReadyWhenStorageIsBelowTheFloor(t *testing.T) {
	c := newTestChecker(t, okPing, MinFreeBytes-1, 1_000_000, 2_000_000, nil)

	report := c.Check(context.Background())

	if report.Ready() {
		t.Fatal("reported ready with the storage filesystem effectively full")
	}
	if report.Checks["storage"].Status != StatusFail {
		t.Fatalf("expected the storage check to fail, got %s", report)
	}
}

func TestNotReadyWhenInodesAreExhaustedDespiteFreeBytes(t *testing.T) {
	c := newTestChecker(t, okPing, 500<<30, 0, 2_000_000, nil)

	report := c.Check(context.Background())

	if report.Ready() {
		t.Fatal("reported ready with no free inodes — every write fails with ENOSPC")
	}
}

// A filesystem that does not report inodes at all reports zero total. That
// must read as "unknown", never as "exhausted", or such a host is permanently
// not-ready for a reason that does not exist.
func TestAFilesystemWithoutInodeAccountingIsNotTreatedAsExhausted(t *testing.T) {
	c := newTestChecker(t, okPing, 500<<30, 0, 0, nil)

	if report := c.Check(context.Background()); !report.Ready() {
		t.Fatalf("expected ready, got %s", report)
	}
}

func TestNotReadyWhenTheStoragePathCannotBeRead(t *testing.T) {
	c := newTestChecker(t, okPing, 0, 0, 0, errors.New("no such file or directory"))

	if report := c.Check(context.Background()); report.Ready() {
		t.Fatal("reported ready with an unreadable storage path — an unmounted volume looks exactly like this")
	}
}

// Object storage leaves nothing local to statfs. Omitting the check is honest;
// reporting it as passing would claim a filesystem was inspected.
func TestStorageCheckIsOmittedWhenThereIsNoLocalPath(t *testing.T) {
	c := New(okPing, "", quietLogger())

	report := c.Check(context.Background())

	if !report.Ready() {
		t.Fatalf("expected ready, got %s", report)
	}
	if _, present := report.Checks["storage"]; present {
		t.Fatal("reported a storage verdict for an instance with no local storage path")
	}
}

func TestObjectStorageProbeIsReportedWhenConfigured(t *testing.T) {
	c := New(okPing, "", quietLogger())
	c.SetObjectStorageChecker(func(context.Context) error { return nil })

	report := c.Check(context.Background())

	if !report.Ready() {
		t.Fatalf("expected ready, got %s", report)
	}
	if check, present := report.Checks["storage"]; !present || check.Status != StatusOK {
		t.Fatalf("expected a passing storage verdict, got %s", report)
	}
}

// checkPastCache runs one probe with the cache expired, so each call is a real
// probe rather than a cached verdict.
func checkPastCache(c *Checker, now *time.Time) Report {
	*now = now.Add(cacheTTL + time.Second)
	return c.Check(context.Background())
}

// A bucket that stays down IS an outage: after the tolerance threshold the
// instance must turn not-ready, with the same coarse detail as before.
func TestNotReadyWhenTheBucketStaysUnreachable(t *testing.T) {
	c := New(okPing, "", quietLogger())
	c.SetObjectStorageChecker(func(context.Context) error { return errors.New("dial tcp: connection refused") })
	now := time.Now()
	c.now = func() time.Time { return now }

	var report Report
	for i := 0; i < objectStorageFailureThreshold; i++ {
		report = checkPastCache(c, &now)
	}

	if report.Ready() {
		t.Fatalf("still ready after %d consecutive bucket failures — every image upload would fail",
			objectStorageFailureThreshold)
	}
	if detail := report.Checks["storage"].Detail; detail != "unreachable" {
		t.Errorf("detail = %q, want %q — this endpoint is public, so the SDK error "+
			"(which carries the endpoint, bucket and sometimes the key id) must not reach it", detail, "unreachable")
	}
}

// A single lost request to the bucket must not take the instance out of
// readiness: answered as 503, the cache would serve that 503 to every caller.
// A miss below the threshold must stay ready.
func TestASingleBucketMissIsToleratedAndAConsecutiveStreakIsNot(t *testing.T) {
	failures := 0
	c := New(okPing, "", quietLogger())
	c.SetObjectStorageChecker(func(context.Context) error {
		if failures > 0 {
			failures--
			return errors.New("context deadline exceeded")
		}
		return nil
	})
	now := time.Now()
	c.now = func() time.Time { return now }

	// One miss, then recovery: ready throughout.
	failures = 1
	if report := checkPastCache(c, &now); !report.Ready() {
		t.Fatalf("a single bucket miss must not take the instance out of readiness: %s", report)
	}
	if report := checkPastCache(c, &now); !report.Ready() {
		t.Fatalf("expected ready after the bucket recovered: %s", report)
	}

	// Two more misses after the recovery: still under the threshold, because
	// the success must have RESET the streak.
	failures = objectStorageFailureThreshold - 1
	for i := 0; i < objectStorageFailureThreshold-1; i++ {
		if report := checkPastCache(c, &now); !report.Ready() {
			t.Fatalf("miss %d after a recovery surfaced early — the success did not reset the streak: %s",
				i+1, report)
		}
	}
}

// The tolerance is the bucket probe's, not the database's: Postgres down is an
// outage on the first probe, always.
func TestTheDatabaseCheckStaysStrictUnderTheBucketTolerance(t *testing.T) {
	c := New(func(context.Context) error { return errors.New("connection refused") }, "", quietLogger())
	c.SetObjectStorageChecker(func(context.Context) error { return nil })

	if report := c.Check(context.Background()); report.Ready() {
		t.Fatal("a single database failure must fail readiness immediately")
	}
}

// The extracted consecutive-miss logic, exercised directly.
func TestMissTolerance(t *testing.T) {
	tol := missTolerance{threshold: 3}

	for i := 1; i <= 2; i++ {
		if surface, n := tol.observe(true); surface || n != i {
			t.Fatalf("miss %d: surface=%v n=%d, want tolerated with the streak counted", i, surface, n)
		}
	}
	if surface, n := tol.observe(true); !surface || n != 3 {
		t.Fatalf("third consecutive miss must surface, got surface=%v n=%d", surface, n)
	}
	// Beyond the threshold it keeps surfacing — an outage does not become
	// tolerable by lasting longer.
	if surface, _ := tol.observe(true); !surface {
		t.Fatal("a continuing outage must keep surfacing")
	}
	if surface, n := tol.observe(false); surface || n != 0 {
		t.Fatalf("a success must reset the streak, got surface=%v n=%d", surface, n)
	}
	if surface, n := tol.observe(true); surface || n != 1 {
		t.Fatalf("the first miss after a reset must be tolerated, got surface=%v n=%d", surface, n)
	}
}

// The bucket probe REPLACES the free-space probe. Running both would fail
// readiness on a full local disk that object storage does not use.
func TestObjectStorageProbeReplacesTheDiskProbe(t *testing.T) {
	c := newTestChecker(t, okPing, 0, 0, 0, errors.New("no such file or directory"))
	c.SetObjectStorageChecker(func(context.Context) error { return nil })

	if report := c.Check(context.Background()); !report.Ready() {
		t.Fatalf("a failing statfs still decided readiness on an object-storage instance: %s", report)
	}
}

// The endpoint is public and unauthenticated. Without the cache, every request
// is a database query and the probe URL becomes a cheap amplifier.
func TestRepeatedProbesDoNotRepeatTheQuery(t *testing.T) {
	calls := 0
	c := newTestChecker(t, func(context.Context) error {
		calls++
		return nil
	}, 10<<30, 1_000_000, 2_000_000, nil)

	now := time.Now()
	c.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		c.Check(context.Background())
	}
	if calls != 1 {
		t.Fatalf("ran %d queries for 5 probes within the cache window, want 1", calls)
	}

	now = now.Add(cacheTTL + time.Second)
	c.Check(context.Background())
	if calls != 2 {
		t.Fatalf("ran %d queries in total, want 2 — the cache never expired", calls)
	}
}

// A cached "ready" must not outlive the failure it predates.
func TestTheCacheExpiresIntoAFailure(t *testing.T) {
	failing := false
	c := newTestChecker(t, func(context.Context) error {
		if failing {
			return errors.New("connection refused")
		}
		return nil
	}, 10<<30, 1_000_000, 2_000_000, nil)

	now := time.Now()
	c.now = func() time.Time { return now }

	if !c.Check(context.Background()).Ready() {
		t.Fatal("expected the first probe to be ready")
	}
	failing = true
	now = now.Add(cacheTTL + time.Second)
	if c.Check(context.Background()).Ready() {
		t.Fatal("still ready after the cache window — a stale green is worse than no probe")
	}
}

func TestProbeReportsTheHTTPVerdict(t *testing.T) {
	c := newTestChecker(t, func(context.Context) error { return errors.New("down") }, 10<<30, 1, 2, nil)

	ready, body := c.Probe(context.Background())

	if ready {
		t.Fatal("expected not ready")
	}
	if _, ok := body.(Report); !ok {
		t.Fatalf("body is %T, want a Report", body)
	}
}

// A hung database must not hang the probe: a monitor waiting as long as the
// server is willing to stall cannot distinguish "slow" from "up".
func TestTheDatabaseProbeIsBounded(t *testing.T) {
	c := newTestChecker(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, 10<<30, 1_000_000, 2_000_000, nil)

	start := time.Now()
	report := c.Check(context.Background())
	elapsed := time.Since(start)

	if report.Ready() {
		t.Fatal("reported ready against a database that never answered")
	}
	if elapsed > probeTimeout*2 {
		t.Fatalf("probe took %s, want it bounded near %s", elapsed, probeTimeout)
	}
}

// A caller that closes the connection cancels the request context. That must
// not fail the probe — and above all must not CACHE the failure, or one
// disconnecting client serves a 503 to everyone for cacheTTL, including the
// external uptime probe, on a perfectly healthy instance.
func TestACallersCancelledContextDoesNotPoisonTheCache(t *testing.T) {
	// A healthy database modeled honestly: the query succeeds unless the
	// context handed to it is already dead.
	ping := func(ctx context.Context) error { return ctx.Err() }
	c := newTestChecker(t, ping, 10<<30, 1_000_000, 2_000_000, nil)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if report := c.Check(cancelled); !report.Ready() {
		t.Fatalf("a caller's own cancellation must not read as an outage, got %s", report)
	}
	// Within the cache window: the next caller must see health, not the
	// first caller's disconnect.
	if report := c.Check(context.Background()); !report.Ready() {
		t.Fatalf("cached verdict poisoned by a cancelled caller, got %s", report)
	}
}
