package config

import "testing"

// AGENT_MAX_PREAUTH_STREAMS sets the global pre-auth stream gate. The default
// is the product of the two per-address bounds (256 connections per client IP
// at the edge x 4 streams per connection), and there is no "unlimited": a
// zero, negative or unparseable value leaves the configured cap alone rather
// than disabling the one bound whose absence is the vulnerability.
func TestAgentMaxPreAuthStreamsFromEnvironment(t *testing.T) {
	c := getDefaultConfig()
	if c.Quota.MaxPreAuthStreams != 1024 {
		t.Fatalf("default MaxPreAuthStreams = %d, want 1024 (256 connections x 4 streams)", c.Quota.MaxPreAuthStreams)
	}

	t.Setenv("AGENT_MAX_PREAUTH_STREAMS", "2048")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if c.Quota.MaxPreAuthStreams != 2048 {
		t.Fatalf("MaxPreAuthStreams = %d after AGENT_MAX_PREAUTH_STREAMS=2048", c.Quota.MaxPreAuthStreams)
	}

	for _, rejected := range []string{"0", "-5", "many"} {
		t.Setenv("AGENT_MAX_PREAUTH_STREAMS", rejected)
		if _, err := ApplyEnvironment(c); err != nil {
			t.Fatalf("ApplyEnvironment(%q): %v", rejected, err)
		}
		if c.Quota.MaxPreAuthStreams != 2048 {
			t.Fatalf("AGENT_MAX_PREAUTH_STREAMS=%q changed the cap to %d; a value that cannot bound must be ignored",
				rejected, c.Quota.MaxPreAuthStreams)
		}
	}
}
