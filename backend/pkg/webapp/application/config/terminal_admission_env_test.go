package config

import "testing"

// TERMINAL_MAX_PENDING_HANDSHAKES and TERMINAL_MAX_PENDING_HANDSHAKES_PER_IP
// set the terminal's pre-auth admission bounds, global and per trusted client
// address. There is no "unlimited": a zero, negative or unparseable value
// leaves the configured cap alone rather than disabling the bound whose
// absence is the vulnerability.
func TestTerminalPendingHandshakeCapsFromEnvironment(t *testing.T) {
	c := getDefaultConfig()
	if c.Terminal.MaxPendingHandshakes != 64 {
		t.Fatalf("default MaxPendingHandshakes = %d, want 64", c.Terminal.MaxPendingHandshakes)
	}
	if c.Terminal.MaxPendingHandshakesPerIP != 8 {
		t.Fatalf("default MaxPendingHandshakesPerIP = %d, want 8", c.Terminal.MaxPendingHandshakesPerIP)
	}

	t.Setenv("TERMINAL_MAX_PENDING_HANDSHAKES", "128")
	t.Setenv("TERMINAL_MAX_PENDING_HANDSHAKES_PER_IP", "16")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if c.Terminal.MaxPendingHandshakes != 128 || c.Terminal.MaxPendingHandshakesPerIP != 16 {
		t.Fatalf("caps = %d/%d after TERMINAL_MAX_PENDING_HANDSHAKES=128 and _PER_IP=16",
			c.Terminal.MaxPendingHandshakes, c.Terminal.MaxPendingHandshakesPerIP)
	}

	for _, rejected := range []string{"0", "-5", "many"} {
		t.Setenv("TERMINAL_MAX_PENDING_HANDSHAKES", rejected)
		t.Setenv("TERMINAL_MAX_PENDING_HANDSHAKES_PER_IP", rejected)
		if _, err := ApplyEnvironment(c); err != nil {
			t.Fatalf("ApplyEnvironment(%q): %v", rejected, err)
		}
		if c.Terminal.MaxPendingHandshakes != 128 || c.Terminal.MaxPendingHandshakesPerIP != 16 {
			t.Fatalf("value %q changed the caps to %d/%d; a value that cannot bound must be ignored",
				rejected, c.Terminal.MaxPendingHandshakes, c.Terminal.MaxPendingHandshakesPerIP)
		}
	}
}
