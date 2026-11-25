package grpc

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// The ids a node presents are peer-supplied STRINGS, and uuid.Parse accepts five
// spellings of one UUID. Everything downstream keys on them: the per-node stream
// cap, the registry (and therefore the displacement of a previous stream), and
// the dispatch lookup — which uses the canonical form. Keying on the raw string
// meant an upper-cased id was a different node to every one of them, so
// AGENT_MAX_STREAMS_PER_NODE never bound and a second stream displaced nothing.
func TestHandshakeKeysOnTheCanonicalNodeID(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)

	// A stream already registered under the CANONICAL id — what an honest agent
	// leaves behind, and what dispatch looks up.
	oldCtx, oldCancel := context.WithCancel(context.Background())
	defer oldCancel()
	existing := newStreamConnection(oldCtx, srv.logger, nodeID.String(), accountID.String(), nil,
		&scriptedStream{ctx: oldCtx}, time.Now(), oldCancel)
	srv.connections[nodeID.String()] = existing

	// The same node reconnects, spelling its id in upper case.
	init := connectionInit(nodeID, accountID)
	init.NodeId = strings.ToUpper(init.NodeId)
	init.AccountId = strings.ToUpper(init.AccountId)
	_ = srv.EstablishStream(&scriptedStream{
		ctx:      context.Background(),
		incoming: []*proto.NodeToPlatform{init},
	})

	// Keyed on the raw string, the new stream was a DIFFERENT node: it took its
	// own registry slot and its own stream-cap slot, and displaced nothing. The
	// old connection surviving intact is what that looks like from here.
	if !existing.waitForWriter(2 * time.Second) {
		t.Error("an upper-cased id did not displace the canonical entry — the registry holds two streams for one node")
	}
	select {
	case <-oldCtx.Done():
	default:
		t.Error("the displaced connection was never cancelled, so a second spelling accumulates instead of replacing")
	}
}

// The monitor works from a snapshot and does per-node database work between
// taking it and acting on it, so a node can reconnect in that window. The
// cleanup must then leave the registry alone: deleting by key would remove the
// NEW connection, whose handler is never cancelled and keeps heartbeating — so
// last_seen_at stays fresh, the database-side sweep never revisits it, and
// IsNodeConnected is false forever while the stream is alive.
func TestMonitorCleanupDoesNotEvictAReconnectedStream(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := NewServer(&config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), svc)

	staleCtx, staleCancel := context.WithCancel(context.Background())
	defer staleCancel()
	stale := newStreamConnection(staleCtx, srv.logger, nodeID.String(), accountID.String(), nil,
		&scriptedStream{ctx: staleCtx}, time.Now().Add(-10*time.Minute), staleCancel)

	// The node has already reconnected: the registry holds the NEW stream.
	freshCtx, freshCancel := context.WithCancel(context.Background())
	defer freshCancel()
	fresh := newStreamConnection(freshCtx, srv.logger, nodeID.String(), accountID.String(), nil,
		&scriptedStream{ctx: freshCtx}, time.Now(), freshCancel)
	srv.mu.Lock()
	srv.connections[nodeID.String()] = fresh
	srv.mu.Unlock()

	// The monitor now reaches the node in its stale snapshot.
	srv.monitor.mu.RLock()
	callback := srv.monitor.cleanupCallback
	srv.monitor.mu.RUnlock()
	swept := callback(nodeID.String(), stale)

	if swept {
		t.Error("the callback reported it swept the registered connection; the monitor will now mark a live node offline")
	}
	srv.mu.RLock()
	current, ok := srv.connections[nodeID.String()]
	srv.mu.RUnlock()
	if !ok || current != fresh {
		t.Error("the reconnected stream was evicted from the registry by a sweep of its predecessor")
	}
}

// ...and when it IS the registered connection, the sweep must still remove it,
// or a dead entry survives every sweep.
func TestMonitorCleanupEvictsTheConnectionItSwept(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := NewServer(&config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newStreamConnection(ctx, srv.logger, nodeID.String(), accountID.String(), nil,
		&scriptedStream{ctx: ctx}, time.Now().Add(-10*time.Minute), cancel)
	srv.mu.Lock()
	srv.connections[nodeID.String()] = conn
	srv.mu.Unlock()

	srv.monitor.mu.RLock()
	callback := srv.monitor.cleanupCallback
	srv.monitor.mu.RUnlock()
	if !callback(nodeID.String(), conn) {
		t.Error("the callback did not report sweeping its own connection, so the node is never marked offline")
	}
	srv.mu.RLock()
	_, ok := srv.connections[nodeID.String()]
	srv.mu.RUnlock()
	if ok {
		t.Error("the stale connection is still registered")
	}
}
