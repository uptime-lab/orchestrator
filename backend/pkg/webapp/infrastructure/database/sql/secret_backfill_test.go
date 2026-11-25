package sql

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/google/uuid"
)

// Encryption at rest must NOT be a field whitelist. It was one, and it was
// inherently incomplete: a {{secrets.*}} reference resolves into whatever param
// the operator authored it into, so dest_path or a package spec reached the
// column in the clear while script/content did not. This
// pins the property that replaced the list — every string value, at any depth,
// whatever its key.
func TestEveryPayloadValueIsEncryptedWhateverTheKey(t *testing.T) {
	enc := reverseEncryptor{}
	payload := map[string]interface{}{
		// Params no whitelist would ever have listed.
		"dest_path": "/srv/deploy/hunter2/artifact",
		"packages":  []interface{}{"acme-agent=1.2.3+hunter2"},
		"nested":    map[string]interface{}{"owner": "svc-hunter2"},
		// Structural values must survive as themselves or the command breaks.
		"reboot":  true,
		"version": float64(3),
	}
	stored, err := encryptCommandPayload(payload, enc, true)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	b, _ := json.Marshal(stored)
	if strings.Contains(string(b), "hunter2") {
		t.Fatalf("secret material reached storage in the clear: %s", b)
	}
	if stored["reboot"] != true || stored["version"] != float64(3) {
		t.Errorf("non-string values must pass through unchanged: %v", stored)
	}
	// Key names stay readable so a row is still recognisable in psql.
	if !strings.Contains(string(b), "dest_path") {
		t.Errorf("key names must stay plaintext, got %s", b)
	}

	back, _ := decryptCommandPayload(stored, enc)
	if !reflect.DeepEqual(back, payload) {
		t.Errorf("payload did not round-trip:\n got %#v\nwant %#v", back, payload)
	}
}

// The redaction whitelist is now a floor, not the whole coverage, but the
// credential-by-nature params must never fall off it.
func TestRedactionFloorCoversCredentialParams(t *testing.T) {
	for _, f := range []string{"headers", "unit_content", "url", "password", "ssh_key"} {
		found := false
		for _, have := range results.SecretBearingPayloadFields {
			if have == f {
				found = true
			}
		}
		if !found {
			t.Errorf("param %q must always be redacted", f)
		}
	}
}

// The reserved key holds param NAMES, and redaction depends on reading it back
// even from a row whose values are on an old key.
func TestSecretDerivedParamsKeyStaysPlaintext(t *testing.T) {
	enc := reverseEncryptor{}
	stored, err := encryptCommandPayload(map[string]interface{}{
		results.SecretDerivedParamsKey: []interface{}{"dest_path"},
		"dest_path":                    "/srv/hunter2",
	}, enc, true)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	got := results.SecretDerivedParams(stored)
	if len(got) != 1 || got[0] != "dest_path" {
		t.Fatalf("redaction bookkeeping must survive encryption unchanged, got %v", got)
	}
}

func TestEncryptionFailureRejectsTheWholePayload(t *testing.T) {
	payload := map[string]interface{}{"target_device": "/dev/sda", "script": "echo hi"}
	if _, err := encryptCommandPayload(payload, reverseEncryptor{failOn: "echo hi"}, true); err == nil {
		t.Fatal("a failing encryptor must fail the whole payload, not silently drop the field: " +
			"a command missing target_device would run against the wrong disk")
	}
}

func TestGraphStateOutputsRoundTrip(t *testing.T) {
	enc := reverseEncryptor{}
	cmdID := uuid.New()
	state := map[string]workflow_execution_node.GraphNodeState{
		"blk1": {
			Status:    workflow_execution_node.GraphNodeCompleted,
			CommandID: &cmdID,
			Outputs:   map[string]string{"stdout": "root:x:0:0", "exit_code": "0"},
		},
		"blk2": {Status: workflow_execution_node.GraphNodeSkipped},
	}

	stored := encryptGraphStateValues(state, enc, true)
	b, _ := json.Marshal(stored)
	if strings.Contains(string(b), "root:x:0:0") {
		t.Fatalf("block output reached storage in the clear: %s", b)
	}
	// Structure stays queryable plaintext.
	if !strings.Contains(string(b), string(workflow_execution_node.GraphNodeCompleted)) {
		t.Errorf("statuses must stay plaintext, got %s", b)
	}

	back := decryptGraphStateValues(stored, enc)
	if back["blk1"].Outputs["stdout"] != "root:x:0:0" {
		t.Errorf("output did not round-trip: %v", back["blk1"].Outputs)
	}
}

func TestGraphStateLegacyPlaintextPassesThrough(t *testing.T) {
	state := map[string]workflow_execution_node.GraphNodeState{
		"blk1": {Outputs: map[string]string{"stdout": "legacy"}},
	}
	back := decryptGraphStateValues(state, reverseEncryptor{})
	if back["blk1"].Outputs["stdout"] != "legacy" {
		t.Errorf("legacy plaintext must pass through, got %v", back["blk1"].Outputs)
	}
}

func TestBackfillCommandRowDecision(t *testing.T) {
	enc := reverseEncryptor{}

	t.Run("legacy plaintext secret field is rewritten", func(t *testing.T) {
		legacy := `{"script":"echo hunter2","target_device":"/dev/sda"}`
		newPayload, _, _, changed := backfillCommandRow(&legacy, nil, nil, enc)
		if !changed {
			t.Fatal("legacy plaintext row must be rewritten")
		}
		if strings.Contains(*newPayload, "hunter2") {
			t.Errorf("backfill left plaintext behind: %s", *newPayload)
		}
	})

	t.Run("already-encrypted row is untouched", func(t *testing.T) {
		payload, err := encryptCommandPayload(map[string]interface{}{"script": "echo x"}, enc, true)
		if err != nil {
			t.Fatalf("encrypt failed: %v", err)
		}
		encrypted, _ := json.Marshal(payload)
		s := string(encrypted)
		_, _, _, changed := backfillCommandRow(&s, nil, nil, enc)
		if changed {
			t.Error("fully encrypted row must not be rewritten (idempotency)")
		}
	})

	t.Run("row the old whitelist skipped is now rewritten", func(t *testing.T) {
		// dest_path was never on the whitelist, so a secret resolved into it
		// would survive a whitelist-based backfill in the clear. This sweep catches it.
		legacy := `{"dest_path":"/srv/hunter2","url":"encv1:already"}`
		newPayload, _, _, changed := backfillCommandRow(&legacy, nil, nil, enc)
		if !changed {
			t.Fatal("a row with any plaintext value must be rewritten")
		}
		if strings.Contains(*newPayload, "hunter2") {
			t.Errorf("backfill left plaintext behind: %s", *newPayload)
		}
	})

	t.Run("legacy plaintext result is rewritten", func(t *testing.T) {
		_, newResult, _, changed := backfillCommandRow(nil, []byte(`{"stdout":"root:x:0:0"}`), nil, enc)
		if !changed {
			t.Fatal("legacy plaintext result must be rewritten")
		}
		if strings.Contains(string(newResult), "root:x:0:0") {
			t.Errorf("backfill left plaintext result behind: %s", newResult)
		}
	})
}

func TestBackfillGraphStateRowDecision(t *testing.T) {
	enc := reverseEncryptor{}

	legacy, _ := json.Marshal(map[string]workflow_execution_node.GraphNodeState{
		"blk1": {Outputs: map[string]string{"stdout": "hunter2"}},
	})
	newState, changed := backfillGraphStateRow(legacy, enc)
	if !changed {
		t.Fatal("legacy plaintext outputs must be rewritten")
	}
	if strings.Contains(string(newState), "hunter2") {
		t.Errorf("backfill left plaintext outputs behind: %s", newState)
	}

	// Second pass over its own output: no further change (idempotency).
	if _, changedAgain := backfillGraphStateRow(newState, enc); changedAgain {
		t.Error("backfill must be idempotent")
	}

	// Outputs-free state untouched.
	bare, _ := json.Marshal(map[string]workflow_execution_node.GraphNodeState{
		"blk1": {Status: workflow_execution_node.GraphNodeSkipped},
	})
	if _, ch := backfillGraphStateRow(bare, enc); ch {
		t.Error("state without outputs must not be rewritten")
	}
}

// Error text is the same agent bytes as an output, arriving by the other door:
// an agent echoes params back when something fails, so a resolved {{secrets.*}}
// lands there just as readily. It used to sit in the clear beside a sealed
// Outputs, which put it in the database dump and in interpolated SQL logs.
func TestGraphStateErrorTextIsSealedAtRest(t *testing.T) {
	enc := reverseEncryptor{}
	state := map[string]workflow_execution_node.GraphNodeState{
		"boom": {
			Status:  workflow_execution_node.GraphNodeFailed,
			Error:   "curl: (22) https://user:hunter2@repo/ returned 401",
			Outputs: map[string]string{"stdout": "nothing"},
		},
	}

	stored := encryptGraphStateValues(state, enc, false)
	if got := stored["boom"].Error; !strings.HasPrefix(got, commandCipherPrefix) {
		t.Fatalf("error text stored as %q, want it sealed", got)
	}
	if strings.Contains(stored["boom"].Error, "hunter2") {
		t.Fatal("the credential survived in the stored error text")
	}
	// The structure stays queryable: only the authored strings are opaque.
	if stored["boom"].Status != workflow_execution_node.GraphNodeFailed {
		t.Fatal("the status was not left plaintext")
	}

	back := decryptGraphStateValues(stored, enc)
	if back["boom"].Error != "curl: (22) https://user:hunter2@repo/ returned 401" {
		t.Fatalf("error text came back as %q", back["boom"].Error)
	}
}

// A row written before this existed carries plaintext with no prefix, and must
// read back unchanged rather than being dropped as undecryptable.
func TestLegacyPlaintextErrorReadsBackUnchanged(t *testing.T) {
	state := map[string]workflow_execution_node.GraphNodeState{
		"boom": {Error: "node offline"},
	}
	back := decryptGraphStateValues(state, reverseEncryptor{})
	if back["boom"].Error != "node offline" {
		t.Fatalf("legacy error text = %q, want it untouched", back["boom"].Error)
	}
}

// node_commands.details is the agent's own prose about a failure, and an agent
// echoes params back — so a resolved {{secrets.*}} reaches it as readily as it
// reaches an output. It sat in the clear beside a sealed payload.
func TestCommandDetailsAreSealedAtRest(t *testing.T) {
	enc := reverseEncryptor{}
	const text = "curl: (22) https://user:hunter2@repo/ returned 401"

	sealed, ok := encryptCommandDetails(text, enc, false)
	if !ok {
		t.Fatal("details could not be sealed")
	}
	if !strings.HasPrefix(sealed, commandCipherPrefix) {
		t.Fatalf("details stored as %q, want the encv1: marker — the rewrap finds it by that prefix", sealed)
	}
	if strings.Contains(sealed, "hunter2") {
		t.Fatal("the credential survived in the stored details")
	}
	if back := decryptCommandDetails(sealed, enc); back != text {
		t.Fatalf("details came back as %q", back)
	}
}

// A row written before this existed is plaintext with no prefix and must read
// back unchanged.
func TestLegacyPlaintextDetailsReadBackUnchanged(t *testing.T) {
	if got := decryptCommandDetails("node offline", reverseEncryptor{}); got != "node offline" {
		t.Fatalf("legacy details = %q, want it untouched", got)
	}
}

// The backfill seals a legacy row once and then leaves it alone — including a
// value it cannot decrypt, which belongs to the rewrap machinery and must never
// be wrapped a second time.
func TestBackfillSealsDetailsIdempotently(t *testing.T) {
	enc := reverseEncryptor{}
	legacy := "node offline"

	_, _, sealed, changed := backfillCommandRow(nil, nil, &legacy, enc)
	if !changed || sealed == nil || !strings.HasPrefix(*sealed, commandCipherPrefix) {
		t.Fatalf("legacy details not sealed: %v changed=%v", sealed, changed)
	}
	if _, _, _, changedAgain := backfillCommandRow(nil, nil, sealed, enc); changedAgain {
		t.Fatal("the sweep re-wrapped its own ciphertext; a double wrap is invisible until a rotation")
	}
}

// The two legacy populations the Outputs-only predicate skipped forever:
// an error-only row — the common shape of a block that failed
// before producing outputs — and a row written between the Outputs-sealing and
// Error-sealing changes, which carries sealed outputs beside a plaintext error.
// Neither carries an encv1: token in the clear field, so the rewrap machinery
// cannot see them either; this sweep is their only path to ciphertext.
func TestBackfillSealsErrorOnlyRows(t *testing.T) {
	enc := reverseEncryptor{}

	t.Run("error-only row is rewritten", func(t *testing.T) {
		legacy, _ := json.Marshal(map[string]workflow_execution_node.GraphNodeState{
			"boom": {
				Status: workflow_execution_node.GraphNodeFailed,
				Error:  "curl: (22) https://user:hunter2@repo/ returned 401",
			},
		})
		newState, changed := backfillGraphStateRow(legacy, enc)
		if !changed {
			t.Fatal("an error-only row with plaintext credentials was left in the clear")
		}
		if strings.Contains(string(newState), "hunter2") {
			t.Fatalf("the credential survived the sweep: %s", newState)
		}
	})

	t.Run("sealed outputs beside a plaintext error are rewritten", func(t *testing.T) {
		sealed := encryptGraphStateValues(map[string]workflow_execution_node.GraphNodeState{
			"blk": {Outputs: map[string]string{"stdout": "ok"}},
		}, enc, true)
		st := sealed["blk"]
		st.Error = "token=hunter2 rejected"
		sealed["blk"] = st
		legacy, _ := json.Marshal(sealed)

		newState, changed := backfillGraphStateRow(legacy, enc)
		if !changed {
			t.Fatal("a row with sealed outputs and a plaintext error was left in the clear")
		}
		if strings.Contains(string(newState), "hunter2") {
			t.Fatalf("the credential survived the sweep: %s", newState)
		}
	})

	// Idempotency is what makes an every-boot sweep free: a fully sealed row —
	// outputs AND error — must not be rewritten again.
	t.Run("fully sealed row is untouched", func(t *testing.T) {
		sealed, _ := json.Marshal(encryptGraphStateValues(map[string]workflow_execution_node.GraphNodeState{
			"blk": {
				Error:   "node offline",
				Outputs: map[string]string{"stdout": "ok"},
			},
		}, enc, false))
		if _, changed := backfillGraphStateRow(sealed, enc); changed {
			t.Fatal("the sweep re-wrapped its own ciphertext; a double wrap is invisible until a rotation")
		}
	})
}
