package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

type fakeCommands struct {
	repository.NodeCommandRepository
	calls   int
	deleted []int64
	err     error
}

func (f *fakeCommands) DeleteTerminalOlderThan(_ context.Context, _ time.Time, _ int) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.calls >= len(f.deleted) {
		return 0, nil
	}
	n := f.deleted[f.calls]
	f.calls++
	return n, nil
}

type fakeAudit struct {
	calls   int
	deleted []int64
	// cutoffs records each delete's scope ("default" or an account id) and
	// cutoff, in call order.
	cutoffs   []auditDelete
	overrides []repository.AuditRetentionOverride
	listErr   error
	// defaults is what ListInstallDefaultAuditAccounts answers; nil stands for
	// one account on the install default. defaultIDs records which account
	// each install-default delete was for.
	defaults        []uuid.UUID
	defaultsErr     error
	defaultIDs      []uuid.UUID
	defaultFailFrom int // 1-based install-default delete that starts failing; 0 = never
}

type auditDelete struct {
	scope  string
	cutoff time.Time
}

func (f *fakeAudit) next() int64 {
	if f.calls >= len(f.deleted) {
		f.calls++
		return 0
	}
	n := f.deleted[f.calls]
	f.calls++
	return n
}

func (f *fakeAudit) ListAuditRetentionOverrides(context.Context) ([]repository.AuditRetentionOverride, error) {
	return f.overrides, f.listErr
}

func (f *fakeAudit) ListInstallDefaultAuditAccounts(context.Context) ([]uuid.UUID, error) {
	if f.defaultsErr != nil {
		return nil, f.defaultsErr
	}
	if f.defaults == nil {
		return []uuid.UUID{uuid.Nil}, nil
	}
	return f.defaults, nil
}

func (f *fakeAudit) DeleteAccountOlderThanAtInstallDefault(_ context.Context, accountID uuid.UUID, cutoff time.Time, _ int) (int64, error) {
	f.cutoffs = append(f.cutoffs, auditDelete{"default", cutoff})
	f.defaultIDs = append(f.defaultIDs, accountID)
	if f.defaultFailFrom > 0 && len(f.defaultIDs) >= f.defaultFailFrom {
		return 0, errors.New("connection reset")
	}
	return f.next(), nil
}

func (f *fakeAudit) DeleteAccountOlderThan(_ context.Context, o repository.AuditRetentionOverride, cutoff time.Time, _ int) (int64, error) {
	f.cutoffs = append(f.cutoffs, auditDelete{o.AccountID.String(), cutoff})
	return f.next(), nil
}

var sweepNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func newService(cfg Config, cmds *fakeCommands, audit *fakeAudit) *Service {
	svc := NewService(cfg, cmds, audit, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return sweepNow }
	return svc
}

func daysBefore(days int) time.Time { return sweepNow.Add(-time.Duration(days) * 24 * time.Hour) }

// Zero means unlimited: with both install periods at 0 and no account
// carrying its own, nothing is deleted.
func TestDisabledRetentionDeletesNothing(t *testing.T) {
	cmds, audit := &fakeCommands{}, &fakeAudit{}
	svc := newService(Config{}, cmds, audit)

	svc.sweep(context.Background())

	if cmds.calls != 0 || audit.calls != 0 {
		t.Errorf("disabled retention deleted anyway: commands=%d audit=%d", cmds.calls, audit.calls)
	}
}

// Each account is swept at its own period when it has one, at the install
// default otherwise; 0 keeps that account's trail forever, and a period
// below the floor is raised to it, so no setting deletes the rows a rate
// limit is counted off.
func TestAuditSweepHonoursEachAccountsPeriod(t *testing.T) {
	shorter, forever, tooShort, longer := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	audit := &fakeAudit{overrides: []repository.AuditRetentionOverride{
		{AccountID: shorter, Days: 30},
		{AccountID: forever, Days: 0},
		{AccountID: tooShort, Days: 1},
		{AccountID: longer, Days: 1000},
	}}
	svc := newService(Config{AuditEventDays: 365}, &fakeCommands{}, audit)

	svc.sweepAuditEvents(context.Background())

	want := []auditDelete{
		{"default", daysBefore(365)},
		{shorter.String(), daysBefore(30)},
		{tooShort.String(), daysBefore(audit_event.MinRetentionDays)},
		{longer.String(), daysBefore(1000)},
	}
	if len(audit.cutoffs) != len(want) {
		t.Fatalf("deletes = %+v, want %+v", audit.cutoffs, want)
	}
	for i := range want {
		if audit.cutoffs[i].scope != want[i].scope || !audit.cutoffs[i].cutoff.Equal(want[i].cutoff) {
			t.Errorf("delete %d = %+v, want %+v", i, audit.cutoffs[i], want[i])
		}
	}
}

// An install that keeps its trail forever still honours an account given a
// bounded period of its own.
func TestAuditOverrideIsSweptWhenTheInstallKeepsForever(t *testing.T) {
	account := uuid.New()
	audit := &fakeAudit{overrides: []repository.AuditRetentionOverride{{AccountID: account, Days: 90}}}
	svc := newService(Config{}, &fakeCommands{}, audit)

	svc.sweepAuditEvents(context.Background())

	if len(audit.cutoffs) != 1 || audit.cutoffs[0].scope != account.String() || !audit.cutoffs[0].cutoff.Equal(daysBefore(90)) {
		t.Errorf("deletes = %+v, want only the account's, at 90 days", audit.cutoffs)
	}
}

// A failed read of the per-account periods sweeps none of those accounts —
// never at the install default, which may be shorter than theirs.
func TestAuditOverrideReadFailureSweepsOnlyTheDefault(t *testing.T) {
	audit := &fakeAudit{listErr: errors.New("connection reset"),
		overrides: []repository.AuditRetentionOverride{{AccountID: uuid.New(), Days: 30}}}
	svc := newService(Config{AuditEventDays: 365}, &fakeCommands{}, audit)

	svc.sweepAuditEvents(context.Background())

	if len(audit.cutoffs) != 1 || audit.cutoffs[0].scope != "default" {
		t.Errorf("deletes = %+v, want the install default's only", audit.cutoffs)
	}
}

// The two periods are independent: an operator who wants command history
// pruned but the audit trail kept forever must get exactly that.
func TestPeriodsAreIndependent(t *testing.T) {
	cmds, audit := &fakeCommands{deleted: []int64{10}}, &fakeAudit{deleted: []int64{10}}
	svc := newService(Config{NodeCommandDays: 30}, cmds, audit)

	svc.sweep(context.Background())

	if cmds.calls == 0 {
		t.Error("command retention was configured and did not run")
	}
	if audit.calls != 0 {
		t.Error("audit events were swept without an audit retention period — the trail is not the noise")
	}
}

// A full batch means there is probably more; a short one means the table is
// clean. Stopping early would spread a large backlog over many hours.
func TestSweepLoopsWhileBatchesAreFull(t *testing.T) {
	cmds := &fakeCommands{deleted: []int64{batchSize, batchSize, 12}}
	svc := newService(Config{NodeCommandDays: 1}, cmds, &fakeAudit{})

	svc.sweepNodeCommands(context.Background())

	if cmds.calls != 3 {
		t.Errorf("made %d delete rounds, want 3 (two full batches then a short one)", cmds.calls)
	}
}

// A failing sweep must give up rather than spin: the next tick is an hour away
// and the table is not going anywhere.
func TestSweepStopsOnError(t *testing.T) {
	cmds := &fakeCommands{err: errors.New("deadlock detected")}
	svc := newService(Config{NodeCommandDays: 1}, cmds, &fakeAudit{})

	done := make(chan struct{})
	go func() {
		svc.sweepNodeCommands(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a failing sweep did not return; it is looping on the error")
	}
}

func TestBothRepositoriesAreRequired(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a service missing a repository must panic at construction, not nil-deref on the first tick")
		}
	}()
	NewService(Config{}, nil, &fakeAudit{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// The install default is swept one account at a time, each under the
// default's cutoff, so no statement reads the history of an account kept
// longer than the default only to discard it.
func TestInstallDefaultIsSweptPerAccount(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	audit := &fakeAudit{defaults: []uuid.UUID{a, b}, deleted: []int64{int64(batchSize), 3, 0}}
	svc := newService(Config{AuditEventDays: 90}, &fakeCommands{}, audit)

	svc.sweepAuditEvents(context.Background())

	// a: a full batch, then a short one; b: one short batch.
	want := []uuid.UUID{a, a, b}
	if len(audit.defaultIDs) != len(want) {
		t.Fatalf("install-default deletes for %v, want %v", audit.defaultIDs, want)
	}
	for i := range want {
		if audit.defaultIDs[i] != want[i] {
			t.Errorf("delete %d was for %s, want %s", i, audit.defaultIDs[i], want[i])
		}
		if !audit.cutoffs[i].cutoff.Equal(daysBefore(90)) {
			t.Errorf("delete %d cutoff = %v, want the default's", i, audit.cutoffs[i].cutoff)
		}
	}
}

// A failure ends the install-default group, as it ends any group; the
// accounts with their own period are still swept.
func TestInstallDefaultFailureEndsOnlyThatGroup(t *testing.T) {
	own := uuid.New()
	audit := &fakeAudit{defaults: []uuid.UUID{uuid.New(), uuid.New()}, defaultFailFrom: 1,
		overrides: []repository.AuditRetentionOverride{{AccountID: own, Days: 30}}}
	svc := newService(Config{AuditEventDays: 90}, &fakeCommands{}, audit)

	svc.sweepAuditEvents(context.Background())

	if len(audit.defaultIDs) != 1 {
		t.Errorf("install-default deletes = %d after a failure, want 1", len(audit.defaultIDs))
	}
	last := audit.cutoffs[len(audit.cutoffs)-1]
	if last.scope != own.String() {
		t.Errorf("last delete = %+v, want the account with its own period", last)
	}
}

// A failed read of the install-default accounts sweeps none of them, and
// leaves the accounts with their own period to their own sweep.
func TestInstallDefaultListFailureSweepsOnlyTheOverrides(t *testing.T) {
	own := uuid.New()
	audit := &fakeAudit{defaultsErr: errors.New("connection reset"),
		overrides: []repository.AuditRetentionOverride{{AccountID: own, Days: 30}}}
	svc := newService(Config{AuditEventDays: 90}, &fakeCommands{}, audit)

	svc.sweepAuditEvents(context.Background())

	if len(audit.cutoffs) != 1 || audit.cutoffs[0].scope != own.String() {
		t.Errorf("deletes = %+v, want only the account with its own period", audit.cutoffs)
	}
}
