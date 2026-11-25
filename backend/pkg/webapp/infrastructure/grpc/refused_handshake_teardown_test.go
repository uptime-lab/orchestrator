package grpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// syncBuffer is a log sink safe for the stream's own goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A refused handshake never registered a node, so its end is not a teardown:
// no "stale stream teardown" line naming the node id the peer claimed (which
// may belong to no node at all), and nothing done to the registry.
func TestARefusedHandshakeHasNoTeardown(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	logs := &syncBuffer{}
	srv := testServer(&stubNodeService{connectErr: errors.New("invalid node token")})
	srv.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	stream := &scriptedStream{ctx: context.Background(), incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)}}
	if err := srv.EstablishStream(stream); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}

	if strings.Contains(logs.String(), "stream teardown") {
		t.Fatalf("a refused handshake was logged as a stream teardown:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), nodeID.String()) && strings.Contains(logs.String(), "reconnected") {
		t.Fatalf("a refused handshake claims the node reconnected:\n%s", logs.String())
	}
}

// The other half: a stream that did register its node still tears down when
// it ends, removing its registry entry.
func TestAnAdmittedStreamStillTearsDown(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	logs := &syncBuffer{}
	srv := testServer(&stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}})
	srv.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	stream := &scriptedStream{ctx: context.Background(), incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)}}
	_ = srv.EstablishStream(stream)

	if !strings.Contains(logs.String(), "connection removed from registry") {
		t.Fatalf("an admitted stream ended without its teardown:\n%s", logs.String())
	}
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	if len(srv.connections) != 0 {
		t.Fatalf("%d connection(s) still registered after the stream ended", len(srv.connections))
	}
}
