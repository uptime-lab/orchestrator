package httpboot

import (
	"sync"
	"time"
)

// netbootGuard auto-bans an IP that keeps requesting UNKNOWN/malformed serials on
// the public netboot endpoint — the signature of serial enumeration or a spoofing
// flood. It is NAT-aware by design: a legitimate cluster behind one NAT IP only
// ever requests its OWN adopted serials (which resolve to nodes), so it accumulates
// zero strikes and is never banned no matter how many devices boot at once. Only
// requests for serials the platform does not know count as strikes.
//
// This is deliberately separate from the per-IP rate limiter (which stays generous
// so a 100+ device cluster power-cycling together is not throttled): volume alone
// is legitimate; volume of UNKNOWN serials is not.
//
// Its lock is taken by EVERY netboot request (banGate), so nothing done under it
// may grow with the number of addresses seen: unauthenticated requests from many
// source addresses, one each, are cheap to send and invisible to the per-IP rate
// limiter. Expired entries are swept at most once per window, and the map is
// capped: when it is full, a NEW address is not tracked (an address sending one
// request is not what the ban is for), while every address already tracked —
// every ban among them — is kept.
type netbootGuard struct {
	mu        sync.Mutex
	entries   map[string]*ipStrikes
	window    time.Duration // strikes are counted within this rolling window
	max       int           // strikes within the window that trigger a ban
	banFor    time.Duration // how long a ban lasts
	clock     func() time.Time
	lastSweep time.Time
}

// netbootGuardMaxEntries caps the addresses tracked at once. A cluster behind
// NAT is one entry, and a probing address is one entry, so the cap is far above
// anything but a flood from many addresses.
const netbootGuardMaxEntries = 1 << 16

type ipStrikes struct {
	count       int
	windowStart time.Time
	bannedUntil time.Time
}

func newNetbootGuard() *netbootGuard {
	return &netbootGuard{
		entries: make(map[string]*ipStrikes),
		window:  time.Minute,
		max:     20,
		banFor:  10 * time.Minute,
		clock:   time.Now,
	}
}

// banned reports whether the IP is currently banned.
func (g *netbootGuard) banned(ip string) bool {
	if ip == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.entries[ip]
	return s != nil && g.clock().Before(s.bannedUntil)
}

// strike records one unknown/malformed-serial request from ip and returns whether
// the IP is now banned.
func (g *netbootGuard) strike(ip string) bool {
	if ip == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()

	g.maybeSweep(now)
	s := g.entries[ip]
	if s == nil {
		if len(g.entries) >= netbootGuardMaxEntries {
			return false
		}
		s = &ipStrikes{windowStart: now}
		g.entries[ip] = s
	}
	// Roll the window: strikes older than `window` no longer count.
	if now.Sub(s.windowStart) > g.window {
		s.windowStart = now
		s.count = 0
	}
	s.count++
	if s.count >= g.max {
		s.bannedUntil = now.Add(g.banFor)
	}

	return now.Before(s.bannedUntil)
}

// maybeSweep runs a sweep at most once per window, so its cost is amortised
// over every strike in that window instead of paid by each one.
func (g *netbootGuard) maybeSweep(now time.Time) {
	if now.Sub(g.lastSweep) < g.window {
		return
	}
	g.lastSweep = now
	g.sweep(now)
}

// sweep evicts entries that are neither banned nor within an active window.
func (g *netbootGuard) sweep(now time.Time) {
	for ip, s := range g.entries {
		if now.After(s.bannedUntil) && now.Sub(s.windowStart) > g.window {
			delete(g.entries, ip)
		}
	}
}

// strikeCount reports the strikes recorded for an IP inside the current window.
// Test-only: the guard's whole purpose is to make a probe visible, and a test
// that cannot see a strike cannot assert that it happened.
func (g *netbootGuard) strikeCount(ip string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.entries[ip]; ok {
		return e.count
	}
	return 0
}
