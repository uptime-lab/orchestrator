package grpc

import (
	"context"
	"fmt"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Panic containment for the gRPC surface.
//
// This process serves BOTH the agent gRPC endpoint and the whole HTTP API, and
// grpc-go installs no panic recovery of its own. Without this, any panic in
// any handler — reachable by anyone who can open a stream to :50051, before
// authentication — would not fail one request: it would kill the process,
// dropping the API, the SPA's backend, every other node's stream and every
// in-flight workflow execution. With restart:always that presents as a restart
// loop rather than an incident, and the caller can simply send the message
// again.
//
// The HTTP side has had echo's middleware.Recover() from the start. This is the
// same guarantee for the side that is exposed to unauthenticated peers.
//
// The panic is logged with its stack and returned as codes.Internal, and
// nothing about it reaches the caller — a stack trace names paths, versions and
// sometimes values.

func (s *Server) recoveryStreamInterceptor(
	srv interface{},
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic in gRPC stream handler — contained",
				"method", info.FullMethod,
				"panic", fmt.Sprint(r),
				"stack", string(debug.Stack()),
			)
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(srv, ss)
}

func (s *Server) recoveryUnaryInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (resp interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic in gRPC unary handler — contained",
				"method", info.FullMethod,
				"panic", fmt.Sprint(r),
				"stack", string(debug.Stack()),
			)
			resp = nil
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}
