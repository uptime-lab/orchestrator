package config

import (
	"strings"
	"testing"
)

// AGENT_GRPC_PROXY_PROTOCOL selects whether the agent listener requires a PROXY
// header. The default is off, the only other value is require, and anything
// else is a Problems() entry — refused, never coerced to off, because a typo
// that quietly disarmed the per-source gate is exactly the failure the enum
// exists to prevent.
func TestAgentGRPCProxyProtocolFromEnvironment(t *testing.T) {
	c := getDefaultConfig()
	if c.GRPC.ProxyProtocol != GRPCProxyProtocolOff {
		t.Fatalf("default ProxyProtocol = %q, want %q", c.GRPC.ProxyProtocol, GRPCProxyProtocolOff)
	}
	if c.GRPC.ProxyProtocolRequired() {
		t.Fatal("the default must not require PROXY protocol")
	}
	if p := c.GRPC.ProxyProtocolProblem(); p != "" {
		t.Fatalf("the default reported a problem: %s", p)
	}

	t.Setenv("AGENT_GRPC_PROXY_PROTOCOL", "require")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if !c.GRPC.ProxyProtocolRequired() {
		t.Fatalf("ProxyProtocol = %q after AGENT_GRPC_PROXY_PROTOCOL=require; ProxyProtocolRequired() = false", c.GRPC.ProxyProtocol)
	}
	if p := c.GRPC.ProxyProtocolProblem(); p != "" {
		t.Fatalf("require reported a problem: %s", p)
	}

	t.Setenv("AGENT_GRPC_PROXY_PROTOCOL", "off")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if c.GRPC.ProxyProtocolRequired() {
		t.Fatal("off must not require PROXY protocol")
	}

	// An empty YAML value is off, not a problem: most installs never set it.
	c.GRPC.ProxyProtocol = ""
	if p := c.GRPC.ProxyProtocolProblem(); p != "" || c.GRPC.ProxyProtocolRequired() {
		t.Fatalf("empty must read as off with no problem, got problem=%q required=%v", p, c.GRPC.ProxyProtocolRequired())
	}
}

func TestAnUnrecognisedProxyProtocolModeIsAProblemNotOff(t *testing.T) {
	for _, bad := range []string{"required", "optional", "on", "true", "v2", "REQUIRE"} {
		c := getDefaultConfig()
		t.Setenv("AGENT_GRPC_PROXY_PROTOCOL", bad)
		if _, err := ApplyEnvironment(c); err != nil {
			t.Fatalf("ApplyEnvironment(%q): %v", bad, err)
		}
		// Kept verbatim so the problem can name it.
		if c.GRPC.ProxyProtocol != bad {
			t.Fatalf("ApplyEnvironment rewrote %q to %q; the loader must not normalise an enum it then cannot report", bad, c.GRPC.ProxyProtocol)
		}
		if c.GRPC.ProxyProtocolRequired() {
			t.Fatalf("%q must not count as require", bad)
		}
		problem := c.GRPC.ProxyProtocolProblem()
		if problem == "" {
			t.Fatalf("AGENT_GRPC_PROXY_PROTOCOL=%q reported no problem; it would silently read as off", bad)
		}
		if !strings.Contains(problem, bad) || !strings.Contains(problem, "off|require") {
			t.Fatalf("the problem must name the value and the accepted set: %q", problem)
		}

		// Problems() carries it, so production refuses to boot and a
		// development install logs it.
		found := false
		for _, p := range c.Problems() {
			if p == problem {
				found = true
			}
		}
		if !found {
			t.Fatalf("Problems() omits the proxy-protocol problem for %q: %v", bad, c.Problems())
		}
		prod := baseProdConfig()
		prod.GRPC.ProxyProtocol = bad
		if err := prod.Validate(); err == nil || !strings.Contains(err.Error(), bad) {
			t.Fatalf("production must refuse AGENT_GRPC_PROXY_PROTOCOL=%q naming it, got %v", bad, err)
		}
	}
	// And the two accepted values pass production validation untouched.
	for _, good := range []string{GRPCProxyProtocolOff, GRPCProxyProtocolRequire, ""} {
		prod := baseProdConfig()
		prod.GRPC.ProxyProtocol = good
		if err := prod.Validate(); err != nil {
			t.Fatalf("production refused AGENT_GRPC_PROXY_PROTOCOL=%q: %v", good, err)
		}
	}
}

// AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE follows the global gate's rule: a
// positive integer sets it, and zero, negative or unparseable leave it alone —
// there is no unlimited setting for a bound whose absence is the vulnerability.
// The default is a quarter of the global gate, so filling the gate costs four
// addresses.
func TestAgentMaxPreAuthStreamsPerSourceFromEnvironment(t *testing.T) {
	c := getDefaultConfig()
	if c.Quota.MaxPreAuthStreamsPerSource != 256 {
		t.Fatalf("default MaxPreAuthStreamsPerSource = %d, want 256 (a quarter of the global pre-auth gate)", c.Quota.MaxPreAuthStreamsPerSource)
	}
	if c.Quota.MaxPreAuthStreamsPerSource*4 != c.Quota.MaxPreAuthStreams {
		t.Fatalf("per-source %d x 4 must equal the global %d", c.Quota.MaxPreAuthStreamsPerSource, c.Quota.MaxPreAuthStreams)
	}

	t.Setenv("AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE", "64")
	if _, err := ApplyEnvironment(c); err != nil {
		t.Fatalf("ApplyEnvironment: %v", err)
	}
	if c.Quota.MaxPreAuthStreamsPerSource != 64 {
		t.Fatalf("MaxPreAuthStreamsPerSource = %d after AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE=64", c.Quota.MaxPreAuthStreamsPerSource)
	}

	for _, rejected := range []string{"0", "-5", "many"} {
		t.Setenv("AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE", rejected)
		if _, err := ApplyEnvironment(c); err != nil {
			t.Fatalf("ApplyEnvironment(%q): %v", rejected, err)
		}
		if c.Quota.MaxPreAuthStreamsPerSource != 64 {
			t.Fatalf("AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE=%q changed the cap to %d; a value that cannot bound must be ignored",
				rejected, c.Quota.MaxPreAuthStreamsPerSource)
		}
	}
}
