// Package ssh is the outbound SSH adapter used by the device-adopt flow. It is
// the ONLY place golang.org/x/crypto/ssh is imported; the application layer sees
// only the service.SSHClient port. Credentials passed through here must never be
// logged.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	defaultConnectTimeout = 15 * time.Second
	// maxOutputBytes caps what one remote command may leave in stdout and in
	// stderr, each. The adoption flow's commands produce a few lines; the
	// largest legitimate output is a dpkg log. Beyond the cap the bytes are
	// DISCARDED and SSHRunResult.Truncated is set — never an error, which
	// would surface as a transport failure and misreport the device.
	maxOutputBytes = 1 << 20
)

// minHandshakeTimeout is the floor on the handshake+auth bound. A forwarded
// agent (YubiKey / GPG smartcard) can wait on a PIN or a touch, so a human
// gets this long to answer even when the connect timeout is short.
const minHandshakeTimeout = 45 * time.Second

// Client dials outbound SSH connections. Safe to share.
type Client struct {
	// handshakeFloor is minHandshakeTimeout in production; a test lowers it
	// so a stalled agent socket can be shown to end the handshake in
	// milliseconds rather than three quarters of a minute.
	handshakeFloor time.Duration
}

var _ service.SSHClient = (*Client)(nil)

// NewClient constructs the SSH client adapter.
func NewClient() *Client { return &Client{handshakeFloor: minHandshakeTimeout} }

// Dial establishes an SSH connection per the params' auth and host-key policy.
func (c *Client) Dial(ctx context.Context, params service.SSHConnParams) (service.SSHSession, error) {
	timeout := params.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	// Bound for the handshake+auth, computed up front because the agent socket
	// (below) has to carry it too. gossh.NewClientConn is NOT covered by
	// cfg.Timeout (that only bounds the TCP dial), and a forwarded agent
	// (YubiKey / GPG smartcard) can block indefinitely waiting for a PIN or
	// touch — without a deadline the whole request hangs. Give a human enough
	// time to authorize, then fail cleanly.
	handshakeTimeout := timeout
	if handshakeTimeout < c.handshakeFloor {
		handshakeTimeout = c.handshakeFloor
	}
	handshakeDeadline := time.Now().Add(handshakeTimeout)

	authMethod, cleanup, err := authMethodFor(params, handshakeDeadline)
	if err != nil {
		return nil, err
	}
	// Keep the (agent) auth resource alive through the handshake, then release.
	defer cleanup()

	var (
		observedFP   string
		observedOnce sync.Once
	)
	hostKeyCallback, err := hostKeyCallbackFor(params, &observedFP, &observedOnce)
	if err != nil {
		return nil, err
	}

	port := params.Port
	if port == 0 {
		port = 22
	}

	cfg := &gossh.ClientConfig{
		User:            params.Username,
		Auth:            []gossh.AuthMethod{authMethod},
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	addr := net.JoinHostPort(params.Host, strconv.Itoa(port))
	// The TCP connection goes to the address the caller vetted, when it gave
	// one; addr (the name) stays what the handshake and every error names.
	// Resolving Host here again would be a second DNS answer the allowlist
	// never saw.
	dialAddr := addr
	if params.DialAddr != "" {
		dialAddr = net.JoinHostPort(params.DialAddr, strconv.Itoa(port))
	}

	// Dial with the connect timeout, honoring ctx cancellation.
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", dialAddr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	// The TCP deadline bounds the transport half of the handshake; the agent
	// socket's deadline (authMethodFor) bounds the auth half. Both are needed:
	// the auth callback blocks on the agent socket in the handshake goroutine,
	// where a failed transport read is not noticed until it returns. Cleared
	// on success.
	_ = conn.SetDeadline(handshakeDeadline)
	sshConn, chans, reqs, err := gossh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		// A strict refusal keeps its type through the handshake error so the
		// caller can tell "the key changed" from "the device is down" and
		// report both fingerprints. golang.org/x/crypto wraps the callback's
		// error with %w, so errors.As reaches it.
		var mismatch *service.HostKeyMismatchError
		if errors.As(err, &mismatch) {
			return nil, mismatch
		}
		if params.UseAgent {
			return nil, fmt.Errorf("ssh handshake: %w (using a forwarded agent — check that the PIN/touch prompt for your key was answered on your machine, and that the device trusts your public key)", err)
		}
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := gossh.NewClient(sshConn, chans, reqs)

	return &sshSession{client: client, fingerprint: observedFP}, nil
}

// authMethodFor picks password, key, or ssh-agent auth (exactly one). It returns
// a cleanup func to release any resource held for the handshake (the agent
// connection); callers must call it after the handshake. handshakeDeadline
// bounds every agent-socket operation: an agent that never answers (a
// smartcard waiting on a human who is not there) otherwise parks the
// handshake goroutine past every other deadline, because the auth callback
// runs synchronously inside it.
func authMethodFor(params service.SSHConnParams, handshakeDeadline time.Time) (gossh.AuthMethod, func(), error) {
	noop := func() {}
	if params.UseAgent {
		sock := params.AgentSock
		if sock == "" {
			sock = os.Getenv("SSH_AUTH_SOCK")
		}
		if sock == "" {
			return nil, noop, fmt.Errorf("ssh-agent auth requested but no agent socket (set SSH_AUTH_SOCK / forward your agent with ssh -A)")
		}
		conn, err := net.DialTimeout("unix", sock, time.Until(handshakeDeadline))
		if err != nil {
			return nil, noop, fmt.Errorf("connect ssh-agent at %s: %w", sock, err)
		}
		_ = conn.SetDeadline(handshakeDeadline)
		ag := agent.NewClient(conn)
		// Signers() proxies to the agent (YubiKey), so the conn must stay open
		// through the handshake; the returned cleanup closes it afterwards.
		return gossh.PublicKeysCallback(ag.Signers), func() { _ = conn.Close() }, nil
	}
	if params.PrivateKeyPEM != "" {
		var (
			signer gossh.Signer
			err    error
		)
		if params.Passphrase != "" {
			signer, err = gossh.ParsePrivateKeyWithPassphrase([]byte(params.PrivateKeyPEM), []byte(params.Passphrase))
		} else {
			signer, err = gossh.ParsePrivateKey([]byte(params.PrivateKeyPEM))
		}
		if err != nil {
			return nil, noop, fmt.Errorf("parse private key: %w", err)
		}
		return gossh.PublicKeys(signer), noop, nil
	}
	if params.Password != "" {
		return gossh.Password(params.Password), noop, nil
	}
	return nil, noop, fmt.Errorf("no SSH auth method provided (need password, private key, or agent)")
}

// hostKeyCallbackFor implements the TOFU / strict host-key policy. It records the
// observed SHA256 fingerprint into observedFP for the caller to pin/display.
func hostKeyCallbackFor(params service.SSHConnParams, observedFP *string, once *sync.Once) (gossh.HostKeyCallback, error) {
	policy := params.HostKeyPolicy
	if policy == "" {
		policy = "tofu"
	}
	switch policy {
	case "tofu":
		return func(_ string, _ net.Addr, key gossh.PublicKey) error {
			once.Do(func() { *observedFP = gossh.FingerprintSHA256(key) })
			return nil
		}, nil
	case "strict":
		want := params.KnownHostKeyFingerprint
		if want == "" {
			return nil, fmt.Errorf("strict host-key policy requires a known fingerprint")
		}
		// The SSH transport verifies the host key during key exchange, BEFORE
		// the authentication phase, so an error here ends the handshake with
		// no credential offered. That ordering is what the strict policy buys.
		return func(_ string, _ net.Addr, key gossh.PublicKey) error {
			got := gossh.FingerprintSHA256(key)
			once.Do(func() { *observedFP = got })
			if got != want {
				return &service.HostKeyMismatchError{Got: got, Want: want}
			}
			return nil
		}, nil
	default:
		return nil, fmt.Errorf("unknown host_key_policy %q (want tofu|strict)", policy)
	}
}

// sshSession wraps an *ssh.Client and satisfies service.SSHSession.
type sshSession struct {
	client      *gossh.Client
	fingerprint string

	sudoProbe  sync.Once
	sudoNoPass bool
}

func (s *sshSession) HostKeyFingerprint() string { return s.fingerprint }

func (s *sshSession) Close() error { return s.client.Close() }

// Run executes a command; a non-zero exit is returned in the result, not as err.
func (s *sshSession) Run(ctx context.Context, cmd string) (service.SSHRunResult, error) {
	return s.run(ctx, cmd, nil)
}

// effectiveSudoPassword drops the supplied password when the remote user has
// passwordless sudo. This is not an optimization: `sudo -S` only consumes the
// stdin password line when authentication is actually required, so on a
// NOPASSWD target the line would fall through to the wrapped command's stdin —
// for WriteFile that means the operator's sudo password written into the file.
// Probed once per session.
func (s *sshSession) effectiveSudoPassword(ctx context.Context, sudoPassword string) string {
	if sudoPassword == "" {
		return ""
	}
	s.sudoProbe.Do(func() {
		r, err := s.run(ctx, "sudo -n true", nil)
		s.sudoNoPass = err == nil && r.ExitCode == 0
	})
	if s.sudoNoPass {
		return ""
	}
	return sudoPassword
}

// RunSudo runs cmd as root. Empty (or unneeded) password → `sudo -n`; otherwise
// the password is piped to `sudo -S` on stdin so it never hits the process list.
func (s *sshSession) RunSudo(ctx context.Context, cmd string, sudoPassword string) (service.SSHRunResult, error) {
	sudoPassword = s.effectiveSudoPassword(ctx, sudoPassword)
	if sudoPassword == "" {
		return s.run(ctx, "sudo -n -- sh -c "+shellQuote(cmd), nil)
	}
	// -S read password from stdin, -p '' suppress the prompt, -k ignore cached creds.
	full := "sudo -S -p '' -k -- sh -c " + shellQuote(cmd)
	return s.run(ctx, full, bytes.NewReader([]byte(sudoPassword+"\n")))
}

// WriteFile writes content to path as root. It streams the bytes over stdin into
// `tee` so nothing sensitive lands in a command line, then chmods. umask 077
// keeps a freshly created file private even before the explicit chmod.
func (s *sshSession) WriteFile(ctx context.Context, path string, content []byte, mode uint32, sudoPassword string) error {
	sudoPassword = s.effectiveSudoPassword(ctx, sudoPassword)
	var stdin *bytes.Reader
	inner := "umask 077 && exec tee -- " + shellQuote(path) + " > /dev/null"
	var teeCmd string
	if sudoPassword == "" {
		stdin = bytes.NewReader(content)
		teeCmd = "sudo -n -- sh -c " + shellQuote(inner)
	} else {
		// `sudo -S` consumes the password line from stdin; the rest is the file.
		buf := append([]byte(sudoPassword+"\n"), content...)
		stdin = bytes.NewReader(buf)
		teeCmd = "sudo -S -p '' -k -- sh -c " + shellQuote(inner)
	}
	res, err := s.run(ctx, teeCmd, stdin)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write %s: exit %d: %s", path, res.ExitCode, res.Stderr)
	}
	if _, err := s.RunSudo(ctx, fmt.Sprintf("chmod %o %s", mode, shellQuote(path)), sudoPassword); err != nil {
		return err
	}
	return nil
}

// run opens a fresh session (SSH requires one per command), optionally feeding
// stdin, and honors ctx by closing the session on cancellation.
func (s *sshSession) run(ctx context.Context, cmd string, stdin *bytes.Reader) (service.SSHRunResult, error) {
	sess, err := s.newSession(ctx)
	if err != nil {
		return service.SSHRunResult{}, fmt.Errorf("open session: %w", err)
	}
	defer sess.Close()

	stdout := &cappedBuffer{limit: maxOutputBytes}
	stderr := &cappedBuffer{limit: maxOutputBytes}
	sess.Stdout = stdout
	sess.Stderr = stderr
	if stdin != nil {
		sess.Stdin = stdin
	}

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = sess.Close()
		return service.SSHRunResult{}, ctx.Err()
	case runErr := <-done:
		res := service.SSHRunResult{
			Stdout:    stdout.String(),
			Stderr:    stderr.String(),
			Truncated: stdout.truncated || stderr.truncated,
		}
		if runErr == nil {
			return res, nil
		}
		var exitErr *gossh.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		return res, runErr
	}
}

// newSession is client.NewSession made cancellation-aware. The channel-open
// it sends observes no context: a device that accepted the login and then
// wedged never answers it, and the caller's deadline was passing unnoticed
// while the whole adoption sat in "connect". On cancellation the TRANSPORT is
// closed, which is what unblocks the open — and a connection whose command
// context is gone has no further use anyway; the caller's next step fails
// with a clear "open session" error instead of hanging in turn.
func (s *sshSession) newSession(ctx context.Context) (*gossh.Session, error) {
	type opened struct {
		sess *gossh.Session
		err  error
	}
	done := make(chan opened, 1)
	go func() {
		sess, err := s.client.NewSession()
		done <- opened{sess, err}
	}()
	select {
	case <-ctx.Done():
		_ = s.client.Close()
		// The goroutine returns promptly once the transport is gone; whatever
		// it hands back is closed so a late success leaks nothing.
		go func() {
			if o := <-done; o.sess != nil {
				_ = o.sess.Close()
			}
		}()
		return nil, ctx.Err()
	case o := <-done:
		return o.sess, o.err
	}
}

// cappedBuffer keeps the first limit bytes written and silently discards the
// rest, remembering that it did. Its Write never fails: an error from a
// session's Stdout is reported by golang.org/x/crypto/ssh as the command's
// failure, and "the device printed too much" must not read as "the device
// could not be reached".
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if room <= 0 {
		if len(p) > 0 {
			c.truncated = true
		}
		return len(p), nil
	}
	if len(p) > room {
		c.truncated = true
		c.buf.Write(p[:room])
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }

// shellQuote single-quotes a path for safe interpolation into a remote command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
