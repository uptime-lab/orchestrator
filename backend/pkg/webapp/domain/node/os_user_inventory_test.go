package node

import "testing"

const sampleScan = `root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
orchestrator:x:1000:1000::/srv/orchestrator:/bin/bash
alice:x:1001:1001::/srv/alice:/bin/bash
svcbot:x:1002:1002::/var/lib/svcbot:/usr/sbin/nologin
---ORCH-MANAGED---
orchestrator-managed:x:1500:alice,orchestrator
`

func TestParseOSUserInventory(t *testing.T) {
	users := ParseOSUserInventory(sampleScan)

	byName := map[string]OSUser{}
	for _, u := range users {
		byName[u.Username] = u
	}

	if _, ok := byName["root"]; ok {
		t.Error("system account root (uid 0) must be filtered out")
	}
	if _, ok := byName["daemon"]; ok {
		t.Error("service account daemon (uid < 1000) must be filtered out")
	}
	if _, ok := byName["svcbot"]; ok {
		t.Error("nologin account svcbot must be filtered out")
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 login accounts (orchestrator, alice), got %d: %+v", len(users), users)
	}

	if !byName["alice"].Managed {
		t.Error("alice is in orchestrator-managed and must be tagged managed")
	}
	if !byName["orchestrator"].Managed {
		t.Error("orchestrator is in orchestrator-managed and must be tagged managed")
	}
	if got := byName["alice"]; got.UID != 1001 || got.Home != "/srv/alice" || got.Shell != "/bin/bash" {
		t.Errorf("alice parsed wrong: %+v", got)
	}
}

func TestParseOSUserInventoryUnmanaged(t *testing.T) {
	// A hand-added account not in the managed group.
	scan := `bob:x:1003:1003::/srv/bob:/bin/bash
---ORCH-MANAGED---
orchestrator-managed:x:1500:
`
	users := ParseOSUserInventory(scan)
	if len(users) != 1 || users[0].Username != "bob" || users[0].Managed {
		t.Fatalf("expected one unmanaged user bob, got %+v", users)
	}
}

func TestParseOSUserInventoryNoGroup(t *testing.T) {
	// The managed group may not exist yet (pre-access-config node).
	scan := `carol:x:1004:1004::/srv/carol:/bin/bash`
	users := ParseOSUserInventory(scan)
	if len(users) != 1 || users[0].Username != "carol" || users[0].Managed {
		t.Fatalf("expected one unmanaged user carol, got %+v", users)
	}
}

func TestParseOSUserInventoryMalformedLinesSkipped(t *testing.T) {
	scan := `garbage
:::::::
dave:x:notanumber:1005::/srv/dave:/bin/bash
erin:x:1006:1006::/srv/erin:/bin/bash`
	users := ParseOSUserInventory(scan)
	if len(users) != 1 || users[0].Username != "erin" {
		t.Fatalf("expected only erin, got %+v", users)
	}
}

func TestNormalizeBootloaderVersion(t *testing.T) {
	cases := map[string]string{
		"f68405bcf704b03511bf1656e86924e8cd6398b4 (release)": "f68405bcf704b03511bf1656e86924e8cd6398b4",
		"f68405bc":  "f68405bc",
		"  abc def": "abc",
		"":          "",
	}
	for in, want := range cases {
		if got := NormalizeBootloaderVersion(in); got != want {
			t.Errorf("NormalizeBootloaderVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
