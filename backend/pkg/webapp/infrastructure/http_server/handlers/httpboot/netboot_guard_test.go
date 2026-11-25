package httpboot

import (
	"fmt"
	"testing"
	"time"
)

func TestNetbootGuard(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	g := newNetbootGuard()
	g.clock = func() time.Time { return now }

	t.Run("stays unbanned below the threshold", func(t *testing.T) {
		for i := 0; i < g.max-1; i++ {
			if g.strike("1.2.3.4") {
				t.Fatalf("banned early at strike %d", i)
			}
		}
		if g.banned("1.2.3.4") {
			t.Fatal("banned below threshold")
		}
	})

	t.Run("bans at the threshold and refuses until expiry", func(t *testing.T) {
		g := newNetbootGuard()
		g.clock = func() time.Time { return now }
		var banned bool
		for i := 0; i < g.max; i++ {
			banned = g.strike("5.6.7.8")
		}
		if !banned || !g.banned("5.6.7.8") {
			t.Fatal("not banned at threshold")
		}
		// still banned just before expiry
		now = now.Add(g.banFor - time.Second)
		if !g.banned("5.6.7.8") {
			t.Fatal("ban lifted early")
		}
		// lifted after expiry
		now = now.Add(2 * time.Second)
		if g.banned("5.6.7.8") {
			t.Fatal("ban not lifted after expiry")
		}
	})

	t.Run("strikes older than the window do not accumulate to a ban", func(t *testing.T) {
		now = time.Unix(1_700_100_000, 0)
		g := newNetbootGuard()
		g.clock = func() time.Time { return now }
		// one strike per (window+1s) never reaches the threshold
		for i := 0; i < g.max*2; i++ {
			if g.strike("9.9.9.9") {
				t.Fatalf("banned by spaced-out strikes at %d", i)
			}
			now = now.Add(g.window + time.Second)
		}
	})

	t.Run("a legitimate cluster IP (no unknown-serial strikes) is never banned", func(t *testing.T) {
		g := newNetbootGuard()
		g.clock = func() time.Time { return now }
		// never call strike() for this IP — it only ever requested known serials
		if g.banned("10.0.0.1") {
			t.Fatal("clean IP reported as banned")
		}
	})

	t.Run("empty IP is ignored", func(t *testing.T) {
		g := newNetbootGuard()
		if g.strike("") || g.banned("") {
			t.Fatal("empty IP should never be banned")
		}
	})
}

// Strikes from many distinct addresses — one request each, which the per-IP
// rate limiter cannot see — must neither grow the map without bound nor cost
// more per strike as it grows: the guard's lock is taken by every netboot
// request, so a strike that walks the whole map stalls every device's boot.
func TestNetbootGuardStaysCheapUnderManyAddresses(t *testing.T) {
	now := time.Unix(1_700_200_000, 0)
	g := newNetbootGuard()
	g.clock = func() time.Time { return now }

	const addresses = 70_000
	start := time.Now()
	for i := 0; i < addresses; i++ {
		g.strike(fmt.Sprintf("2001:db8::%x", i))
	}
	elapsed := time.Since(start)

	if n := len(g.entries); n > netbootGuardMaxEntries {
		t.Errorf("guard holds %d entries after %d distinct addresses; want it capped", n, addresses)
	}
	// Linear work is a few tens of milliseconds; a sweep per strike is
	// quadratic and takes tens of seconds here.
	if elapsed > 3*time.Second {
		t.Errorf("%d strikes took %v; want the cost per strike independent of the map size", addresses, elapsed)
	}

	// The cap may refuse to track a NEW address while full, but one already
	// tracked keeps its ban.
	g2 := newNetbootGuard()
	g2.clock = g.clock
	for i := 0; i < g2.max; i++ {
		g2.strike("198.51.100.7")
	}
	for i := 0; i < addresses; i++ {
		g2.strike(fmt.Sprintf("2001:db8::%x", i))
	}
	if !g2.banned("198.51.100.7") {
		t.Error("a ban was lost while the guard was full")
	}
}
