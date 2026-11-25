package grpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// The invariant these tests pin: the live OU scope of a connected node always
// equals the last COMMITTED value of its row. Two races, each confirmed
// against the first fix (a plain copy written by the assignment path), break
// it, and each is driven here deterministically — no sleeps, the
// interleaving is produced by the stub's own lookup.

// Race (a): registration reads the unit, an assignment commits a new one and
// calls UpdateNodeOU — which finds no connection yet — and registration then
// publishes the value it read. Stale until the node reconnects.
//
// The stub's FIRST lookup is the registration's pre-publish read; it performs
// the assignment (commit B, notify the registry) before yielding the snapshot
// it took, A. From the registration path's point of view the read has answered
// A and the connection is not yet published — exactly the window.
func TestRegistrationCapturesAnAssignmentCommittedBeforePublish(t *testing.T) {
	nodeID, accountID, ouA, ouB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	row := &committedRow{node: committedNode(nodeID, accountID, &ouA)}
	svc := &stubNodeService{connected: committedNode(nodeID, accountID, &ouA)}
	srv := testServer(svc)
	ws := &capturingWebsocketClient{}
	srv.websocketClient = ws

	var reads int
	svc.lookup = func(ctx context.Context, n, a uuid.UUID) (*node.Node, error) {
		reads++
		if reads == 1 {
			// The assignment lands while registration holds its snapshot.
			row.set(committedNode(nodeID, accountID, &ouB))
			srv.UpdateNodeOU(nodeID.String(), &ouB)
			return committedNode(nodeID, accountID, &ouA), nil
		}
		return row.lookup(ctx, n, a)
	}

	stream := &scriptedStream{
		ctx: context.Background(),
		incoming: []*proto.NodeToPlatform{
			connectionInit(nodeID, accountID),
			{
				NodeId:    nodeID.String(),
				AccountId: accountID.String(),
				Payload:   &proto.NodeToPlatform_MetricsUpdate{MetricsUpdate: &proto.MetricsUpdate{}},
			},
		},
	}
	// The scripted peer ends with EOF once its frames are consumed; what must
	// not happen is a refusal.
	if err := srv.EstablishStream(stream); status.Code(err) == codes.FailedPrecondition || status.Code(err) == codes.Unauthenticated {
		t.Fatalf("EstablishStream refused the session: %v", err)
	}

	var metrics []websockets3.Message
	for _, m := range ws.sent {
		if w, ok := m.Payload.(websockets3.MessageWrapper); ok && w.Type == "node_metrics" {
			metrics = append(metrics, m)
		}
	}
	if len(metrics) != 1 {
		t.Fatalf("the metrics sample fanned out %d node_metrics messages, want 1", len(metrics))
	}
	msg := metrics[0]
	if msg.ScopeOU == nil || *msg.ScopeOU != ouB {
		t.Errorf("the first sample after connect is scoped to %v, want unit B %s: the assignment committed before publish was lost, and this stream stays on unit A until the node reconnects", msg.ScopeOU, ouB)
	}
}

// Race (b): two assignments commit A then B but their in-memory updates land
// B then A. Driven as the sequence the mutex must make harmless: both commits
// are in the row before either apply, and the apply carrying the OLDER value
// runs last. A refresh that trusts its argument ends on A; one that re-reads
// the committed row under the per-connection lock ends on B whatever the order.
func TestOutOfOrderScopeAppliesConvergeOnTheCommittedValue(t *testing.T) {
	nodeID, accountID, ou0, ouA, ouB := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	row := &committedRow{node: committedNode(nodeID, accountID, &ou0)}
	srv := testServer(&stubNodeService{lookup: row.lookup})
	ws := &capturingWebsocketClient{}
	srv.websocketClient = ws

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newStreamConnection(ctx, srv.logger, nodeID.String(), accountID.String(), &ou0,
		&scriptedStream{ctx: ctx}, time.Now(), cancel)
	srv.connections[nodeID.String()] = conn
	defer conn.Stop()

	// Commit A, commit B — B is the row's final value.
	row.set(committedNode(nodeID, accountID, &ouA))
	row.set(committedNode(nodeID, accountID, &ouB))
	// The in-memory updates arrive in the reverse order.
	srv.UpdateNodeOU(nodeID.String(), &ouB)
	srv.UpdateNodeOU(nodeID.String(), &ouA)

	msg := lastMetricsScope(t, srv, ws, nodeID, accountID)
	if msg.ScopeOU == nil || *msg.ScopeOU != ouB {
		t.Errorf("live scope is %v while the row says unit B %s: the later-committed assignment lost to the earlier one's in-memory update", msg.ScopeOU, ouB)
	}
}

// The same property under real concurrency, for the race detector: many
// refreshes racing each other and the metrics fan-out, with the row settled on
// B before any of them start. Whatever order they run in, the live scope must
// end on B and no read or write of the copy may race.
func TestConcurrentScopeRefreshesAreRaceFreeAndConverge(t *testing.T) {
	nodeID, accountID, ouA, ouB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	row := &committedRow{node: committedNode(nodeID, accountID, &ouB)}
	srv := testServer(&stubNodeService{lookup: row.lookup})
	ws := &capturingWebsocketClient{}
	srv.websocketClient = ws

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newStreamConnection(ctx, srv.logger, nodeID.String(), accountID.String(), &ouA,
		&scriptedStream{ctx: ctx}, time.Now(), cancel)
	srv.mu.Lock()
	srv.connections[nodeID.String()] = conn
	srv.mu.Unlock()
	defer conn.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		arg := &ouA
		if i%2 == 0 {
			arg = &ouB
		}
		go func() {
			defer wg.Done()
			srv.UpdateNodeOU(nodeID.String(), arg)
		}()
	}
	// The fan-out reads the copy concurrently, the way recordMetrics does (the
	// websocket stub is not goroutine-safe, so the read is mirrored here).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 16; i++ {
			srv.mu.RLock()
			if c, ok := srv.connections[nodeID.String()]; ok {
				_ = c.OUID
			}
			srv.mu.RUnlock()
		}
	}()
	wg.Wait()

	msg := lastMetricsScope(t, srv, ws, nodeID, accountID)
	if msg.ScopeOU == nil || *msg.ScopeOU != ouB {
		t.Errorf("after concurrent refreshes the live scope is %v, want the committed unit B %s", msg.ScopeOU, ouB)
	}
}

// A node with no live stream has nothing to refresh: the update stays a no-op
// AND performs no database read — the assignment path calls this for every
// node it moves, most of which are not connected.
func TestUpdateNodeOUWithoutAStreamReadsNothing(t *testing.T) {
	var reads int
	srv := testServer(&stubNodeService{lookup: func(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
		reads++
		return nil, nil
	}})
	ou := uuid.New()
	srv.UpdateNodeOU(uuid.NewString(), &ou)
	if reads != 0 {
		t.Errorf("a node with no live stream cost %d database read(s), want none", reads)
	}
	if len(srv.connections) != 0 {
		t.Errorf("updating an unknown node changed the registry: %d entries", len(srv.connections))
	}
}

// When the committed row cannot be read the refresh must not guess in EITHER
// direction: the argument may already have been overtaken by a later commit,
// and the previous copy is the stale unit a move must stop feeding. It fails
// closed — the scope narrows to "unassigned", which reaches account-wide
// holders only — logs a warning naming the node, and retries; once a read
// succeeds the committed unit is published.
func TestScopeReadFailureNarrowsTheScopeThenRetries(t *testing.T) {
	nodeID, accountID, ouA, ouB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var failing atomic.Bool
	failing.Store(true)
	srv := testServer(&stubNodeService{lookup: func(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
		if failing.Load() {
			return nil, errors.New("connection reset by peer")
		}
		return &node.Node{ID: nodeID, AccountID: accountID, OUID: &ouB}, nil
	}})
	var logs bytes.Buffer
	srv.logger = slog.New(slog.NewTextHandler(&logs, nil))
	ws := &capturingWebsocketClient{}
	srv.websocketClient = ws

	prevDelay := scopeRefreshRetryDelay
	scopeRefreshRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() { scopeRefreshRetryDelay = prevDelay })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newStreamConnection(ctx, srv.logger, nodeID.String(), accountID.String(), &ouA,
		&scriptedStream{ctx: ctx}, time.Now(), cancel)
	srv.connections[nodeID.String()] = conn
	defer conn.Stop()

	srv.UpdateNodeOU(nodeID.String(), &ouB)

	// Failed read: neither unit A (stale) nor unit B (the untrusted argument),
	// but unassigned — account-wide viewers only.
	msg := lastMetricsScope(t, srv, ws, nodeID, accountID)
	if msg.ScopeOU != nil || !msg.ScopeOUUnassigned {
		t.Errorf("a failed re-read left the live scope at %v; it must narrow to unassigned rather than keep unit A or trust unit B", msg.ScopeOU)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, nodeID.String()) {
		t.Errorf("a failed scope re-read must be logged as a warning naming the node; got:\n%s", out)
	}

	// The read recovers: a retry publishes the committed unit without any
	// further assignment or reconnect.
	failing.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		msg = lastMetricsScope(t, srv, ws, nodeID, accountID)
		if msg.ScopeOU != nil && *msg.ScopeOU == ouB {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the read recovered the retry never published unit B; live scope is %v", msg.ScopeOU)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Every retry is spent: a row that stays unreadable narrows the scope and stops
// after the bounded number of attempts rather than retrying forever.
func TestScopeReadFailureRetriesAreBounded(t *testing.T) {
	nodeID, accountID, ouA := uuid.New(), uuid.New(), uuid.New()
	var reads atomic.Int32
	srv := testServer(&stubNodeService{lookup: func(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
		reads.Add(1)
		return nil, errors.New("connection reset by peer")
	}})
	srv.logger = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	prevDelay := scopeRefreshRetryDelay
	scopeRefreshRetryDelay = 5 * time.Millisecond
	t.Cleanup(func() { scopeRefreshRetryDelay = prevDelay })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newStreamConnection(ctx, srv.logger, nodeID.String(), accountID.String(), &ouA,
		&scriptedStream{ctx: ctx}, time.Now(), cancel)
	srv.connections[nodeID.String()] = conn
	defer conn.Stop()

	srv.UpdateNodeOU(nodeID.String(), nil)
	time.Sleep(200 * time.Millisecond)
	if got := reads.Load(); got != int32(1+scopeRefreshRetries) {
		t.Fatalf("reads = %d, want the first attempt plus %d retries", got, scopeRefreshRetries)
	}
}
