package terminal

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The pending-handshake quota used to be ONE global counter, so a single unauthenticated source
// could hold every slot and deny terminals to everyone else. Admission is now
// isolated by the trusted client address — a per-source pending cap and an
// arrival-rate limiter, both before the upgrade — while the global cap and
// the frame/deadline bounds stay as they were.

const (
	sourceA = "203.0.113.10"
	sourceB = "198.51.100.20"
	sourceC = "192.0.2.30"
)

// A source over its own pending quota is refused 429, and the refusal names
// the address as the reason so the client does not read it as a platform
// outage; a second source is admitted at the same moment, which is the whole
// point of keying the bound on the source.
func TestPendingHandshakesAreCappedPerSource(t *testing.T) {
	hs := newHarness(t)
	hs.cfg.Terminal.MaxPendingHandshakesPerIP = 2
	hs.cfg.Terminal.MaxPendingHandshakes = 100

	hs.dialFrom(t, sourceA)
	hs.dialFrom(t, sourceA)
	_, status, body := hs.dialFrom(t, sourceA)
	if status != http.StatusTooManyRequests || !strings.Contains(body, "from this address") {
		t.Fatalf("third silent socket from A answered %d %q, want 429 naming the address", status, body)
	}
	if _, status, _ := hs.dialFrom(t, sourceB); status != http.StatusSwitchingProtocols {
		t.Fatalf("a socket from B while A is saturated answered %d, want 101", status)
	}
}

// Every pre-auth exit path hands the source's slot back, and a source whose
// count reaches zero is DELETED from the map — otherwise the map grows by one
// entry per address that ever tried, which is the shape of a slow leak that
// an unauthenticated peer controls.
func TestPerSourcePendingSlotsAreReleasedAndTheEntryDeleted(t *testing.T) {
	saturate := func(t *testing.T, hs *harness) []*websocket.Conn {
		t.Helper()
		first, _, _ := hs.dialFrom(t, sourceA)
		second, _, _ := hs.dialFrom(t, sourceA)
		if _, status, _ := hs.dialFrom(t, sourceA); status != http.StatusTooManyRequests {
			t.Fatalf("A was not saturated: third socket answered %d", status)
		}
		return []*websocket.Conn{first, second}
	}
	expectFreed := func(t *testing.T, hs *harness) {
		t.Helper()
		waitFor(t, "A's per-source entry to be deleted", func() bool {
			_, present := hs.h.pendingFor(sourceA)
			return !present
		})
		if _, status, _ := hs.dialFrom(t, sourceA); status != http.StatusSwitchingProtocols {
			t.Fatalf("A after releasing its sockets answered %d, want 101", status)
		}
	}

	t.Run("peer closes", func(t *testing.T) {
		hs := newHarness(t)
		hs.cfg.Terminal.MaxPendingHandshakesPerIP = 2
		for _, conn := range saturate(t, hs) {
			conn.Close()
		}
		expectFreed(t, hs)
	})

	t.Run("auth fails", func(t *testing.T) {
		hs := newHarness(t)
		hs.cfg.Terminal.MaxPendingHandshakesPerIP = 2
		for _, conn := range saturate(t, hs) {
			if err := conn.WriteJSON(map[string]any{"token": "wrong", "cols": 80, "rows": 24}); err != nil {
				t.Fatal(err)
			}
			readClose(t, conn)
		}
		expectFreed(t, hs)
	})

	t.Run("auth deadline", func(t *testing.T) {
		hs := newHarness(t)
		hs.cfg.Terminal.MaxPendingHandshakesPerIP = 2
		authFrameDeadlineOverride.Store(int64(200 * time.Millisecond))
		t.Cleanup(func() { authFrameDeadlineOverride.Store(0) })
		for _, conn := range saturate(t, hs) {
			readClose(t, conn)
		}
		expectFreed(t, hs)
	})

	t.Run("auth succeeds", func(t *testing.T) {
		hs := newHarness(t)
		hs.cfg.Terminal.MaxPendingHandshakesPerIP = 1
		conn, _, _ := hs.dialFrom(t, sourceA)
		hs.authenticate(t, conn)
		expectFreed(t, hs)
	})
}

// The per-source cap does not replace the global one: many sources each
// under their own quota still add up, and the 503 that names the platform
// as the reason is kept for exactly that case.
func TestGlobalPendingCapStillAppliesAcrossSources(t *testing.T) {
	hs := newHarness(t)
	hs.cfg.Terminal.MaxPendingHandshakesPerIP = 2
	hs.cfg.Terminal.MaxPendingHandshakes = 2

	hs.dialFrom(t, sourceA)
	hs.dialFrom(t, sourceB)
	_, status, body := hs.dialFrom(t, sourceC)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "in progress") {
		t.Fatalf("third source past the global cap answered %d %q, want 503", status, body)
	}
	if count, _ := hs.h.pendingFor(sourceC); count != 0 {
		t.Fatalf("a globally refused source kept %d per-source slot(s)", count)
	}
}

// A burst of handshakes from one address is refused 429 before the upgrade
// once the bucket is empty, whether or not the sockets stay open — a source
// churning short-lived connections holds no pending slot for long, so the
// rate limiter is the bound that meets it. Another address is unaffected.
func TestHandshakeArrivalsAreRateLimitedPerSource(t *testing.T) {
	hs := newHarness(t)
	hs.cfg.Terminal.MaxPendingHandshakesPerIP = 100
	hs.cfg.Terminal.MaxPendingHandshakes = 100

	for i := 0; i < handshakeArrivalBurst; i++ {
		if _, status, body := hs.dialFrom(t, sourceA); status != http.StatusSwitchingProtocols {
			t.Fatalf("handshake %d of %d from A answered %d %q, want 101", i+1, handshakeArrivalBurst, status, body)
		}
	}
	refused := false
	for i := 0; i < 3 && !refused; i++ {
		_, status, body := hs.dialFrom(t, sourceA)
		if status == http.StatusTooManyRequests {
			if !strings.Contains(body, "rate limit") {
				t.Fatalf("rate-limited handshake answered %q, want the rate-limit message", body)
			}
			refused = true
		}
	}
	if !refused {
		t.Fatalf("%d+3 handshakes from A in a burst were all admitted", handshakeArrivalBurst)
	}
	if _, status, _ := hs.dialFrom(t, sourceB); status != http.StatusSwitchingProtocols {
		t.Fatalf("a socket from B while A is rate-limited answered %d, want 101", status)
	}
}
