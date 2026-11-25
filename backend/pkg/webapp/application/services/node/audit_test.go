package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// Deleting a node is the most destructive single call in the API: it takes the
// node's tracked files, their whole version history and its adoption codes with
// it, and it must leave a record of who did it.

type deleteFakeRepo struct {
	repository.NodeRepository
	nodes   map[uuid.UUID]*node.Node
	deleted []uuid.UUID
}

func (f *deleteFakeRepo) GetByID(_ context.Context, _, id uuid.UUID) (*node.Node, error) {
	n, ok := f.nodes[id]
	if !ok {
		return nil, app_errors.ErrNodeNotFound
	}
	return n, nil
}

func (f *deleteFakeRepo) Delete(_ context.Context, _, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	delete(f.nodes, id)
	return nil
}

func TestDeleteNodeIsAudited(t *testing.T) {
	accountID, actorUserID, nodeID := uuid.New(), uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {
			ID:        nodeID,
			AccountID: accountID,
			Hostname:  "rpi-abcd0001",
			Status:    node.StatusOnline,
			Fingerprint: &node.HardwareFingerprint{
				HardwareSerial: "10000000abcd0001",
			},
		},
	}}
	recorder, sink := audittest.New()
	svc := &Service{
		nodeRepo: repo,
		audit:    recorder,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// force: this fixture's node is ONLINE, which the delete now refuses without
	// it. Deleting an online node is what strands the device.
	if err := svc.Delete(context.Background(), accountID, actorUserID, nodeID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionNodeDelete)
	if !ok {
		t.Fatalf("no node.delete event; recorded %v", sink.Actions())
	}
	if event.ActorKind != audit_event.ActorUser {
		t.Errorf("actor kind = %q, want a user", event.ActorKind)
	}
	if event.ActorUserID == nil || *event.ActorUserID != actorUserID {
		t.Errorf("actor = %v, want %v", event.ActorUserID, actorUserID)
	}
	if event.AccountID != accountID {
		t.Errorf("account = %v, want %v", event.AccountID, accountID)
	}
	if event.TargetType != audit_event.TargetNode || event.TargetID != nodeID.String() {
		t.Errorf("target = %s/%s, want node/%s", event.TargetType, event.TargetID, nodeID)
	}
	// Recorded from the PRE-delete copy: afterwards the id resolves to nothing,
	// and the hostname and serial are what an operator remembers a node by.
	for _, want := range []string{"rpi-abcd0001", "10000000abcd0001"} {
		if !strings.Contains(event.Detail, want) {
			t.Errorf("detail %q is missing %q — it was not read before the delete", event.Detail, want)
		}
	}
}

// A delete that never happened must not be recorded as one.
func TestAFailedDeleteRecordsNothing(t *testing.T) {
	recorder, sink := audittest.New()
	svc := &Service{
		nodeRepo: &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{}},
		audit:    recorder,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.Delete(context.Background(), uuid.New(), uuid.New(), uuid.New(), true); err == nil {
		t.Fatal("expected an error deleting a node that does not exist")
	}
	if actions := sink.Actions(); len(actions) != 0 {
		t.Errorf("a failed delete recorded %v", actions)
	}
}

func TestNodeNewServiceRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewService(&deleteFakeRepo{}, nil, nil, nil, nil, nil, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Fingerprint is nullable and routinely nil here: a WebUSB placeholder, or a node
// created but never connected, has none — and the serial-conflict rollbacks in
// the adoption flows delete exactly those. Dereferencing it turned the audit row
// into a panic on the most common delete there is.
func TestDeletingANodeWithNoFingerprintDoesNotPanic(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Hostname: "", Status: node.StatusFlashing},
	}}
	recorder, sink := audittest.New()
	svc := &Service{
		nodeRepo: repo,
		audit:    recorder,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}
	event, ok := sink.Find(audit_event.ActionNodeDelete)
	if !ok {
		t.Fatalf("no node.delete event; recorded %v", sink.Actions())
	}
	if !strings.Contains(event.Detail, `serial=""`) {
		t.Errorf("detail %q should record an empty serial rather than inventing one", event.Detail)
	}
}

// --- execution-abandoner wiring --------------------------------------------

// stubAbandoner records the settle call node deletion is supposed to make.
type stubAbandoner struct {
	calls  int
	nodeID uuid.UUID
	err    error
}

func (s *stubAbandoner) AbandonNodeExecutions(_ context.Context, _, nodeID uuid.UUID) (int, error) {
	s.calls++
	s.nodeID = nodeID
	return 1, s.err
}

// Deleting a node must settle the workflow runs targeting it. Without this call
// the run stays active forever: its only settler runs on reconnect, which a
// deleted node never does, so it consumes the account's active-execution cap and
// retention never sweeps it.
func TestDeletingANodeSettlesItsWorkflowRuns(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Hostname: "rpi-1", Status: node.StatusOnline},
	}}
	recorder, _ := audittest.New()
	abandoner := &stubAbandoner{}
	svc := &Service{
		nodeRepo:           repo,
		audit:              recorder,
		executionAbandoner: abandoner,
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if abandoner.calls != 1 {
		t.Fatalf("the workflow settle was called %d times, want 1", abandoner.calls)
	}
	if abandoner.nodeID != nodeID {
		t.Errorf("settled runs for %v, want %v", abandoner.nodeID, nodeID)
	}
}

// The settle is bookkeeping about a node that is ALREADY gone. Failing the
// request would leave the operator with neither the node nor a way to retry, so
// the delete must still succeed — the remedy for a failed settle is the cancel
// endpoint.
func TestAFailedWorkflowSettleDoesNotFailTheDelete(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Status: node.StatusOnline},
	}}
	recorder, sink := audittest.New()
	svc := &Service{
		nodeRepo:           repo,
		audit:              recorder,
		executionAbandoner: &stubAbandoner{err: errors.New("database is down")},
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete failed because its workflow bookkeeping did: %v", err)
	}
	if _, ok := sink.Find(audit_event.ActionNodeDelete); !ok {
		t.Error("the delete was not audited")
	}
}
