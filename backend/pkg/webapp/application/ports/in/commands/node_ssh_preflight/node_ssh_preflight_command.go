package node_ssh_preflight

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// SSHPreflightCommand is a NON-DESTRUCTIVE connectivity/capability probe against
// a target device before adoption: it dials SSH, captures the host-key
// fingerprint, and checks arch/sudo/CDN reachability without writing anything.
// Exactly one auth method is supplied (Password XOR SSHKeyID XOR UseAgent).
//
// The preflight is also where host-key TRUST is established: a reachable probe
// answers with a host_key_ticket that the adoption must present, so the adopt
// call (the one that offers credentials) never accepts a key on first use.
type SSHPreflightCommand struct {
	Host         string     `json:"host"`
	Port         int        `json:"port,omitempty"`
	Username     string     `json:"username"`
	Password     string     `json:"password,omitempty"`
	SSHKeyID     *uuid.UUID `json:"ssh_key_id,omitempty"` // a connection keypair to auth with
	UseAgent     bool       `json:"use_agent,omitempty"`  // auth via the forwarded ssh-agent (YubiKey)
	SudoPassword string     `json:"sudo_password,omitempty"`
	// ExpectedHostKeyFingerprint is the fingerprint an earlier preflight saw
	// (SHA256:…). When present the probe connects strictly and a different key
	// is reported as host_key_changed with both fingerprints, before any
	// credential is offered. A server whose host_key_policy is "strict"
	// refuses a probe without one.
	ExpectedHostKeyFingerprint string `json:"expected_host_key_fingerprint,omitempty"`
	// AcceptNewHostKey, together with ExpectedHostKeyFingerprint, lets THIS
	// probe accept a key that differs from the expected one: the operator has
	// seen the host_key_changed report and says the device was reinstalled.
	// The reset is recorded in the audit trail with both fingerprints. Without
	// the flag a different key stays a refusal; without an expectation there
	// is nothing to reset, so the flag alone is a validation error.
	AcceptNewHostKey bool `json:"accept_new_host_key,omitempty"`

	AuthContext commands.AuthContext `json:"-"` // Set from context, not from body
}

// Validate checks required fields, field sizes, and that exactly one auth
// method is provided.
func (c SSHPreflightCommand) Validate() error {
	if err := commands.ValidateSSHTarget(c.Host, c.Username); err != nil {
		return err
	}
	if err := commands.ValidateSSHCredentialSizes(c.Password, c.SudoPassword); err != nil {
		return err
	}
	n := 0
	if c.Password != "" {
		n++
	}
	if c.SSHKeyID != nil && *c.SSHKeyID != uuid.Nil {
		n++
	}
	if c.UseAgent {
		n++
	}
	if n != 1 {
		return fmt.Errorf("provide exactly one of password, ssh_key_id or use_agent")
	}
	if c.AcceptNewHostKey && strings.TrimSpace(c.ExpectedHostKeyFingerprint) == "" {
		return fmt.Errorf("accept_new_host_key needs expected_host_key_fingerprint: the key being replaced")
	}
	return commands.ValidateHostKeyFingerprint("expected_host_key_fingerprint", c.ExpectedHostKeyFingerprint)
}
