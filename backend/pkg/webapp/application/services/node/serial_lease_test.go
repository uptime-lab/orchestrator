package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

type leaseFakeRepo struct {
	repository.NodeRepository
	released []repository.ReleasedSerialLease
	err      error
	cutoff   time.Time
}

func (f *leaseFakeRepo) ReleaseUnclaimedSerialLeases(_ context.Context, unseenSince time.Time) ([]repository.ReleasedSerialLease, error) {
	f.cutoff = unseenSince
	return f.released, f.err
}

func (f *leaseFakeRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	return nil, app_errors.ErrNodeNotFound
}

// Every release is a platform action against a tenant's row with no human
// behind it, so the tenant's audit trail must say so — system actor, the
// node as target, the serial in the detail. A release nobody can find later
// reads as data loss.
func TestReleasingASerialLeaseIsAuditedPerNodeAsTheSystem(t *testing.T) {
	a1, a2 := uuid.New(), uuid.New()
	n1, n2 := uuid.New(), uuid.New()
	repo := &leaseFakeRepo{released: []repository.ReleasedSerialLease{
		{NodeID: n1, AccountID: a1, HardwareSerial: "1000000000000001"},
		{NodeID: n2, AccountID: a2, HardwareSerial: "1000000000000002"},
	}}
	recorder, sink := audittest.New()
	svc := &Service{nodeRepo: repo, audit: recorder, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	n, err := svc.ReleaseUnclaimedSerialLeases(context.Background(), cutoff)
	if err != nil || n != 2 {
		t.Fatalf("got %d, %v; want 2, nil", n, err)
	}
	if !repo.cutoff.Equal(cutoff) {
		t.Errorf("cutoff passed through: got %v, want %v", repo.cutoff, cutoff)
	}
	events := sink.Events()
	if len(events) != 2 {
		t.Fatalf("got %d audit events, want 2: %v", len(events), sink.Actions())
	}
	seen := map[uuid.UUID]bool{}
	for _, e := range events {
		if e.Action != audit_event.ActionNodeSerialLeaseReleased {
			t.Errorf("action %q", e.Action)
		}
		if e.ActorUserID != nil {
			t.Errorf("a lease release has no human actor, got %v", e.ActorUserID)
		}
		seen[e.AccountID] = true
		if e.TargetType != audit_event.TargetNode || (e.TargetID != n1.String() && e.TargetID != n2.String()) {
			t.Errorf("target %s/%s", e.TargetType, e.TargetID)
		}
		if !strings.Contains(e.Detail, "serial=") {
			t.Errorf("detail must name the serial: %q", e.Detail)
		}
	}
	if !seen[a1] || !seen[a2] {
		t.Errorf("each row must be recorded in ITS account: %v", seen)
	}
}

func TestReleasingSerialLeasesReportsARepositoryFailure(t *testing.T) {
	recorder, sink := audittest.New()
	svc := &Service{nodeRepo: &leaseFakeRepo{err: errors.New("deadlock")}, audit: recorder, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := svc.ReleaseUnclaimedSerialLeases(context.Background(), time.Now()); err == nil {
		t.Fatal("expected the failure to be reported")
	}
	if len(sink.Actions()) != 0 {
		t.Errorf("nothing was released, nothing may be audited: %v", sink.Actions())
	}
}
