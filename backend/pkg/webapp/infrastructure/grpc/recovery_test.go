package grpc

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The attack this file exists for: a ConnectionInit with no fingerprint.
//
// `fingerprint` is an optional proto3 message, so omitting it is legal wire
// format and unmarshals to nil. The handler runs before validating any token —
// i.e. before authentication — and a panic in a gRPC stream handler is not
// contained by grpc-go, so a dereference there would let one such message from
// anyone able to reach :50051 take down the HTTP API, every agent stream and
// every running workflow in the same process.

func TestConnectionInitWithoutFingerprintDoesNotPanic(t *testing.T) {
	// The nil fingerprint must be reachable through the generated getters
	// without panicking — this is the shape the handler now uses.
	init := &proto.ConnectionInit{AgentVersion: "0.4.1"}

	if fp := init.GetFingerprint(); fp != nil {
		t.Fatalf("expected a nil fingerprint, got %+v", fp)
	}
	// GetMacAddress/GetHardwareSerial on the nil message must be safe; a direct
	// field access here is what crashed the process.
	var fp *proto.HardwareFingerprint
	if mac := fp.GetMacAddress(); mac != "" {
		t.Fatalf("nil fingerprint must yield an empty MAC, got %q", mac)
	}
	if serial := fp.GetHardwareSerial(); serial != "" {
		t.Fatalf("nil fingerprint must yield an empty serial, got %q", serial)
	}
}

func TestStreamInterceptorContainsAPanic(t *testing.T) {
	s := &Server{logger: slog.Default()}

	err := s.recoveryStreamInterceptor(
		nil, nil,
		&grpc.StreamServerInfo{FullMethod: "/orchestrator.NodeCommunication/EstablishStream"},
		func(interface{}, grpc.ServerStream) error {
			var fp *proto.HardwareFingerprint
			// Exactly the old dereference, on exactly the old value.
			_ = fp.MacAddress //nolint:staticcheck // deliberate nil deref
			return nil
		},
	)

	if err == nil {
		t.Fatal("a panicking handler must yield an error, not a crashed process")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.Internal {
		t.Fatalf("want codes.Internal, got %v", err)
	}
	// The caller must not receive the panic text or a stack trace.
	if msg := status.Convert(err).Message(); msg != "internal error" {
		t.Fatalf("panic detail leaked to the caller: %q", msg)
	}
}

func TestStreamInterceptorPassesThroughNormalErrors(t *testing.T) {
	s := &Server{logger: slog.Default()}
	sentinel := errors.New("ordinary failure")

	err := s.recoveryStreamInterceptor(
		nil, nil,
		&grpc.StreamServerInfo{FullMethod: "/x/y"},
		func(interface{}, grpc.ServerStream) error { return sentinel },
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("a normal error must reach the caller unchanged, got %v", err)
	}
}

func TestUnaryInterceptorContainsAPanic(t *testing.T) {
	s := &Server{logger: slog.Default()}

	resp, err := s.recoveryUnaryInterceptor(
		context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/x/y"},
		func(context.Context, interface{}) (interface{}, error) { panic("boom") },
	)
	if resp != nil {
		t.Fatalf("a contained panic must not return a response, got %+v", resp)
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.Internal {
		t.Fatalf("want codes.Internal, got %v", err)
	}
}
