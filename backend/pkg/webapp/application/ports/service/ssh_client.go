package service

import (
	"context"
	"time"
)

// SSHConnParams describes an outbound SSH connection to a target device. Exactly
// one auth method is used: Password OR PrivateKeyPEM (a connection key). Secrets
// here must never be logged.
type SSHConnParams struct {
	Host     string
	Port     int
	Username string
	// DialAddr, when set, is the IP literal the TCP connection actually goes
	// to. The adoption allowlist resolves Host once, vets every address and
	// hands the vetted one here; dialing Host again would let a second DNS
	// answer (rebinding) point the connection outside the allowed subnets.
	// Host stays the name for logs, errors and the job record.
	DialAddr string

	// Auth (exactly one): Password for password auth; PrivateKeyPEM (+ optional
	// Passphrase) for key auth; or UseAgent to authenticate via a (forwarded)
	// ssh-agent — the operator's YubiKey stays on their machine and signs live.
	Password      string
	PrivateKeyPEM string
	Passphrase    string
	UseAgent      bool
	// AgentSock is the ssh-agent socket path; empty falls back to $SSH_AUTH_SOCK.
	AgentSock string

	// HostKeyPolicy is "tofu" (accept + return the fingerprint on first contact)
	// or "strict" (require KnownHostKeyFingerprint to match). Never a silent ignore.
	// A strict mismatch fails the handshake at the host-key callback, which the
	// SSH protocol runs BEFORE authentication, so no credential is offered to a
	// host presenting the wrong key.
	HostKeyPolicy           string
	KnownHostKeyFingerprint string

	ConnectTimeout time.Duration
}

// SSHRunResult is the outcome of a single remote command.
type SSHRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// Truncated reports that stdout or stderr exceeded the adapter's per-stream
	// cap and the excess was DISCARDED. Legitimate adoption output is tiny (a
	// uname, a whoami, a dpkg log); an unbounded buffer let a hostile device
	// grow the backend's memory without limit. Discarded rather than failed:
	// a writer that errors would surface as a transport error and misreport a
	// noisy device as an unreachable one.
	Truncated bool
}

// HostKeyMismatchError is returned by Dial when the strict policy refused the
// host key. Both fingerprints ride on it so a caller can report "the key
// changed from X to Y" without a second connection.
type HostKeyMismatchError struct {
	Got  string
	Want string
}

func (e *HostKeyMismatchError) Error() string {
	return "host key mismatch: got " + e.Got + ", expected " + e.Want
}

// SSHSession is a live connection to a device. Callers must Close it.
type SSHSession interface {
	// Run executes a command and returns its output + exit code. A non-zero exit
	// is NOT an error (err is reserved for transport failures).
	Run(ctx context.Context, cmd string) (SSHRunResult, error)
	// RunSudo runs a command with root privileges. An empty sudoPassword uses
	// passwordless sudo (`sudo -n`); otherwise the password is fed on stdin
	// (`sudo -S`) so it never appears in the device's process list.
	RunSudo(ctx context.Context, cmd string, sudoPassword string) (SSHRunResult, error)
	// WriteFile writes content to path on the device as root (used by the adopt
	// runner to drop the adoption file); mode is the target file mode.
	WriteFile(ctx context.Context, path string, content []byte, mode uint32, sudoPassword string) error
	// HostKeyFingerprint returns the SHA256 fingerprint of the server host key
	// observed at connection time (for TOFU pinning / display).
	HostKeyFingerprint() string
	Close() error
}

// SSHClient dials outbound SSH connections. The only implementation lives in
// infrastructure/ssh; this port keeps the application layer transport-agnostic.
type SSHClient interface {
	Dial(ctx context.Context, params SSHConnParams) (SSHSession, error)
}
