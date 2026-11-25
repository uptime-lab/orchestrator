// Package health answers the question a static 200 cannot: can this process
// still do its job?
//
// The split is deliberate and operational.
//
//   - `/health` is LIVENESS — the process is up and serving HTTP. It is what a
//     container healthcheck must watch, because restarting the backend repairs
//     neither a dead database nor a full disk, and a restart loop makes both
//     considerably harder to fix.
//   - `/ready` is READINESS — the two dependencies without which every write in
//     this platform fails: Postgres, and the filesystem the storage volume sits
//     on. It is what an external uptime probe should watch. Until this existed
//     the only endpoint on offer reported green through a total database
//     failure, which is exactly the outage the probe is bought for.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"
)

const (
	// probeTimeout bounds the database round trip. A probe willing to wait as
	// long as its caller reports "unusably slow" as "up".
	probeTimeout = 2 * time.Second

	// cacheTTL keeps an unauthenticated public endpoint from becoming a query
	// generator — one uncached SELECT per request makes the probe URL a cheap
	// amplifier. Five seconds is far below the interval any monitor polls at,
	// so it costs the operator nothing.
	cacheTTL = 5 * time.Second

	// MinFreeBytes is the floor below which this instance cannot function even
	// though nothing has failed yet: Postgres shares this filesystem and needs
	// room to checkpoint, an image upload needs gigabytes, and the node-file
	// versions land here too. Reporting "ready" with 50 MiB free is reporting
	// the moment before the outage as health.
	MinFreeBytes = 256 << 20

	// objectStorageFailureThreshold is how many CONSECUTIVE bucket-probe
	// failures it takes before the storage check turns the instance not-ready.
	//
	// Tolerance, not a bigger timeout, on purpose: an isolated miss is a lost
	// HTTPS request that ends exactly at the timeout while healthy probes
	// answer in milliseconds, so a longer timeout would not remove it — it is
	// lost, not slow. A single lost request is not an outage, and with
	// cacheTTL one miss answered as 503 would be served to every caller for
	// five seconds. The API is fully usable without the bucket for
	// everything except image transfer, so one miss is logged and tolerated;
	// three in a row is a real outage. The database check stays strict — the
	// platform is NOT usable without Postgres.
	objectStorageFailureThreshold = 3
)

// Status is the per-check and overall verdict. Two values on purpose: an
// operator paging on "degraded" wants the same action as one paging on "down".
type Status string

const (
	StatusOK   Status = "ok"
	StatusFail Status = "fail"
)

// Check is one dependency's verdict.
//
// Detail is deliberately coarse. This endpoint is public and unauthenticated,
// so a driver error (which carries the database host, user and sometimes the
// DSN) must never reach it; the specific error goes to the log, where it is
// already scoped to the operator.
type Check struct {
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Report is the whole readiness answer.
type Report struct {
	Status Status           `json:"status"`
	Checks map[string]Check `json:"checks"`
}

// Ready reports whether every check passed.
func (r Report) Ready() bool { return r.Status == StatusOK }

// PingFunc runs one real query against the database.
//
// A query rather than a connection ping: a Postgres in recovery, or one that
// has exhausted its connection slots for everyone but this pool, accepts a TCP
// connection and answers nothing useful.
type PingFunc func(ctx context.Context) error

// Checker evaluates readiness, with a short cache in front.
type Checker struct {
	ping        PingFunc
	storagePath string
	log         *slog.Logger

	// stat is the filesystem probe, injectable so a test can describe a full
	// disk without needing one.
	stat func(path string) (freeBytes, freeInodes, totalInodes uint64, err error)
	now  func() time.Time

	// objectStorage answers "can the bucket be reached", and REPLACES the
	// free-space check when set. On object storage the local disk says nothing
	// about whether an image can be stored, so a statfs there would be a probe
	// that passes while every upload fails.
	objectStorage func(ctx context.Context) error

	// bucketMisses tolerates transient bucket-probe failures — see
	// objectStorageFailureThreshold. Guarded by mu (probes run under it).
	bucketMisses missTolerance

	mu       sync.Mutex
	cached   Report
	cachedAt time.Time
}

// missTolerance turns a stream of probe outcomes into "surface this failure or
// tolerate it": a failure is surfaced only once it has happened threshold times
// CONSECUTIVELY, and any success resets the streak. Extracted so the
// consecutive-miss logic is testable without a bucket.
type missTolerance struct {
	threshold int
	misses    int
}

// observe records one probe outcome. It returns whether a failure should be
// surfaced, and the current consecutive-miss count (0 after a success).
func (t *missTolerance) observe(failed bool) (surface bool, consecutive int) {
	if !failed {
		t.misses = 0
		return false, 0
	}
	t.misses++
	return t.misses >= t.threshold, t.misses
}

// New builds a checker from an already-narrowed ping function.
//
// storagePath empty means the instance keeps its files somewhere this process
// cannot statfs (object storage) — the storage check is then omitted from the
// report rather than reported as passing.
func New(ping PingFunc, storagePath string, log *slog.Logger) *Checker {
	if ping == nil {
		panic("health.New: ping is required")
	}
	if log == nil {
		panic("health.New: logger is required")
	}
	return &Checker{
		ping:         ping,
		storagePath:  storagePath,
		log:          log.With("component", "health"),
		stat:         diskFree,
		now:          time.Now,
		bucketMisses: missTolerance{threshold: objectStorageFailureThreshold},
	}
}

// NewGormChecker is the wiring the composition root uses.
func NewGormChecker(db *gorm.DB, storagePath string, log *slog.Logger) *Checker {
	if db == nil {
		panic("health.NewGormChecker: db is required")
	}
	return New(func(ctx context.Context) error {
		return db.WithContext(ctx).Exec("SELECT 1").Error
	}, storagePath, log)
}

// Check returns the current readiness report, recomputing it at most once per
// cacheTTL.
func (c *Checker) Check(ctx context.Context) Report {
	// Detach from the caller's cancellation: this endpoint is public, and a
	// client that closes the connection mid-request cancels the request
	// context, fails the probe, and the failure is then CACHED — a 503 served
	// to everyone for cacheTTL, including the external uptime probe, on a
	// perfectly healthy instance. Each probe still bounds itself
	// with probeTimeout, so nothing runs unbounded.
	ctx = context.WithoutCancel(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.cachedAt.IsZero() && c.now().Sub(c.cachedAt) < cacheTTL {
		return c.cached
	}

	report := Report{Status: StatusOK, Checks: map[string]Check{}}
	report.Checks["database"] = c.checkDatabase(ctx)
	switch {
	case c.objectStorage != nil:
		report.Checks["storage"] = c.checkObjectStorage(ctx)
	case c.storagePath != "":
		report.Checks["storage"] = c.checkStorage()
	}
	for _, check := range report.Checks {
		if check.Status != StatusOK {
			report.Status = StatusFail
		}
	}

	c.cached, c.cachedAt = report, c.now()
	return report
}

// Probe adapts Check to the HTTP layer's ReadinessProbe port.
func (c *Checker) Probe(ctx context.Context) (bool, any) {
	report := c.Check(ctx)
	return report.Ready(), report
}

func (c *Checker) checkDatabase(ctx context.Context) Check {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if err := c.ping(ctx); err != nil {
		detail := "unreachable"
		if errors.Is(err, context.DeadlineExceeded) {
			detail = fmt.Sprintf("did not answer within %s", probeTimeout)
		}
		c.log.Error("readiness: database probe failed", "error", err)
		return Check{Status: StatusFail, Detail: detail}
	}
	return Check{Status: StatusOK}
}

func (c *Checker) checkStorage() Check {
	freeBytes, freeInodes, totalInodes, err := c.stat(c.storagePath)
	if err != nil {
		// An unmounted volume reads as a missing path, which is the failure
		// this catches that a byte threshold never would.
		c.log.Error("readiness: storage probe failed", "error", err, "path", c.storagePath)
		return Check{Status: StatusFail, Detail: "unreadable"}
	}
	if freeBytes < MinFreeBytes {
		c.log.Error("readiness: storage is effectively full",
			"path", c.storagePath, "free_bytes", freeBytes, "floor_bytes", uint64(MinFreeBytes))
		return Check{Status: StatusFail, Detail: "free space below the operating floor"}
	}
	// Inodes run out separately, and when they do every write fails with
	// ENOSPC while the byte number an operator is watching says nothing is
	// wrong. A filesystem reporting no inodes at all (some do not) reports
	// zero total, which must read as "unknown", never as "exhausted".
	if totalInodes > 0 && freeInodes == 0 {
		c.log.Error("readiness: storage has no free inodes", "path", c.storagePath, "total_inodes", totalInodes)
		return Check{Status: StatusFail, Detail: "no free inodes"}
	}
	return Check{Status: StatusOK}
}

// SetObjectStorageChecker installs the bucket reachability probe. Calling it
// switches the storage check from statfs to the bucket; the composition root
// does this only when an object-storage backend was chosen.
func (c *Checker) SetObjectStorageChecker(check func(ctx context.Context) error) {
	if check == nil {
		panic("health.SetObjectStorageChecker: check is required")
	}
	c.objectStorage = check
}

func (c *Checker) checkObjectStorage(ctx context.Context) Check {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	err := c.objectStorage(ctx)
	surface, consecutive := c.bucketMisses.observe(err != nil)
	if err != nil {
		if !surface {
			// A transient miss: a single lost HTTPS request to the bucket must
			// not (via the cache) 503 every caller for cacheTTL. Logged so the
			// misses stay countable; readiness holds until the streak proves an
			// outage.
			c.log.Warn("readiness: object storage probe missed; tolerating",
				"error", err, "consecutive_misses", consecutive,
				"threshold", objectStorageFailureThreshold)
			return Check{Status: StatusOK}
		}
		detail := "unreachable"
		if errors.Is(err, context.DeadlineExceeded) {
			detail = fmt.Sprintf("did not answer within %s", probeTimeout)
		}
		// Coarse detail on purpose, as with the database: the SDK error carries
		// the endpoint, the bucket and sometimes the access key id, and this
		// endpoint is public and unauthenticated.
		c.log.Error("readiness: object storage probe failed",
			"error", err, "consecutive_misses", consecutive)
		return Check{Status: StatusFail, Detail: detail}
	}
	return Check{Status: StatusOK}
}

// String renders the report compactly, for a log line.
func (r Report) String() string {
	b, err := json.Marshal(r)
	if err != nil {
		return string(r.Status)
	}
	return string(b)
}
