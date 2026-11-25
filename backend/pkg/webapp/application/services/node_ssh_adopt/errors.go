package node_ssh_adopt

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
)

// ErrSSHAdoptDisabled refuses an adoption and a preflight alike on a server
// where SSH adoption is off. One value for both routes, so the adoption
// answers what the preflight already did: 400 with this text, plus a code a
// client can branch on.
var ErrSSHAdoptDisabled = app_errors.InvalidRequest("ssh_adopt_disabled",
	errors.New("SSH adoption is not enabled on this server"))

// AdoptionCapacityError reports that no SSH adoption slot is free for this
// request. The request is fine and nothing about the target refuses it; the
// caller's next step is to wait, which is what 429 says. Raised BEFORE a job
// row exists, so a refused request leaves no pending job that a queue would
// have parked for an unbounded time. PerAccount says which bound was hit: the
// server-wide one, or this account's share of it (so one tenant cannot hold
// every slot on a shared install).
type AdoptionCapacityError struct {
	Limit      int
	PerAccount bool
}

func (e *AdoptionCapacityError) Error() string {
	if e.PerAccount {
		return fmt.Sprintf("Your account already has %d adoptions running, the most it may run at once. Wait for one to finish and try again.", e.Limit)
	}
	return fmt.Sprintf("All %d SSH adoption slots on this server are busy. Wait for a running adoption to finish and try again.", e.Limit)
}

func (e *AdoptionCapacityError) HTTPStatus() int   { return http.StatusTooManyRequests }
func (e *AdoptionCapacityError) ErrorCode() string { return "adoption_capacity" }

// PreflightCapacityError is the same refusal for connection tests: each one
// holds a DNS lookup, a key decryption and an outbound SSH handshake, so they
// are bounded server-wide and per account, before any of those start.
type PreflightCapacityError struct {
	Limit      int
	PerAccount bool
}

func (e *PreflightCapacityError) Error() string {
	if e.PerAccount {
		return fmt.Sprintf("Your account already has %d connection tests running, the most it may run at once. Wait a moment and try again.", e.Limit)
	}
	return fmt.Sprintf("All %d connection-test slots on this server are busy. Wait a moment and try again.", e.Limit)
}

func (e *PreflightCapacityError) HTTPStatus() int   { return http.StatusTooManyRequests }
func (e *PreflightCapacityError) ErrorCode() string { return "preflight_capacity" }

// HostKeyFingerprintRequiredError reports that this server's host_key_policy is
// "strict" and the PREFLIGHT carried no fingerprint to check the device
// against. The request must change (400): obtain the device's SHA256 host-key
// fingerprint out of band and send it with the request. (An adoption without
// a binding is HostKeyBindingRequiredError, whatever the policy.)
type HostKeyFingerprintRequiredError struct {
	Field string
}

func (e *HostKeyFingerprintRequiredError) Error() string {
	return fmt.Sprintf("This server verifies SSH host keys strictly: send the device's SHA256 host key fingerprint as %s. Read it on the device with ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub.", e.Field)
}

func (e *HostKeyFingerprintRequiredError) HTTPStatus() int   { return http.StatusBadRequest }
func (e *HostKeyFingerprintRequiredError) ErrorCode() string { return "host_key_fingerprint_required" }

// HostKeyBindingRequiredError reports that an adoption carried no usable proof
// of the device's host key: neither a host_key_ticket that verifies for this
// request nor a host_key_fingerprint. An adoption offers SSH and sudo
// credentials to whatever answers on host:port, so it never takes a key on
// first use. The request must change (400): run the connection test again
// and adopt with the ticket it returns. Reason, when set, names what was
// wrong with the ticket that WAS sent.
type HostKeyBindingRequiredError struct {
	Reason string
}

func (e *HostKeyBindingRequiredError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("Run the connection test again, then adopt. The host key ticket was not accepted: %s.", e.Reason)
	}
	return "Run the connection test again, then adopt. The adoption needs proof of the device's SSH host key: the ticket from a recent connection test, or the device's SHA256 host key fingerprint sent as host_key_fingerprint."
}

func (e *HostKeyBindingRequiredError) HTTPStatus() int   { return http.StatusBadRequest }
func (e *HostKeyBindingRequiredError) ErrorCode() string { return "host_key_binding_required" }

var (
	_ results.OperatorFacingError = (*AdoptionCapacityError)(nil)
	_ results.OperatorFacingError = (*PreflightCapacityError)(nil)
	_ results.OperatorFacingError = (*HostKeyFingerprintRequiredError)(nil)
	_ results.OperatorFacingError = (*HostKeyBindingRequiredError)(nil)
)
