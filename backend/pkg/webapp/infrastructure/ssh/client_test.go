package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	gossh "golang.org/x/crypto/ssh"
)

// testServer is an in-process SSH server with one known host key. It accepts
// the password "secret" and scripts three commands: "big" writes 4 MiB and
// exits 3, "block" never answers until the channel is closed, anything else
// echoes the command back and exits 0.
type testServer struct {
	addr        string
	port        int
	fingerprint string
	// passwordCalls counts how many times the server's PasswordCallback ran —
	// i.e. how many times a client OFFERED a credential. A strict host-key
	// refusal must leave it at zero.
	passwordCalls atomic.Int32
}

func startTestServer(t *testing.T) *testServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srv := &testServer{fingerprint: gossh.FingerprintSHA256(signer.PublicKey())}

	cfg := &gossh.ServerConfig{
		PasswordCallback: func(_ gossh.ConnMetadata, password []byte) (*gossh.Permissions, error) {
			srv.passwordCalls.Add(1)
			if string(password) == "secret" {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
		// Advertised so a client asked to use an agent actually consults it:
		// gossh only tries methods the server lists after the "none" probe.
		PublicKeyCallback: func(_ gossh.ConnMetadata, _ gossh.PublicKey) (*gossh.Permissions, error) {
			return nil, errors.New("no key is trusted here")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv.addr = ln.Addr().(*net.TCPAddr).IP.String()
	srv.port = ln.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn, cfg)
		}
	}()
	return srv
}

func (s *testServer) serve(conn net.Conn, cfg *gossh.ServerConfig) {
	sconn, chans, reqs, err := gossh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sconn.Close()
	go gossh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(gossh.UnknownChannelType, "session only")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, chReqs)
	}
}

func (s *testServer) session(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		_ = gossh.Unmarshal(req.Payload, &payload)
		_ = req.Reply(true, nil)
		status := uint32(0)
		switch payload.Command {
		case "big":
			chunk := []byte(strings.Repeat("x", 64*1024))
			for i := 0; i < 64; i++ { // 4 MiB
				if _, err := ch.Write(chunk); err != nil {
					return
				}
			}
			status = 3
		case "block":
			// Hold the session open until the client closes it. The client sends
			// stdin EOF at once, so a Read cannot tell "still open" from "gone";
			// a periodic one-byte write fails only once the channel is closed.
			for {
				time.Sleep(50 * time.Millisecond)
				if _, err := ch.Write([]byte(".")); err != nil {
					return
				}
			}
		default:
			_, _ = ch.Write([]byte(payload.Command))
		}
		_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

func (s *testServer) params() service.SSHConnParams {
	return service.SSHConnParams{
		Host:           s.addr,
		Port:           s.port,
		Username:       "pi",
		Password:       "secret",
		ConnectTimeout: 5 * time.Second,
	}
}

func TestStrictWrongFingerprintRefusesBeforeAnyCredentialIsOffered(t *testing.T) {
	srv := startTestServer(t)
	p := srv.params()
	p.HostKeyPolicy = "strict"
	p.KnownHostKeyFingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	sess, err := NewClient().Dial(context.Background(), p)
	if err == nil {
		_ = sess.Close()
		t.Fatal("Dial succeeded against the wrong host key")
	}
	var mismatch *service.HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %v, want a HostKeyMismatchError", err)
	}
	if mismatch.Got != srv.fingerprint || mismatch.Want != p.KnownHostKeyFingerprint {
		t.Errorf("mismatch = got %q want %q; server key %q", mismatch.Got, mismatch.Want, srv.fingerprint)
	}
	if n := srv.passwordCalls.Load(); n != 0 {
		t.Fatalf("the server's PasswordCallback ran %d times: a credential was offered to a host with the wrong key", n)
	}
}

func TestStrictRightFingerprintConnects(t *testing.T) {
	srv := startTestServer(t)
	p := srv.params()
	p.HostKeyPolicy = "strict"
	p.KnownHostKeyFingerprint = srv.fingerprint

	sess, err := NewClient().Dial(context.Background(), p)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()
	if sess.HostKeyFingerprint() != srv.fingerprint {
		t.Errorf("fingerprint = %q, want %q", sess.HostKeyFingerprint(), srv.fingerprint)
	}
}

func TestTofuConnectsAndReportsTheServerKey(t *testing.T) {
	srv := startTestServer(t)
	sess, err := NewClient().Dial(context.Background(), srv.params())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()
	if sess.HostKeyFingerprint() != srv.fingerprint {
		t.Errorf("fingerprint = %q, want the server key's %q", sess.HostKeyFingerprint(), srv.fingerprint)
	}
	r, err := sess.Run(context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Stdout != "echo hi" || r.ExitCode != 0 || r.Truncated {
		t.Errorf("Run = %+v", r)
	}
}

func TestDialAddrIsWhatTheConnectionGoesTo(t *testing.T) {
	srv := startTestServer(t)
	p := srv.params()
	// A name that resolves nowhere: the connection must succeed only because
	// the vetted DialAddr, not Host, is what gets dialed.
	p.Host = "device.invalid"
	p.DialAddr = srv.addr

	sess, err := NewClient().Dial(context.Background(), p)
	if err != nil {
		t.Fatalf("Dial with DialAddr: %v", err)
	}
	_ = sess.Close()
}

func TestOversizedOutputIsCappedAndTheExitCodeSurvives(t *testing.T) {
	srv := startTestServer(t)
	sess, err := NewClient().Dial(context.Background(), srv.params())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	r, err := sess.Run(context.Background(), "big")
	if err != nil {
		t.Fatalf("Run: %v (an oversized output must not read as a transport failure)", err)
	}
	if len(r.Stdout) != maxOutputBytes {
		t.Errorf("len(Stdout) = %d, want exactly the cap %d", len(r.Stdout), maxOutputBytes)
	}
	if !r.Truncated {
		t.Error("Truncated = false, want true")
	}
	if r.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", r.ExitCode)
	}
}

func TestBlockingCommandHonoursTheContext(t *testing.T) {
	srv := startTestServer(t)
	sess, err := NewClient().Dial(context.Background(), srv.params())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = sess.Run(ctx, "block")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline error", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Run returned after %s, want promptly after the 300ms deadline", elapsed)
	}
}

// ---- NewSession and the agent socket must honour a bound ----

// startDeafServer runs an SSH server that completes the handshake and password
// auth but NEVER services a channel-open: the incoming channel queue is left
// unread, so a client's NewSession waits for a reply that will not come. That
// is the shape of a device that accepts the login and then wedges.
func startDeafServer(t *testing.T) *testServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srv := &testServer{fingerprint: gossh.FingerprintSHA256(signer.PublicKey())}
	cfg := &gossh.ServerConfig{
		PasswordCallback: func(_ gossh.ConnMetadata, _ []byte) (*gossh.Permissions, error) { return nil, nil },
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv.addr = ln.Addr().(*net.TCPAddr).IP.String()
	srv.port = ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := gossh.NewServerConn(conn, cfg)
				if err != nil {
					_ = conn.Close()
					return
				}
				defer func() { _ = sconn.Close() }()
				go gossh.DiscardRequests(reqs)
				// Deliberately never read from chans: park until the client
				// gives up and the transport closes.
				_ = chans
				_ = sconn.Wait()
			}()
		}
	}()
	return srv
}

func TestNewSessionThatIsNeverAnsweredReturnsAtTheContextDeadline(t *testing.T) {
	srv := startDeafServer(t)
	sess, err := NewClient().Dial(context.Background(), srv.params())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.Run(ctx, "true")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context's deadline error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run is still blocked in NewSession 3s after a 300ms deadline: the channel-open observes no context")
	}
}

// startDeafAgentSocket listens on a unix socket and accepts connections without
// ever writing a byte back: an ssh-agent whose smartcard is waiting on a human
// who is not there.
func startDeafAgentSocket(t *testing.T) string {
	t.Helper()
	// macOS caps sun_path at 104 bytes and t.TempDir() alone is longer than
	// that under /var/folders, so the socket lives under a short /tmp dir.
	dir, err := os.MkdirTemp("/tmp", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := dir + "/a.sock"
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 1024)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	return path
}

func TestStalledAgentSocketEndsTheHandshakeAtTheDeadline(t *testing.T) {
	srv := startTestServer(t)
	p := srv.params()
	p.Password = ""
	p.UseAgent = true
	p.AgentSock = startDeafAgentSocket(t)
	p.ConnectTimeout = 300 * time.Millisecond

	c := &Client{handshakeFloor: 300 * time.Millisecond}
	done := make(chan error, 1)
	go func() {
		sess, err := c.Dial(context.Background(), p)
		if err == nil {
			_ = sess.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Dial succeeded against an agent that never answered")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dial is still blocked on the agent socket 3s after a 300ms handshake bound")
	}
}
