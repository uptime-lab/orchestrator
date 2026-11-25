package grpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// capturingWebsocketClient records what the server fans out to the frontend.
type capturingWebsocketClient struct {
	sent []websockets3.Message
}

func (c *capturingWebsocketClient) Send(msg websockets3.Message) error {
	c.sent = append(c.sent, msg)
	return nil
}

func (c *capturingWebsocketClient) GetChannel() chan websockets3.Message { return nil }

// lastMetricsScope pushes one metrics sample and returns the scope it was sent
// with.
func lastMetricsScope(t *testing.T, srv *Server, ws *capturingWebsocketClient, nodeID, accountID uuid.UUID) websockets3.Message {
	t.Helper()
	before := len(ws.sent)
	srv.recordMetrics(nodeID.String(), accountID.String(), &proto.MetricsUpdate{})
	if len(ws.sent) != before+1 {
		t.Fatalf("recordMetrics sent %d messages, want 1", len(ws.sent)-before)
	}
	return ws.sent[len(ws.sent)-1]
}

// committedRow is the node row as the database holds it, for the scope-refresh
// tests: UpdateNodeOU re-reads the committed unit rather than trusting its
// argument, so a stub that answers with this is what "the last COMMITTED value"
// means in those tests.
type committedRow struct {
	mu   sync.Mutex
	node *node.Node
}

func (c *committedRow) set(n *node.Node) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.node = n
}

func (c *committedRow) lookup(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.node == nil {
		return nil, app_errors.ErrNodeNotFound
	}
	n := *c.node
	return &n, nil
}

// committedNode is a row for nodeID in accountID whose unit is ouID.
func committedNode(nodeID, accountID uuid.UUID, ouID *uuid.UUID) *node.Node {
	return &node.Node{ID: nodeID, AccountID: accountID, OUID: ouID}
}

// The metrics push scopes itself by the OU captured at registration, and that
// copy had no other writer: a node moved from unit A to unit B while its stream
// stayed up kept feeding A's viewers and starved B's until it reconnected.
// The assignment path now repoints the live connection.
func TestReassigningAConnectedNodeRescopesItsMetrics(t *testing.T) {
	nodeID, accountID, ouA, ouB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	row := &committedRow{node: committedNode(nodeID, accountID, &ouA)}
	srv := testServer(&stubNodeService{lookup: row.lookup})
	ws := &capturingWebsocketClient{}
	srv.websocketClient = ws

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newStreamConnection(ctx, srv.logger, nodeID.String(), accountID.String(), &ouA,
		&scriptedStream{ctx: ctx}, time.Now(), cancel)
	srv.connections[nodeID.String()] = conn
	defer conn.Stop()

	if msg := lastMetricsScope(t, srv, ws, nodeID, accountID); msg.ScopeOU == nil || *msg.ScopeOU != ouA {
		t.Fatalf("before the move the sample is scoped to %v, want unit A %s", msg.ScopeOU, ouA)
	}

	// The node moves to unit B without reconnecting.
	row.set(committedNode(nodeID, accountID, &ouB))
	srv.UpdateNodeOU(nodeID.String(), &ouB)
	msg := lastMetricsScope(t, srv, ws, nodeID, accountID)
	if msg.ScopeOU == nil || *msg.ScopeOU != ouB {
		t.Errorf("after the move the sample is still scoped to %v, want unit B %s — unit A's viewers keep the feed and B's never get it", msg.ScopeOU, ouB)
	}
	if msg.ScopeOUUnassigned {
		t.Error("a node in unit B was pushed as unassigned")
	}

	// And out of any unit: the sample must now reach only the callers who may
	// see unassigned nodes.
	row.set(committedNode(nodeID, accountID, nil))
	srv.UpdateNodeOU(nodeID.String(), nil)
	msg = lastMetricsScope(t, srv, ws, nodeID, accountID)
	if msg.ScopeOU != nil || !msg.ScopeOUUnassigned {
		t.Errorf("after clearing the unit the sample is scoped to %v (unassigned=%v), want unassigned", msg.ScopeOU, msg.ScopeOUUnassigned)
	}
}

// The registry keys on the canonical spelling of the id; the update must find
// the connection however the caller spelled it, and a node with no live stream
// is a no-op rather than a phantom entry.
func TestUpdateNodeOUCanonicalisesAndIgnoresUnknownNodes(t *testing.T) {
	nodeID, accountID, ouA, ouB := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	row := &committedRow{node: committedNode(nodeID, accountID, &ouB)}
	srv := testServer(&stubNodeService{lookup: row.lookup})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newStreamConnection(ctx, srv.logger, nodeID.String(), accountID.String(), &ouA,
		&scriptedStream{ctx: ctx}, time.Now(), cancel)
	srv.connections[nodeID.String()] = conn
	defer conn.Stop()

	srv.UpdateNodeOU("{"+nodeID.String()+"}", &ouB)
	if conn.OUID == nil || *conn.OUID != ouB {
		t.Errorf("a braced spelling of the id did not reach the canonical registry entry: OUID=%v", conn.OUID)
	}

	srv.UpdateNodeOU(uuid.NewString(), &ouA)
	if len(srv.connections) != 1 {
		t.Errorf("updating an unknown node changed the registry: %d entries, want 1", len(srv.connections))
	}
	if *conn.OUID != ouB {
		t.Errorf("updating an unknown node touched another node's scope: %v", conn.OUID)
	}
}
