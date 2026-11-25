package config

import (
	"strings"
	"testing"
)

// The agent channel has three supported shapes and a production boot must
// hear which one it is in: TLS at the server (certificate files), TLS at a
// reverse proxy (GRPC_TLS_TERMINATED_EXTERNALLY, with PROXY protocol so the
// per-source bound sees real addresses), or plaintext on a trusted LAN by
// declaration (AGENT_GRPC_PLAINTEXT_OK). Nothing declared is a refusal that
// names the two declarations; plaintext declared is ONE advisory, not a
// refusal.

func plaintextProdConfig() *Config {
	c := baseProdConfig()
	c.GRPC.TLSCertFile = ""
	c.GRPC.TLSKeyFile = ""
	c.GRPC.TLSTerminatedExternally = false
	return c
}

func TestAnUndeclaredPlaintextAgentChannelIsRefusedNamingBothDeclarations(t *testing.T) {
	c := plaintextProdConfig()
	err := c.Validate()
	if err == nil {
		t.Fatal("production booted a plaintext agent channel with nothing declared")
	}
	for _, name := range []string{"GRPC_TLS_TERMINATED_EXTERNALLY", "AGENT_GRPC_PLAINTEXT_OK", "AGENT_GRPC_PROXY_PROTOCOL=require"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the refusal must name %s; got %q", name, err)
		}
	}
	if advisoryNames(c, "AGENT_GRPC_PLAINTEXT_OK") {
		t.Error("a refused boot must not also carry the plaintext advisory; one message, not two")
	}
}

func TestADeclaredPlaintextAgentChannelBootsWithOneAdvisory(t *testing.T) {
	c := plaintextProdConfig()
	c.GRPC.PlaintextOK = true
	if err := c.Validate(); err != nil {
		t.Fatalf("AGENT_GRPC_PLAINTEXT_OK=true must satisfy production: %v", err)
	}
	var count int
	for _, a := range c.Advisories() {
		if strings.Contains(a, "AGENT_GRPC_PLAINTEXT_OK") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one plaintext advisory, got %d: %v", count, c.Advisories())
	}
}

// The declaration is about the wire to the devices. Under certificate files
// or external termination that wire is TLS and the flag changes nothing, so
// it must say nothing either.
func TestThePlaintextDeclarationIsInertUnderTLS(t *testing.T) {
	c := baseProdConfig() // certificate files
	c.GRPC.PlaintextOK = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if advisoryNames(c, "AGENT_GRPC_PLAINTEXT_OK") {
		t.Error("an inert declaration under certificate files must not be reported")
	}
	c = plaintextProdConfig()
	c.GRPC.TLSTerminatedExternally = true
	c.GRPC.PlaintextOK = true
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if advisoryNames(c, "AGENT_GRPC_PLAINTEXT_OK") {
		t.Error("an inert declaration under external termination must not be reported")
	}
}

func TestPlaintextOKIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("AGENT_GRPC_PLAINTEXT_OK", "true")
	cfg, err := ApplyEnvironment(getDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GRPC.PlaintextOK {
		t.Fatal("AGENT_GRPC_PLAINTEXT_OK=true was not applied")
	}
	t.Setenv("AGENT_GRPC_PLAINTEXT_OK", "sure")
	cfg, err = ApplyEnvironment(getDefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPC.PlaintextOK {
		t.Fatal("an unparseable boolean must not declare plaintext acceptable")
	}
}
