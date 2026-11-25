package grpc

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

// deviceCapStatus maps the device entitlement's admission refusals to the
// status a device receives. Every other refusal on the master-token path
// stays Unauthenticated, so a refused device learns nothing it could not
// learn from the card.
//
//   - At the cap: ResourceExhausted with the FIXED GRPCRefusalMessage, so the
//     agent can tell "this install will not admit you" from the per-node
//     stream cap, which shares the code and which a device is right to retry.
//     The agent matches that message exactly and holds for its long retry.
//   - At the account's own device limit: ResourceExhausted with the FIXED
//     GRPCAccountLimitMessage. Its own message, so a device log says which
//     limit holds it; the agent holds for its long retry on either message.
//   - The cap could not be checked: ResourceExhausted with the fixed
//     GRPCUnverifiedMessage. The agent classifies a ResourceExhausted whose
//     message is not the cap's as recoverable and reconnects through its
//     ordinary backoff, keeping its identity and its process — the answer to
//     a refusal that clears when the database does. Never Unauthenticated: a
//     device must not read a database fault as a credential it lacks.
func deviceCapStatus(err error) (*status.Status, bool) {
	if errors.As(err, new(*entitlement.DeviceCapUnverifiedError)) {
		return status.New(codes.ResourceExhausted, entitlement.GRPCUnverifiedMessage), true
	}
	if errors.As(err, new(*entitlement.AccountDeviceLimitError)) {
		return status.New(codes.ResourceExhausted, entitlement.GRPCAccountLimitMessage), true
	}
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		return nil, false
	}
	return status.New(codes.ResourceExhausted, entitlement.GRPCRefusalMessage), true
}
