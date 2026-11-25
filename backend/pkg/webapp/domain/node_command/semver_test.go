package node_command

import "testing"

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.3.0", "0.3.0", 0},
		{"0.2.16", "0.3.0", -1},
		{"0.3.1", "0.3.0", 1},
		{"1.0.0", "0.9.9", 1},
		{"v0.3.0", "0.3.0", 0},
		{"0.3.0-rc1", "0.3.0", 0}, // pre-release suffix ignored
		{"0.3", "0.3.0", 0},       // missing patch == 0
	}
	for _, c := range cases {
		if got := CompareSemver(c.a, c.b); got != c.want {
			t.Errorf("CompareSemver(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestMeetsMinAgentVersion_FailClosed(t *testing.T) {
	min := "0.3.0"
	// Fail-closed on unknown/empty/latest/garbage.
	for _, v := range []string{"", "latest", "LATEST", "garbage", "dev"} {
		if MeetsMinAgentVersion(v, min) {
			t.Errorf("version %q must fail closed", v)
		}
	}
	// Too old.
	if MeetsMinAgentVersion("0.2.16", min) {
		t.Errorf("0.2.16 should not meet %s", min)
	}
	// Exactly meets and newer.
	if !MeetsMinAgentVersion("0.3.0", min) {
		t.Errorf("0.3.0 should meet %s", min)
	}
	if !MeetsMinAgentVersion("0.4.1", min) {
		t.Errorf("0.4.1 should meet %s", min)
	}
}

func TestQueueableAndMinVersionRegistries(t *testing.T) {
	if !IsQueueable(SlugApplyAccessConfig) {
		t.Error("apply_access_config must be queueable")
	}
	if IsQueueable(SlugReboot) {
		t.Error("reboot must not be queueable")
	}
	if IsQueueable(SlugInstallOS) {
		t.Error("install_os must not be queueable (signed URL expires)")
	}
	// 0.5.0, not 0.3.0: raised when the platform stopped sending cleartext
	// passwords in the access config. An agent below the floor reads only the
	// retired `password` field, so it would apply the config with NO password —
	// which turns a sudo user who should be prompted into NOPASSWD.
	if v, ok := MinAgentVersion(SlugApplyAccessConfig); !ok || v != "0.5.0" {
		t.Errorf("apply_access_config min version = %q,%v", v, ok)
	}
	if _, ok := MinAgentVersion(SlugReboot); ok {
		t.Error("reboot should impose no version floor")
	}
	// 0.4.6 is the release that added handlers.MaxResultOutputBytes on the
	// agent side. Below it a script result is unbounded and an oversized frame
	// tears down the node's stream; the floor is also what finally makes the
	// fail-closed comparison (empty/"latest"/garbage refused) run for
	// run_script at all. Lowering or removing it lets an agent with an
	// unbounded script result through again.
	if v, ok := MinAgentVersion(SlugRunScript); !ok || v != "0.4.6" {
		t.Errorf("run_script min version = %q,%v; want 0.4.6", v, ok)
	}
}
