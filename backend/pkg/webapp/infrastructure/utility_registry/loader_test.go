package utility_registry

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// TestLoad is the CI half of the registry contract: the same Load that is
// fatal at boot must pass here, so a bad entry breaks the build before it can
// break a deploy.
func TestLoad(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("registry failed to load: %v", err)
	}
	docker, ok := catalog.Get("docker")
	if !ok {
		t.Fatal("the docker utility must exist in the phase-1 catalog")
	}
	if docker.Class != utility.ClassReconciled {
		t.Fatalf("docker is a reconciled utility, got %q", docker.Class)
	}
	// The log caps are default-ON — shipping them as a buried option would
	// contradict the fleet's own SD-wear story (docs/UTILITIES-PLAN.md §5.1).
	cfg := docker.ResolvedConfig(nil)
	if cfg["log_max_size"] != "10m" {
		t.Fatalf("log_max_size must default on, got %v", cfg["log_max_size"])
	}
	if _, ok := cfg["log_max_file"]; !ok {
		t.Fatal("log_max_file must default on")
	}
}

// TestTailscaleFloor pins the per-utility agent floor: the applier ships in
// the release after 0.6.10, so dropping the floor would dispatch a set every
// fielded agent refuses per-utility instead of skipping with the remedy named.
func TestTailscaleFloor(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("registry failed to load: %v", err)
	}
	ts, ok := catalog.Get("tailscale")
	if !ok {
		t.Fatal("the tailscale utility must exist in the catalog")
	}
	if ts.MinAgentVersion == "" {
		t.Fatal("tailscale must carry a min_agent_version — its applier is not in any fielded agent")
	}
	if !ts.Params[0].Required || ts.Params[0].Name != "auth_key" {
		t.Fatalf("auth_key must be the required first param, got %+v", ts.Params[0])
	}
}

// A value copied from a CLI example ("auth-key=tskey-...") must be refused at
// save, not first in the DEVICE's failure report. The pattern moves that
// refusal to save time — for literals only, because a
// {{secrets.*}} reference resolves at apply and the agent's own check stays
// the backstop there.
func TestTailscaleAuthKeyPatternRefusesCLIPaste(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatalf("registry failed to load: %v", err)
	}
	ts, _ := catalog.Get("tailscale")

	base := map[string]interface{}{"auth_key": "tskey-auth-abc123"}
	if err := ts.ValidateConfig(base); err != nil {
		t.Fatalf("a bare tskey- literal must validate, got: %v", err)
	}
	for _, bad := range []string{
		"auth-key=tskey-auth-abc123",
		"--authkey=tskey-auth-abc123",
		"key tskey-auth-abc123",
	} {
		if err := ts.ValidateConfig(map[string]interface{}{"auth_key": bad}); err == nil {
			t.Errorf("value %q must be refused at save", bad)
		} else if !strings.Contains(err.Error(), "auth-key= prefix") {
			t.Errorf("the refusal for %q must carry the operator hint, got: %v", bad, err)
		}
	}
	if err := ts.ValidateConfig(map[string]interface{}{"auth_key": "{{secrets.ts_key}}"}); err != nil {
		t.Errorf("a secret reference must skip the pattern (resolved at apply), got: %v", err)
	}
}
