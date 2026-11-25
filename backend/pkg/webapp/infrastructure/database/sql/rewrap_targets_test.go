package sql

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
)

// The inventory of at-rest ciphertext, written out so a change has to be
// deliberate. Adding an encrypted column without adding it here fails this test;
// adding it here without wiring it fails the next one.
var expectedEncryptedTargets = []string{
	"secrets.value_encrypted",
	"access_entries.password_encrypted",
	"ssh_keys.private_key_encrypted",
	"two_factors.secret",
	"nodes.default_password",
	"organizational_units.default_password",
	"account_policies.default_password",
	"master_tokens.default_password",
	"nodes.unique_token",
	"node_tombstones.unique_token",
	"node_files.content",
	"node_commands.payload/result/details",
	"workflow_execution_nodes.graph_state",
}

// The strongest form available: the rewrap and its verifier do not HAVE two
// lists to compare, they walk one. What remains checkable is that the one list
// is complete — two lists can drift apart, and the all-clear from a shorter list is the line an operator acts on when deciding to drop a key.
func TestTheEncryptedTargetInventoryIsComplete(t *testing.T) {
	targets := encryptedTargets()

	if len(targets) != len(expectedEncryptedTargets) {
		t.Fatalf("the inventory has %d targets, expected %d — if an encrypted column was added or removed, update expectedEncryptedTargets deliberately",
			len(targets), len(expectedEncryptedTargets))
	}
	for i, want := range expectedEncryptedTargets {
		if targets[i].Label != want {
			t.Errorf("target %d is %q, want %q", i, targets[i].Label, want)
		}
	}
}

// Both operations must exist for every target. A nil Coverage is exactly the old
// bug — a target the rewrap moves and the verifier never looks at.
func TestEveryTargetCarriesBothOperations(t *testing.T) {
	for _, target := range encryptedTargets() {
		if target.Rewrap == nil {
			t.Errorf("%s has no rewrap", target.Label)
		}
		if target.Coverage == nil {
			t.Errorf("%s has no coverage check — this is the drift the single list exists to prevent", target.Label)
		}
	}
}

// The two jsonb blobs are the ones the verifier used to miss. Naming them
// explicitly means a refactor that drops them again fails here rather than in a
// rotation months later.
func TestTheJSONBTargetsAreCovered(t *testing.T) {
	labels := map[string]bool{}
	for _, target := range encryptedTargets() {
		labels[target.Label] = true
	}
	for _, required := range []string{"node_commands.payload/result/details", "workflow_execution_nodes.graph_state"} {
		if !labels[required] {
			t.Errorf("%s is missing from the inventory; a rotation could report the old key as droppable while queued command payloads still need it", required)
		}
	}
}

func TestKeyCoverageIsCleanOnlyWhenEverythingIsOnThePrimaryKey(t *testing.T) {
	cases := []struct {
		name   string
		report []KeyCoverage
		clean  bool
	}{
		{"all on primary", []KeyCoverage{{OnPrimary: 12}}, true},
		{"nothing encrypted at all", []KeyCoverage{{}}, true},
		{"one row still on the old key", []KeyCoverage{{OnPrimary: 12, OnOldKey: 1}}, false},
		{"one unreadable row", []KeyCoverage{{OnPrimary: 12, Unreadable: 1}}, false},
		{"a failed query", []KeyCoverage{{Unreadable: -1}}, false},
	}
	for _, tc := range cases {
		if got := KeyCoverageIsClean(tc.report); got != tc.clean {
			t.Errorf("%s: KeyCoverageIsClean = %v, want %v", tc.name, got, tc.clean)
		}
	}
}

// A sampled report must never say the old key can be dropped: the value still on
// the old key is exactly the one the sample did not look at.
func TestASampledReportIsNeverClean(t *testing.T) {
	report := []KeyCoverage{{Target: "node_commands.payload/result", OnPrimary: 500, Truncated: true}}

	if KeyCoverageIsClean(report) {
		t.Fatal("a truncated report claimed the old key can be dropped")
	}
	if !strings.Contains(FormatKeyCoverage(report), "sampled") {
		t.Errorf("the rendered report does not disclose that it is a sample: %s", FormatKeyCoverage(report))
	}
}

// The wrong-key canary runs on a sample, so it must key off "unreadable by any
// key" rather than "clean" — otherwise it would fire on every boot of a healthy
// install, spending the one alarm that catches a restore with the wrong key.
func TestTheWrongKeySignalIsIndependentOfSampling(t *testing.T) {
	healthySample := []KeyCoverage{{OnPrimary: 500, Truncated: true}}
	if KeyCoverageHasUnreadable(healthySample) {
		t.Error("a healthy sampled report must not raise the wrong-key alarm")
	}

	wrongKey := []KeyCoverage{{Unreadable: 500, Truncated: true}}
	if !KeyCoverageHasUnreadable(wrongKey) {
		t.Error("a sample full of unreadable values must raise the wrong-key alarm")
	}

	// A failed query reports -1, which is "unknown", not "unreadable": it must
	// not raise the alarm either.
	unknown := []KeyCoverage{{Unreadable: -1}}
	if KeyCoverageHasUnreadable(unknown) {
		t.Error("a failed query must not be reported as a wrong key")
	}
}

// classify is the whole verdict in three lines, and getting it wrong is what
// "the old key can be dropped" would be built on.
func TestClassifyPutsEachValueUnderTheRightKey(t *testing.T) {
	rotated := crypto.NewEncryptorWithFallbacks("new-primary", []string{"old"})

	onPrimary, err := rotated.Encrypt("current")
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	onOldKey, err := crypto.NewEncryptor("old").Encrypt("legacy")
	if err != nil {
		t.Fatalf("encrypting with the old key: %v", err)
	}
	orphan, err := crypto.NewEncryptor("a-key-nobody-configured").Encrypt("lost")
	if err != nil {
		t.Fatalf("encrypting with a stranger key: %v", err)
	}

	var cov KeyCoverage
	classify(rotated, onPrimary, &cov)
	classify(rotated, onOldKey, &cov)
	classify(rotated, orphan, &cov)

	if cov.OnPrimary != 1 || cov.OnOldKey != 1 || cov.Unreadable != 1 {
		t.Fatalf("classified primary=%d old=%d unreadable=%d, want 1/1/1",
			cov.OnPrimary, cov.OnOldKey, cov.Unreadable)
	}
}
