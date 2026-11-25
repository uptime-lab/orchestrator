package postgres

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sqlStateError string

func (e sqlStateError) Error() string    { return "server error " + string(e) }
func (e sqlStateError) SQLState() string { return string(e) }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A database that is still starting is waited for: after a host reboot the
// server can come up before Postgres does.
func TestADatabaseThatIsStartingIsWaitedFor(t *testing.T) {
	for _, notYet := range []error{
		fmt.Errorf("dial: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}),
		fmt.Errorf("lookup: %w", &net.DNSError{Err: "no such host", Name: "postgres"}),
		fmt.Errorf("connect: %w", sqlStateError("57P03")),
	} {
		calls := 0
		want := &gorm.DB{}
		db, err := openWithStartupWait(func() (*gorm.DB, error) {
			calls++
			if calls < 3 {
				return nil, notYet
			}
			return want, nil
		}, time.Minute, func(time.Duration) {}, quietLogger())
		if err != nil || db != want || calls != 3 {
			t.Errorf("%v: db=%v err=%v calls=%d, want the third attempt's connection", notYet, db, err, calls)
		}
	}
}

// Anything else is an answer, not a database still starting: a wrong password
// must fail the start at once rather than after the wait.
func TestARefusalIsNotRetried(t *testing.T) {
	calls := 0
	refused := fmt.Errorf("auth: %w", sqlStateError("28P01"))
	_, err := openWithStartupWait(func() (*gorm.DB, error) {
		calls++
		return nil, refused
	}, time.Minute, func(time.Duration) {}, quietLogger())
	if !errors.Is(err, refused) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want the refusal after one attempt", err, calls)
	}
}

// The wait is bounded: a database that never comes up still fails the start.
func TestTheWaitIsBounded(t *testing.T) {
	var slept time.Duration
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	_, err := openWithStartupWait(func() (*gorm.DB, error) {
		return nil, refused
	}, 20*time.Second, func(d time.Duration) { slept += d }, quietLogger())
	if !errors.As(err, new(*net.OpError)) {
		t.Fatalf("err=%v, want the last connection error", err)
	}
	if slept < 20*time.Second || slept > 30*time.Second {
		t.Fatalf("slept %v in total, want a little over the 20s wait", slept)
	}
}

// While the server waits for a database that is still starting, the ORM's own
// "failed to initialize database" line is not written at ERROR; the failure
// the wait gives up on is still returned for the caller to report.
func TestWaitingForTheDatabaseLogsNoORMError(t *testing.T) {
	var buf bytes.Buffer
	recorded := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := &gorm.Config{Logger: &SlogAdapter{SL: recorded, LogLevel: logger.Warn}}
	// Nothing listens on port 1: the dial is refused, the retryable case.
	dsn := "host=127.0.0.1 port=1 user=u password=p dbname=d sslmode=disable connect_timeout=2"

	sleeps := 0
	_, err := openDatabase(postgres.Open(dsn), cfg, 2*time.Second, func(time.Duration) { sleeps++ }, recorded)
	if err == nil {
		t.Fatal("opened a database nothing serves")
	}
	if !databaseNotUpYet(err) || sleeps == 0 {
		t.Fatalf("err=%v sleeps=%d, want the refused dial retried and then returned", err, sleeps)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("the wait logged at ERROR:\n%s", buf.String())
	}
	if cfg.Logger == nil || cfg.Logger.(*SlogAdapter).SL != recorded {
		t.Error("the caller's configured logger was replaced")
	}
}
