package commands

import (
	"fmt"
	"strings"
)

// Field-size bounds shared by the SSH preflight and adopt commands. Each is
// generous for any real value and refuses at the request boundary what would
// otherwise reach a DNS lookup, an audit row or an SSH handshake unbounded.
const (
	// MaxSSHHostBytes is the DNS name length limit (RFC 1035); an IP literal
	// is far shorter.
	MaxSSHHostBytes = 253
	// MaxSSHUsernameBytes is what useradd accepts on Linux (32) with room for
	// other systems' longer names.
	MaxSSHUsernameBytes = 64
	// MaxSSHPasswordBytes bounds the SSH and sudo passwords. sshd itself caps
	// a password at 1024 bytes; a longer one never authenticates anywhere.
	MaxSSHPasswordBytes = 1024
)

// ValidateSSHTarget checks the host and username an SSH command dials: both
// present, neither oversized, neither carrying whitespace a shell or a log
// line would misread.
//
// The bounds apply to the value AS SENT, not to a trimmed copy: the raw value
// is what the job row stores, the allowlist resolves and the audit line
// quotes, so measuring a trimmed copy let any amount of leading or trailing
// whitespace ride past the limits. Padding is refused outright rather than
// trimmed here, because the callers dial the raw string and a silently trimmed
// validation would vouch for a value they never see.
func ValidateSSHTarget(host, username string) error {
	if strings.TrimSpace(host) != host {
		return fmt.Errorf("host must not start or end with whitespace")
	}
	if strings.TrimSpace(username) != username {
		return fmt.Errorf("username must not start or end with whitespace")
	}
	if host == "" {
		return fmt.Errorf("host is required")
	}
	if len(host) > MaxSSHHostBytes {
		return fmt.Errorf("host must be at most %d characters", MaxSSHHostBytes)
	}
	if strings.ContainsAny(host, " \t\r\n/") {
		return fmt.Errorf("host must be a hostname or an IP address")
	}
	if username == "" {
		return fmt.Errorf("username is required")
	}
	if len(username) > MaxSSHUsernameBytes {
		return fmt.Errorf("username must be at most %d characters", MaxSSHUsernameBytes)
	}
	if strings.ContainsAny(username, " \t\r\n") {
		return fmt.Errorf("username must not contain whitespace")
	}
	return nil
}

// ValidateSSHCredentialSizes bounds the two passwords an SSH command may
// carry. Empty is fine (another auth method, or no sudo password).
func ValidateSSHCredentialSizes(password, sudoPassword string) error {
	if len(password) > MaxSSHPasswordBytes {
		return fmt.Errorf("password must be at most %d characters", MaxSSHPasswordBytes)
	}
	if len(sudoPassword) > MaxSSHPasswordBytes {
		return fmt.Errorf("sudo_password must be at most %d characters", MaxSSHPasswordBytes)
	}
	return nil
}
