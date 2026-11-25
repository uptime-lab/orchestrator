package results

import "time"

// SSHPreflightResult reports what a non-destructive SSH probe found on a target
// device. It is advisory: the UI uses it to warn before the operator commits to
// adoption. No device state is changed to produce it.
type SSHPreflightResult struct {
	Reachable          bool   `json:"reachable"`            // TCP+SSH auth succeeded
	Error              string `json:"error,omitempty"`      // why unreachable (if not)
	HostKeyFingerprint string `json:"host_key_fingerprint"` // SHA256, for display and as the expectation of the next probe
	// HostKeyChanged is set when the request carried an expected fingerprint
	// and the device presented a different key. The handshake was refused
	// before authentication, so no credential reached the device and nothing
	// else below was probed; ExpectedHostKeyFingerprint is what the caller
	// pinned and HostKeyFingerprint what the device presented.
	HostKeyChanged             bool   `json:"host_key_changed,omitempty"`
	ExpectedHostKeyFingerprint string `json:"expected_host_key_fingerprint,omitempty"`
	// FirstContact is set when this probe carried no expectation and took the
	// key on first use. The UI says so out loud: trust in a key nobody has
	// checked is a different thing from a key that matched an earlier test.
	FirstContact bool `json:"first_contact,omitempty"`
	// HostKeyReset is set when the request asked to accept a new key
	// (accept_new_host_key) and the device did present one that differs from
	// the expectation. The replaced key is ExpectedHostKeyFingerprint.
	HostKeyReset bool `json:"host_key_reset,omitempty"`
	// HostKeyTicket is the server's proof that this caller saw
	// HostKeyFingerprint on this host:port just now. The adoption presents it
	// as host_key_ticket and connects strictly against the key inside it. It
	// is issued only for a reachable probe and expires at
	// HostKeyTicketExpiresAt (a backend restart also invalidates it; re-run
	// the preflight).
	HostKeyTicket          string     `json:"host_key_ticket,omitempty"`
	HostKeyTicketExpiresAt *time.Time `json:"host_key_ticket_expires_at,omitempty"`
	User                   string     `json:"user"`           // whoami
	Arch                   string     `json:"arch"`           // uname -m
	ArchSupported          bool       `json:"arch_supported"` // arm64/aarch64 (agent .deb target)
	// SudoMode: "root" (already root), "nopasswd" (passwordless sudo),
	// "password" (sudo works with the supplied password), "none" (no sudo).
	SudoMode     string `json:"sudo_mode"`
	CDNReachable bool   `json:"cdn_reachable"`   // device can fetch the agent package
	CDNChecked   bool   `json:"cdn_checked"`     // whether a CDN base was configured to test
	Notes        string `json:"notes,omitempty"` // human-readable extra detail
	// Existing-node match by hardware serial: set when this device is already
	// registered (an accidental re-adopt). Adoption reuses it instead of creating
	// a duplicate; the UI surfaces this as a warning.
	ExistingNodeID     string `json:"existing_node_id,omitempty"`
	ExistingNodeName   string `json:"existing_node_name,omitempty"`
	ExistingNodeStatus string `json:"existing_node_status,omitempty"`
}
