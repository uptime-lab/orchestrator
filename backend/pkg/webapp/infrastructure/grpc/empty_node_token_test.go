package grpc

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// An empty node token is refused at the gRPC boundary, before the service is
// asked: it is never a node's credential.
func TestNodeTokenArmRefusesAnEmptyToken(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connected: &node.Node{ID: nodeID, AccountID: accountID}}
	srv := testServer(svc)

	init := connectionInit(nodeID, accountID)
	init.UniqueToken = ""
	stream := &scriptedStream{ctx: context.Background(), incoming: []*proto.NodeToPlatform{init}}

	err := srv.EstablishStream(stream)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
	if svc.connects != 0 {
		t.Errorf("the service was asked to authenticate an empty token %d time(s)", svc.connects)
	}
}
