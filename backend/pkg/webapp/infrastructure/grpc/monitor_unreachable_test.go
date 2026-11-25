package grpc

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// Both stubs embed their interface: only the calls this pass is allowed to make
// are implemented, and anything else panics on the nil embedded interface.

type stuckFakeNodeService struct {
	services.NodeService

	stale        []node.Node
	askedFor     time.Time
	listErr      error
	statusWrites []string
	statusIDs    []uuid.UUID
	statusErr    error
}

func (f *stuckFakeNodeService) GetStaleTransientNodes(_ context.Context, threshold time.Time) ([]node.Node, error) {
	f.askedFor = threshold
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.stale, nil
}

func (f *stuckFakeNodeService) UpdateStatus(_ context.Context, nodeID uuid.UUID, status string) error {
	if f.statusErr != nil {
		return f.statusErr
	}
	f.statusIDs = append(f.statusIDs, nodeID)
	f.statusWrites = append(f.statusWrites, status)
	return nil
}

type stuckFakeCommandService struct {
	services.NodeCommandService

	settled []uuid.UUID
	count   int
	err     error
	// order records the sequence of the two side effects, so the test can prove
	// commands are settled before the node is declared offline.
	order *[]string
}

func (f *stuckFakeCommandService) FailCommandsForUnreachableNode(_ context.Context, nodeID uuid.UUID) (int, error) {
	f.settled = append(f.settled, nodeID)
	if f.order != nil {
		*f.order = append(*f.order, "commands")
	}
	if f.err != nil {
		return 0, f.err
	}
	return f.count, nil
}

type orderingNodeService struct {
	stuckFakeNodeService
	order *[]string
}

func (o *orderingNodeService) UpdateStatus(ctx context.Context, nodeID uuid.UUID, status string) error {
	*o.order = append(*o.order, "status")
	return o.stuckFakeNodeService.UpdateStatus(ctx, nodeID, status)
}

func stuckNode(status node.NodeStatus, lastSeen *time.Time) node.Node {
	return node.Node{
		ID:         uuid.New(),
		AccountID:  uuid.New(),
		Status:     status,
		LastSeenAt: lastSeen,
	}
}

func stuckMonitor(nodeSvc services.NodeService, cmdSvc services.NodeCommandService) *Monitor {
	return &Monitor{
		nodeService:        nodeSvc,
		nodeCommandService: cmdSvc,
		logger:             slog.Default(),
		offlineThreshold:   90 * time.Second,
		transientGrace:     15 * time.Minute,
	}
}

func noConnections() map[string]*StreamConnection { return map[string]*StreamConnection{} }

// Without this, a node that never returns from a reboot would read 'restarting'
// forever, so no pill and no alert sees it, and the dispatch path will not even
// accept a status_check to diagnose it.
func TestANodeThatNeverCameBackIsMarkedOfflineAndItsCommandsSettled(t *testing.T) {
	dead := stuckNode(node.StatusRestarting, ptrTime(time.Now().Add(-time.Hour)))
	nodeSvc := &stuckFakeNodeService{stale: []node.Node{dead}}
	cmdSvc := &stuckFakeCommandService{count: 1}
	m := stuckMonitor(nodeSvc, cmdSvc)

	m.settleUnreachableTransientNodes(context.Background(), time.Now(), noConnections())

	if len(nodeSvc.statusWrites) != 1 || nodeSvc.statusWrites[0] != string(node.StatusOffline) {
		t.Fatalf("status writes %v, want one 'offline'", nodeSvc.statusWrites)
	}
	if len(cmdSvc.settled) != 1 || cmdSvc.settled[0] != dead.ID {
		t.Fatalf("settled %v, want the dead node's commands", cmdSvc.settled)
	}
}

// 'maintenance' has the same shape: it is set at dispatch and only the device
// clears it, so a device that dies mid-command leaves the node in it forever.
func TestAMaintenanceNodeIsBoundedToo(t *testing.T) {
	stuck := stuckNode(node.StatusMaintenance, ptrTime(time.Now().Add(-time.Hour)))
	nodeSvc := &stuckFakeNodeService{stale: []node.Node{stuck}}
	m := stuckMonitor(nodeSvc, &stuckFakeCommandService{count: 2})

	m.settleUnreachableTransientNodes(context.Background(), time.Now(), noConnections())

	if len(nodeSvc.statusWrites) != 1 {
		t.Fatalf("status writes %v, want the node freed", nodeSvc.statusWrites)
	}
}

// The grace is what keeps a healthy reboot from tripping this: the query is
// asked for nodes unheard-from since now minus the grace, never for all of them.
func TestTheQueryIsBoundedByTheGrace(t *testing.T) {
	nodeSvc := &stuckFakeNodeService{}
	m := stuckMonitor(nodeSvc, &stuckFakeCommandService{})
	now := time.Now()

	m.settleUnreachableTransientNodes(context.Background(), now, noConnections())

	want := now.Add(-15 * time.Minute)
	if diff := nodeSvc.askedFor.Sub(want); diff > time.Second || diff < -time.Second {
		t.Fatalf("asked for nodes stale since %s, want %s", nodeSvc.askedFor, want)
	}
}

// A device that is answering must never be declared dead, whatever the stored
// last_seen_at says.
func TestANodeWithALiveStreamIsLeftAlone(t *testing.T) {
	live := stuckNode(node.StatusRestarting, ptrTime(time.Now().Add(-time.Hour)))
	nodeSvc := &stuckFakeNodeService{stale: []node.Node{live}}
	cmdSvc := &stuckFakeCommandService{}
	m := stuckMonitor(nodeSvc, cmdSvc)

	connections := map[string]*StreamConnection{
		live.ID.String(): connSeenAt(time.Now()),
	}
	m.settleUnreachableTransientNodes(context.Background(), time.Now(), connections)

	if len(nodeSvc.statusWrites) != 0 || len(cmdSvc.settled) != 0 {
		t.Fatalf("touched a node whose agent is still streaming: statuses=%v settled=%v",
			nodeSvc.statusWrites, cmdSvc.settled)
	}
}

// A registry entry that is itself stale is not evidence of life — that is the
// case the two earlier passes exist to clean up.
func TestAStaleRegistryEntryDoesNotProtectTheNode(t *testing.T) {
	dead := stuckNode(node.StatusRestarting, ptrTime(time.Now().Add(-time.Hour)))
	nodeSvc := &stuckFakeNodeService{stale: []node.Node{dead}}
	m := stuckMonitor(nodeSvc, &stuckFakeCommandService{})

	connections := map[string]*StreamConnection{
		dead.ID.String(): connSeenAt(time.Now().Add(-30 * time.Minute)),
	}
	m.settleUnreachableTransientNodes(context.Background(), time.Now(), connections)

	if len(nodeSvc.statusWrites) != 1 {
		t.Fatalf("status writes %v, want the node freed despite the stale registry entry", nodeSvc.statusWrites)
	}
}

// Commands first: the reverse order would report a node offline while its reboot
// still claims to be in progress, which is the state this whole fix removes.
func TestCommandsAreSettledBeforeTheNodeIsDeclaredOffline(t *testing.T) {
	order := []string{}
	dead := stuckNode(node.StatusRestarting, ptrTime(time.Now().Add(-time.Hour)))
	nodeSvc := &orderingNodeService{
		stuckFakeNodeService: stuckFakeNodeService{stale: []node.Node{dead}},
		order:                &order,
	}
	m := stuckMonitor(nodeSvc, &stuckFakeCommandService{count: 1, order: &order})

	m.settleUnreachableTransientNodes(context.Background(), time.Now(), noConnections())

	if len(order) != 2 || order[0] != "commands" || order[1] != "status" {
		t.Fatalf("side effects ran in order %v, want commands then status", order)
	}
}

// A command settle that fails must not block the status transition: the node is
// still gone, and the next tick retries the commands.
func TestAFailedCommandSettleStillFreesTheNode(t *testing.T) {
	dead := stuckNode(node.StatusRestarting, ptrTime(time.Now().Add(-time.Hour)))
	nodeSvc := &stuckFakeNodeService{stale: []node.Node{dead}}
	m := stuckMonitor(nodeSvc, &stuckFakeCommandService{err: errors.New("database is down")})

	m.settleUnreachableTransientNodes(context.Background(), time.Now(), noConnections())

	if len(nodeSvc.statusWrites) != 1 {
		t.Fatalf("status writes %v, want the node freed anyway", nodeSvc.statusWrites)
	}
}

// A monitor whose command service was never wired still frees the status, which
// is the half that unblocks the operator.
func TestTheStatusIsFreedWithNoCommandServiceWired(t *testing.T) {
	dead := stuckNode(node.StatusRestarting, ptrTime(time.Now().Add(-time.Hour)))
	nodeSvc := &stuckFakeNodeService{stale: []node.Node{dead}}
	m := stuckMonitor(nodeSvc, nil)

	m.settleUnreachableTransientNodes(context.Background(), time.Now(), noConnections())

	if len(nodeSvc.statusWrites) != 1 {
		t.Fatalf("status writes %v, want one 'offline'", nodeSvc.statusWrites)
	}
}

// Zero grace disables the pass rather than meaning "immediately", so a
// misconfiguration cannot declare every rebooting node dead.
func TestAZeroGraceDisablesThePass(t *testing.T) {
	nodeSvc := &stuckFakeNodeService{stale: []node.Node{stuckNode(node.StatusRestarting, nil)}}
	m := stuckMonitor(nodeSvc, &stuckFakeCommandService{})
	m.transientGrace = 0

	m.settleUnreachableTransientNodes(context.Background(), time.Now(), noConnections())

	if !nodeSvc.askedFor.IsZero() || len(nodeSvc.statusWrites) != 0 {
		t.Fatal("ran with the grace disabled")
	}
}

func TestAListFailureIsLoggedNotFatal(t *testing.T) {
	nodeSvc := &stuckFakeNodeService{listErr: errors.New("database is down")}
	m := stuckMonitor(nodeSvc, &stuckFakeCommandService{})

	m.settleUnreachableTransientNodes(context.Background(), time.Now(), noConnections())

	if len(nodeSvc.statusWrites) != 0 {
		t.Fatal("wrote a status off a failed listing")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// connSeenAt builds a registry entry whose stream activity is stamped at t —
// LastSeen moved behind an atomic, so tests set it as the code
// does, through Touch.
func connSeenAt(t time.Time) *StreamConnection {
	c := &StreamConnection{}
	c.Touch(t)
	return c
}

// Six frame handlers stamp activity while the monitor reads it; run under
// -race this is the regression guard for the atomic.
func TestLastSeenTouchAndReadAreRaceFree(t *testing.T) {
	c := &StreamConnection{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			c.Touch(time.Now())
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = c.LastSeenAt()
	}
	<-done
}
