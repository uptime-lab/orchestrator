package sql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The locked admission with a database that cannot answer, without a
// database: a scripted database/sql driver records every statement and every
// transaction boundary, and fails the one statement a test names. What it
// proves is the repository's own control flow — which statements it sends,
// and that a failed lock or count is followed by a ROLLBACK and nothing else.
// The real Postgres behaviour (a statement timeout inside the transaction)
// is the DSN-gated test in device_cap_integration_test.go.

type scriptedDB struct {
	mu   sync.Mutex
	log  []string
	fail func(query string) error
	// count is what a successful count statement answers.
	count int64
}

func (s *scriptedDB) record(entry string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, entry)
}

func (s *scriptedDB) entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func (s *scriptedDB) failure(query string) error {
	if s.fail == nil {
		return nil
	}
	return s.fail(query)
}

type scriptedConnector struct{ s *scriptedDB }

func (c scriptedConnector) Connect(context.Context) (driver.Conn, error) {
	return &scriptedConn{s: c.s}, nil
}
func (c scriptedConnector) Driver() driver.Driver { return scriptedDriver{c.s} }

type scriptedDriver struct{ s *scriptedDB }

func (d scriptedDriver) Open(string) (driver.Conn, error) { return &scriptedConn{s: d.s}, nil }

type scriptedConn struct{ s *scriptedDB }

func (c *scriptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("scripted driver: prepared statements are not supported")
}
func (c *scriptedConn) Close() error { return nil }
func (c *scriptedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *scriptedConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if err := c.s.failure("BEGIN"); err != nil {
		return nil, err
	}
	c.s.record("BEGIN")
	return scriptedTx{c.s}, nil
}

func (c *scriptedConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.s.record(query)
	if err := c.s.failure(query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (c *scriptedConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.s.record(query)
	if err := c.s.failure(query); err != nil {
		return nil, err
	}
	if strings.Contains(strings.ToLower(query), "count(") {
		return &scriptedRows{cols: []string{"count"}, rows: [][]driver.Value{{c.s.count}}}, nil
	}
	return &scriptedRows{}, nil
}

type scriptedTx struct{ s *scriptedDB }

func (t scriptedTx) Commit() error   { t.s.record("COMMIT"); return nil }
func (t scriptedTx) Rollback() error { t.s.record("ROLLBACK"); return nil }

type scriptedRows struct {
	cols []string
	rows [][]driver.Value
}

func (r *scriptedRows) Columns() []string { return r.cols }
func (r *scriptedRows) Close() error      { return nil }
func (r *scriptedRows) Next(dest []driver.Value) error {
	if len(r.rows) == 0 {
		return io.EOF
	}
	copy(dest, r.rows[0])
	r.rows = r.rows[1:]
	return nil
}

func scriptedRepo(t *testing.T, s *scriptedDB) *NodeRepository {
	t.Helper()
	sqlDB := sql.OpenDB(scriptedConnector{s})
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent), SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("opening the scripted database: %v", err)
	}
	return NewNodeRepository(db, passthroughEncryptor{})
}

func isLock(q string) bool  { return strings.Contains(q, "pg_advisory_xact_lock") }
func isCount(q string) bool { return strings.Contains(strings.ToLower(q), "count(") }
func isWrite(q string) bool {
	q = strings.ToUpper(strings.TrimSpace(q))
	return strings.HasPrefix(q, "INSERT") || strings.HasPrefix(q, "UPDATE") || strings.HasPrefix(q, "DELETE")
}

// admissionWrites is every locked admission the repository offers, each
// against a fresh pre-connect row.
func admissionWrites() map[string]func(r *NodeRepository, cap int) (repository.DeviceCapAdmission, error) {
	n := func() *node.Node {
		return &node.Node{ID: uuid.New(), AccountID: uuid.New(), Name: "n", UniqueToken: uuid.New().String(),
			Status: node.StatusOnline, ApprovalStatus: node.ApprovalStatusApproved}
	}
	ctx := context.Background()
	return map[string]func(r *NodeRepository, cap int) (repository.DeviceCapAdmission, error){
		"CreateUnderDeviceCap": func(r *NodeRepository, cap int) (repository.DeviceCapAdmission, error) {
			return r.CreateUnderDeviceCap(ctx, n(), repository.InstanceCapOnly(cap))
		},
		"UpdateUnderDeviceCap": func(r *NodeRepository, cap int) (repository.DeviceCapAdmission, error) {
			return r.UpdateUnderDeviceCap(ctx, n(), repository.InstanceCapOnly(cap))
		},
		"ApproveUnderDeviceCap": func(r *NodeRepository, cap int) (repository.DeviceCapAdmission, error) {
			return r.ApproveUnderDeviceCap(ctx, uuid.New(), repository.InstanceCapOnly(cap))
		},
		"UpdateStatusUnderDeviceCap": func(r *NodeRepository, cap int) (repository.DeviceCapAdmission, error) {
			return r.UpdateStatusUnderDeviceCap(ctx, uuid.New(), string(node.StatusOnline), "", repository.InstanceCapOnly(cap))
		},
	}
}

// A failed lock, a failed count, or a transaction that cannot begin refuses
// the admission with the unverified error: the transaction is rolled back,
// and no write is sent — not inside it, and not after it on its own.
func TestLockedAdmissionRefusesWhenTheCheckFails(t *testing.T) {
	timeout := errors.New("ERROR: canceling statement due to statement timeout (SQLSTATE 57014)")
	failures := map[string]func(q string) error{
		"lock": func(q string) error {
			if isLock(q) {
				return timeout
			}
			return nil
		},
		"count": func(q string) error {
			if isCount(q) {
				return timeout
			}
			return nil
		},
		"begin": func(q string) error {
			if q == "BEGIN" {
				return timeout
			}
			return nil
		},
	}
	for writeName, write := range admissionWrites() {
		for failName, fail := range failures {
			t.Run(writeName+"/"+failName, func(t *testing.T) {
				s := &scriptedDB{fail: fail}
				_, err := write(scriptedRepo(t, s), 25)

				var unverified *entitlement.DeviceCapUnverifiedError
				if !errors.As(err, &unverified) {
					t.Fatalf("err = %v, want *entitlement.DeviceCapUnverifiedError", err)
				}
				if unverified.Cap != 25 || !errors.Is(unverified.Cause, timeout) {
					t.Errorf("refusal = cap %d cause %v, want cap 25 and the database's error", unverified.Cap, unverified.Cause)
				}
				log := s.entries()
				for _, q := range log {
					if isWrite(q) {
						t.Errorf("a write was sent after the check failed: %s\nstatements: %q", q, log)
					}
					if q == "COMMIT" {
						t.Errorf("a refused admission committed\nstatements: %q", log)
					}
				}
				if failName != "begin" && (len(log) == 0 || log[len(log)-1] != "ROLLBACK") {
					t.Errorf("the transaction was not rolled back last\nstatements: %q", log)
				}
			})
		}
	}
}

// The control: the same driver, with the check answering, sends the lock,
// the count and the write inside one transaction and commits — so the test
// above fails for the reason it names, not because the fake cannot write.
func TestLockedAdmissionWritesInsideTheTransactionWhenTheCheckAnswers(t *testing.T) {
	for name, write := range admissionWrites() {
		t.Run(name, func(t *testing.T) {
			s := &scriptedDB{count: 3}
			if _, err := write(scriptedRepo(t, s), 25); err != nil {
				t.Fatalf("admission below the cap: %v", err)
			}
			log := s.entries()
			want := []func(string) bool{
				func(q string) bool { return q == "BEGIN" }, isLock, isCount, isWrite,
				func(q string) bool { return q == "COMMIT" },
			}
			if len(log) != len(want) {
				t.Fatalf("statements = %q, want BEGIN, lock, count, write, COMMIT", log)
			}
			for i, ok := range want {
				if !ok(log[i]) {
					t.Errorf("statement %d = %q\nstatements: %q", i, log[i], log)
				}
			}
		})
	}
}

// An unlimited cap is a plain write: no transaction, no lock, no count — so
// a database that could not answer them cannot refuse anything.
func TestUnlimitedAdmissionNeverAsksForTheLockOrTheCount(t *testing.T) {
	for name, write := range admissionWrites() {
		t.Run(name, func(t *testing.T) {
			s := &scriptedDB{fail: func(q string) error {
				if isLock(q) || isCount(q) || q == "BEGIN" {
					return errors.New("must not be asked")
				}
				return nil
			}}
			if _, err := write(scriptedRepo(t, s), 0); err != nil {
				t.Fatalf("unlimited admission: %v", err)
			}
			log := s.entries()
			if len(log) != 1 || !isWrite(log[0]) {
				t.Errorf("statements = %q, want the write alone", log)
			}
		})
	}
}
