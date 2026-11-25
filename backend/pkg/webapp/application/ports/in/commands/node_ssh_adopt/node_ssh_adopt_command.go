package node_ssh_adopt

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
)

// SSHAdoptCommand adopts an already-running device over SSH: install the agent,
// register a node, wait for it to connect and (optionally) flash a network-first
// EEPROM. Connection auth is exactly one of Password, AuthSSHKeyID or UseAgent.
//
// The adoption never accepts a host key on first use: it is the request that
// offers SSH and sudo credentials to the device, so it must carry proof of the
// key it expects — HostKeyTicket (from a preflight) or HostKeyFingerprint (out
// of band). One of the two, never neither; the service refuses the omission
// with a typed 400 before any dial.
type SSHAdoptCommand struct {
	// Connection.
	Host         string     `json:"host"`
	Port         int        `json:"port,omitempty"`
	Username     string     `json:"username"`
	Password     string     `json:"password,omitempty"`
	AuthSSHKeyID *uuid.UUID `json:"auth_ssh_key_id,omitempty"` // a connection keypair to auth with
	UseAgent     bool       `json:"use_agent,omitempty"`       // auth via the forwarded ssh-agent (YubiKey)
	SudoPassword string     `json:"sudo_password,omitempty"`
	// HostKeyTicket is the proof a reachable preflight returned: THIS user, in
	// THIS account, saw THIS host key on THIS host:port within the last few
	// minutes. The adoption connects strictly against the key inside it. It
	// is the path the UI takes.
	HostKeyTicket string `json:"host_key_ticket,omitempty"`
	// HostKeyFingerprint pins the device's SSH host key directly (SHA256:…),
	// for an API client that knows the key out of band. A different key fails
	// the handshake before any credential is offered.
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`

	// Node record to create for this device.
	NodeName              string     `json:"node_name"`
	DefaultUsername       string     `json:"default_username"`
	DefaultPassword       string     `json:"default_password"`
	NodeSSHKeyID          *uuid.UUID `json:"node_ssh_key_id,omitempty"` // key provisioned onto the node
	ProvisionNetworkFirst bool       `json:"provision_network_first,omitempty"`

	AuthContext commands.AuthContext `json:"-"`
}

// maxHostKeyTicketBytes bounds the ticket field; a real ticket is well under
// half of this.
const maxHostKeyTicketBytes = 1024

// Validate checks required fields, field sizes, and that exactly one
// connection auth is given. The "one of ticket or fingerprint" rule is only
// half-enforced here (not both); "not neither" is the service's typed refusal,
// so the API answers it with a code and a remedy rather than a bare 400.
func (c SSHAdoptCommand) Validate() error {
	if err := commands.ValidateSSHTarget(c.Host, c.Username); err != nil {
		return err
	}
	if err := commands.ValidateSSHCredentialSizes(c.Password, c.SudoPassword); err != nil {
		return err
	}
	// node_name/default_username/default_password are required only when
	// registering a NEW node; if the device is already registered (matched by
	// hardware serial during the run) adoption reuses it, so they may be blank.
	// The create-new branch of the runner enforces them at that point.
	n := 0
	if c.Password != "" {
		n++
	}
	if c.AuthSSHKeyID != nil && *c.AuthSSHKeyID != uuid.Nil {
		n++
	}
	if c.UseAgent {
		n++
	}
	if n != 1 {
		return fmt.Errorf("provide exactly one of password, auth_ssh_key_id or use_agent")
	}
	if c.HostKeyTicket != "" && c.HostKeyFingerprint != "" {
		return fmt.Errorf("send host_key_ticket or host_key_fingerprint, not both")
	}
	if len(c.HostKeyTicket) > maxHostKeyTicketBytes {
		return fmt.Errorf("host_key_ticket is not one this server issued")
	}
	return commands.ValidateHostKeyFingerprint("host_key_fingerprint", c.HostKeyFingerprint)
}
