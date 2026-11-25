package terminal

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// The handshake used to be unbounded: the socket was upgraded before the
// kill-switch was read, the auth
// frame was read with no size bound, nothing bounded sockets that had not yet
// presented a token, and a post-auth input frame of any size became one gRPC
// message on the node's stream.

// A disabled terminal is an HTTP refusal, not an upgraded socket that then
// says no. The Swagger annotation promised a 403; it was a 101.
func TestDisabledTerminalRefusesAtTheHandshake(t *testing.T) {
	hs := newHarness(t)
	hs.cfg.Terminal.Enabled = false
	if _, status := hs.dialStatus(t); status != http.StatusForbidden {
		t.Fatalf("disabled terminal answered %d, want 403", status)
	}
}

// oversizedAuthFrame is a syntactically valid auth message whose token alone
// is 9 KiB. Valid on purpose: the JSON decoder reads it to the end, so the
// bound is what refuses it, not a syntax error on the first byte.
func oversizedAuthFrame() []byte {
	return []byte(`{"token":"` + strings.Repeat("a", 9*1024) + `"}`)
}

// The auth frame carries a JWT and two integers; anything much larger is not
// a client. gorilla answers an overflow with close 1009 before the bytes are
// buffered. The dialer's write buffer is widened so the message travels as
// ONE frame — the default 4 KiB buffer would fragment it, which is the next
// test.
func TestOversizedAuthFrameIsClosedWithMessageTooBig(t *testing.T) {
	hs := newHarness(t)
	dialer := *websocket.DefaultDialer
	dialer.WriteBufferSize = 16 * 1024
	conn, _, err := dialer.Dial(hs.url(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, oversizedAuthFrame()); err != nil {
		t.Fatal(err)
	}
	if code, _ := readClose(t, conn); code != websocket.CloseMessageTooBig {
		t.Fatalf("9 KiB auth frame closed with %d, want 1009", code)
	}
	if hs.term.openedCount() != 0 {
		t.Fatal("an oversized auth frame opened a terminal")
	}
}

// The bound is on the MESSAGE, not the frame: a peer fragmenting a large
// message into small frames is caught by the accumulated length.
func TestFragmentedOversizedAuthMessageIsClosedWithMessageTooBig(t *testing.T) {
	hs := newHarness(t)
	dialer := *websocket.DefaultDialer
	dialer.WriteBufferSize = 1024
	conn, _, err := dialer.Dial(hs.url(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	w, err := conn.NextWriter(websocket.TextMessage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(oversizedAuthFrame()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if code, _ := readClose(t, conn); code != websocket.CloseMessageTooBig {
		t.Fatalf("fragmented 9 KiB auth message closed with %d, want 1009", code)
	}
}

// A socket that sends nothing is ended at the auth deadline, so a stranger
// cannot park one open. Pre-existing behaviour, kept because the pending cap
// below rests on it: the slot is released when the handler returns.
func TestSilentSocketIsClosedAtTheAuthDeadline(t *testing.T) {
	hs := newHarness(t)
	authFrameDeadlineOverride.Store(int64(200 * time.Millisecond))
	t.Cleanup(func() { authFrameDeadlineOverride.Store(0) })

	conn := hs.dial(t)
	started := time.Now()
	_, texts := readClose(t, conn)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("silent socket lived %s past a 200ms deadline", elapsed)
	}
	if len(texts) != 1 || !strings.Contains(texts[0], "expected auth message") {
		t.Fatalf("silent socket got %q, want the auth-expected error", texts)
	}
}

// Upgraded-but-unauthenticated sockets are bounded, and the bound is applied
// BEFORE the upgrade so a refusal costs an HTTP response and nothing else.
func TestPendingHandshakesAreCapped(t *testing.T) {
	hs := newHarness(t)
	hs.h.maxPendingHandshakes = 2

	first := hs.dial(t)
	hs.dial(t)
	if _, status := hs.dialStatus(t); status != http.StatusServiceUnavailable {
		t.Fatalf("third unauthenticated socket answered %d, want 503", status)
	}

	// Closing one hands its slot back once the handler notices.
	first.Close()
	waitFor(t, "a released pending slot", func() bool {
		conn, status := hs.dialStatus(t)
		if conn != nil {
			conn.Close()
		}
		return status == http.StatusSwitchingProtocols
	})
}

// Once a socket authenticates, the per-user cap is the bound that applies and
// the pending slot goes back to the strangers' pool — otherwise every open
// shell would count against the handshake cap for its whole life.
func TestPendingSlotIsReleasedOnAuthSuccess(t *testing.T) {
	hs := newHarness(t)
	hs.h.maxPendingHandshakes = 1

	conn := hs.dial(t)
	hs.authenticate(t, conn)
	waitFor(t, "the pending count to drop", func() bool {
		hs.h.pendingMu.Lock()
		defer hs.h.pendingMu.Unlock()
		return hs.h.pending == 0
	})
	if _, status := hs.dialStatus(t); status != http.StatusSwitchingProtocols {
		t.Fatalf("a second handshake behind one OPEN shell answered %d, want 101", status)
	}
}

// After auth, an input frame is forwarded as one gRPC message on the node's
// platform→node stream, where an oversized one fails Stream.Send and takes
// the whole stream with it. The bound must land on this socket instead: the
// session ends, the device is told, and the audit close row is written.
func TestOversizedInputFrameEndsTheSessionNotTheNodeStream(t *testing.T) {
	hs := newHarness(t)
	conn := hs.dial(t)
	hs.authenticate(t, conn)

	frame := []byte(`{"type":"input","data":"` + strings.Repeat("x", 65*1024) + `"}`)
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		t.Fatal(err)
	}
	if code, _ := readClose(t, conn); code != websocket.CloseMessageTooBig {
		t.Fatalf("65 KiB input frame closed with %d, want 1009", code)
	}
	if hs.term.inputCount() != 0 {
		t.Fatal("the oversized frame was forwarded to the device")
	}
	waitFor(t, "CloseTerminal", func() bool { return hs.term.closedCount() == 1 })
	waitFor(t, "the audit close row", func() bool { return len(hs.sink.Actions()) == 2 })
	if actions := hs.sink.Actions(); actions[0] != audit_event.ActionNodeTerminalOpen || actions[1] != audit_event.ActionNodeTerminalClose {
		t.Fatalf("audit rows %v", actions)
	}
}

// The handshake half of the live-session rule: an access token verifies
// statelessly until its
// TTL, so one whose session was already signed out must be refused BEFORE a
// PTY is spawned.
func TestRevokedSessionCannotOpenAShell(t *testing.T) {
	hs := newHarness(t)
	hs.auth.live.Store(false)
	conn := hs.dial(t)
	if err := conn.WriteJSON(map[string]any{"token": goodToken, "cols": 80, "rows": 24}); err != nil {
		t.Fatal(err)
	}
	_, texts := readClose(t, conn)
	if len(texts) != 1 || !strings.Contains(texts[0], "session is no longer valid") {
		t.Fatalf("revoked session got %q, want the session refusal", texts)
	}
	if hs.term.openedCount() != 0 {
		t.Fatal("a revoked session opened a terminal")
	}
}
