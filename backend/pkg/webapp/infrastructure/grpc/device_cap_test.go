package grpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// A device refused at the cap must hear something it can act on differently
// from "connection failed": the fixed ResourceExhausted message, through the
// wrap the service puts around it.
func TestDeviceCapRefusalIsResourceExhaustedWithTheFixedMessage(t *testing.T) {
	err := fmt.Errorf("failed to create node: %w", &entitlement.DeviceCapError{Cap: 25, Count: 25})
	st, ok := deviceCapStatus(err)
	if !ok {
		t.Fatal("the cap refusal was not recognised through the wrap")
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("code = %s, want ResourceExhausted", st.Code())
	}
	if st.Message() != entitlement.GRPCRefusalMessage {
		t.Errorf("message = %q, want the fixed %q", st.Message(), entitlement.GRPCRefusalMessage)
	}
}

// The node-token first connect is an admission too: refused at the cap, the
// device hears the same fixed ResourceExhausted the master-token arm sends,
// not the uniform "connection failed", and no stream is registered.
func TestNodeTokenFirstConnectAtCapIsResourceExhausted(t *testing.T) {
	nodeID, accountID := uuid.New(), uuid.New()
	svc := &stubNodeService{connectErr: &entitlement.DeviceCapError{Cap: 25, Count: 25}}
	srv := testServer(svc)

	// The refusal is parked until the stream's context ends.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	stream := &scriptedStream{ctx: ctx, incoming: []*proto.NodeToPlatform{connectionInit(nodeID, accountID)}}

	err := srv.EstablishStream(stream)

	st, _ := status.FromError(err)
	if st.Code() != codes.ResourceExhausted || st.Message() != entitlement.GRPCRefusalMessage {
		t.Fatalf("refusal = %v, want ResourceExhausted %q", err, entitlement.GRPCRefusalMessage)
	}
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	if len(srv.connections) != 0 {
		t.Errorf("a refused first connect registered %d stream(s)", len(srv.connections))
	}
}

// The master-token arm answers the cap refusal without an ERROR line of its
// own: the device asks again on its own, and the service records the refusal
// once per device per hour. An unthrottled ERROR per handshake was the
// flood.
func TestMasterTokenCapRefusalIsNotAnErrorLine(t *testing.T) {
	accountID := uuid.New()
	svc := &stubNodeService{masterErr: fmt.Errorf("failed to create node: %w", &entitlement.DeviceCapError{Cap: 25, Count: 25})}
	srv := testServer(svc)
	srv.SetMasterTokenService(&stubMasterTokenService{accountID: accountID})
	var logs bytes.Buffer
	srv.logger = slog.New(slog.NewTextHandler(&logs, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := srv.EstablishStream(&scriptedStream{ctx: ctx, incoming: []*proto.NodeToPlatform{masterTokenInit(accountID)}})

	if st, _ := status.FromError(err); st.Code() != codes.ResourceExhausted {
		t.Fatalf("refusal = %v, want ResourceExhausted", err)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("the cap refusal wrote an ERROR line:\n%s", logs.String())
	}
}

// Every other refusal keeps the uniform answer: nothing here must turn a
// not-found or a serial conflict into a distinguishable status.
func TestOtherRefusalsAreNotTheDeviceCap(t *testing.T) {
	for _, err := range []error{app_errors.ErrNodeNotFound, app_errors.ErrHardwareSerialConflict, errors.New("database is unhappy")} {
		if _, ok := deviceCapStatus(err); ok {
			t.Errorf("%v was mapped as a device cap refusal", err)
		}
	}
}

// A cap that could not be checked is answered with the cap's code and its
// own fixed message. The message must differ from the cap-reached one: the
// agent holds for its long retry only on that exact message, and retries
// every other ResourceExhausted through its reconnect backoff — which is the
// answer to a refusal that clears when the database does.
func TestUnverifiedCapIsResourceExhaustedWithItsOwnMessage(t *testing.T) {
	err := fmt.Errorf("failed to create node: %w", &entitlement.DeviceCapUnverifiedError{Cap: 25, Cause: errors.New("lock timeout")})
	st, ok := deviceCapStatus(err)
	if !ok {
		t.Fatal("the unverified refusal was not recognised through the wrap")
	}
	if st.Code() != codes.ResourceExhausted || st.Message() != entitlement.GRPCUnverifiedMessage {
		t.Errorf("status = %s %q, want ResourceExhausted %q", st.Code(), st.Message(), entitlement.GRPCUnverifiedMessage)
	}
	if entitlement.GRPCUnverifiedMessage == entitlement.GRPCRefusalMessage {
		t.Error("the unverified message equals the cap-reached one; the agent would hold for its long retry")
	}
	if strings.Contains(st.Message(), "lock timeout") {
		t.Error("the database's error reached the device")
	}
}

// Both arms that admit over gRPC — the node-token first connect and the
// master-token handshake — answer the unverified refusal with that status,
// never the Unauthenticated a device reads as a credential it lacks, and
// register no stream.
func TestUnverifiedCapOnBothConnectArms(t *testing.T) {
	refusal := &entitlement.DeviceCapUnverifiedError{Cap: 25, Cause: errors.New("statement timeout")}
	arms := []struct {
		name string
		run  func(ctx context.Context) (*Server, error)
	}{
		{"node token", func(ctx context.Context) (*Server, error) {
			srv := testServer(&stubNodeService{connectErr: refusal})
			err := srv.EstablishStream(&scriptedStream{ctx: ctx,
				incoming: []*proto.NodeToPlatform{connectionInit(uuid.New(), uuid.New())}})
			return srv, err
		}},
		{"master token", func(ctx context.Context) (*Server, error) {
			accountID := uuid.New()
			srv := testServer(&stubNodeService{masterErr: fmt.Errorf("failed to create node: %w", refusal)})
			srv.SetMasterTokenService(&stubMasterTokenService{accountID: accountID})
			err := srv.EstablishStream(&scriptedStream{ctx: ctx,
				incoming: []*proto.NodeToPlatform{masterTokenInit(accountID)}})
			return srv, err
		}},
	}
	for _, arm := range arms {
		t.Run(arm.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			srv, err := arm.run(ctx)
			st, _ := status.FromError(err)
			if st.Code() != codes.ResourceExhausted || st.Message() != entitlement.GRPCUnverifiedMessage {
				t.Fatalf("refusal = %v, want ResourceExhausted %q", err, entitlement.GRPCUnverifiedMessage)
			}
			srv.mu.RLock()
			defer srv.mu.RUnlock()
			if len(srv.connections) != 0 {
				t.Errorf("a refused connect registered %d stream(s)", len(srv.connections))
			}
		})
	}
}

// A device refused at its account's own limit hears its own fixed message,
// not the instance cap's and not the transient one.
func TestAccountDeviceLimitRefusalIsResourceExhaustedWithItsOwnMessage(t *testing.T) {
	err := fmt.Errorf("failed to create node: %w", &entitlement.AccountDeviceLimitError{Limit: 5, Count: 5})
	st, ok := deviceCapStatus(err)
	if !ok || st.Code() != codes.ResourceExhausted || st.Message() != entitlement.GRPCAccountLimitMessage {
		t.Fatalf("status = %v (recognised %t), want ResourceExhausted %q", st, ok, entitlement.GRPCAccountLimitMessage)
	}
	if entitlement.GRPCAccountLimitMessage == entitlement.GRPCRefusalMessage ||
		entitlement.GRPCAccountLimitMessage == entitlement.GRPCUnverifiedMessage {
		t.Error("the account-limit message is not distinct")
	}
}
