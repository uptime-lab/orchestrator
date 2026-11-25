package sql

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/google/uuid"
)

// reverseEncryptor is a stand-in cipher: reversible, obviously not the stored
// form, and it needs no key material in the test.
type reverseEncryptor struct{ failOn string }

func (e reverseEncryptor) Encrypt(plaintext string) (string, error) {
	if e.failOn != "" && plaintext == e.failOn {
		return "", errFake
	}
	return reverse(plaintext), nil
}

func (e reverseEncryptor) Decrypt(ciphertext string) (string, error) {
	return reverse(ciphertext), nil
}

var errFake = &fakeError{}

type fakeError struct{}

func (*fakeError) Error() string { return "encrypt failed" }

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func sampleCommand() *node_command.NodeCommand {
	return &node_command.NodeCommand{
		ID:        uuid.New(),
		NodeID:    uuid.New(),
		AccountID: uuid.New(),
		Slug:      "run_script",
		Payload: map[string]interface{}{
			"script":        "echo hunter2",
			"target_device": "/dev/nvme0n1",
		},
		Result: map[string]string{"content": "root:x:0:0"},
	}
}

func TestSecretBearingFieldsAreNotStoredInTheClear(t *testing.T) {
	enc := reverseEncryptor{}
	var model NodeCommandModel
	model.FromDomain(sampleCommand(), enc)

	if model.Payload == nil {
		t.Fatal("expected a serialized payload")
	}
	if strings.Contains(*model.Payload, "hunter2") {
		t.Errorf("secret material reached storage in the clear: %s", *model.Payload)
	}
	if strings.Contains(string(model.Result), "root:x:0:0") {
		t.Errorf("agent output reached storage in the clear: %s", model.Result)
	}

	// Values are opaque whatever the key — the whitelist that used to spare
	// "non-secret" fields is what let secrets resolved into them leak. Key
	// names stay plaintext so the row is still recognisable in psql, and the
	// column is still valid JSON.
	var stored map[string]interface{}
	if err := json.Unmarshal([]byte(*model.Payload), &stored); err != nil {
		t.Fatalf("stored payload is not valid JSON: %v", err)
	}
	if stored["target_device"] == "/dev/nvme0n1" {
		t.Error("every value is encrypted now, including ones no whitelist would have listed")
	}
	if _, ok := stored["target_device"]; !ok {
		t.Error("key names must stay plaintext")
	}
	if got := model.ToDomain(enc); got.Payload["target_device"] != "/dev/nvme0n1" {
		t.Errorf("target_device did not round-trip: %v", got.Payload["target_device"])
	}
}

func TestRoundTripRestoresPlaintext(t *testing.T) {
	enc := reverseEncryptor{}
	var model NodeCommandModel
	model.FromDomain(sampleCommand(), enc)

	got := model.ToDomain(enc)

	if got.Payload["script"] != "echo hunter2" {
		t.Errorf("script did not round-trip: %v", got.Payload["script"])
	}
	if got.Result["content"] != "root:x:0:0" {
		t.Errorf("result did not round-trip: %v", got.Result["content"])
	}
}

// Rows written before encryption existed carry no prefix. They must keep
// working, so that shipping this needs no backfill.
func TestLegacyPlaintextRowsStillRead(t *testing.T) {
	legacy := `{"script":"echo legacy","target_device":"/dev/sda"}`
	model := NodeCommandModel{Payload: &legacy}

	got := model.ToDomain(reverseEncryptor{})

	if got.Payload["script"] != "echo legacy" {
		t.Errorf("legacy plaintext must pass through untouched, got %v", got.Payload["script"])
	}
}

// A field that cannot be encrypted must be dropped, never written in the clear:
// storing the plaintext is the exact outcome this encryption exists to prevent.
// An encryption failure drops the WHOLE payload, not the offending field. The
// per-field drop it replaced could strip target_device from an erase_drive and
// leave a command that still ran, against whatever the agent defaulted to.
func TestUnencryptablePayloadIsDroppedWholeRatherThanStoredPlainly(t *testing.T) {
	var model NodeCommandModel
	model.FromDomain(sampleCommand(), reverseEncryptor{failOn: "echo hunter2"})

	if model.Payload != nil {
		t.Fatalf("payload must be dropped entirely when a field cannot be encrypted, got %s", *model.Payload)
	}
}

// SetResult writes the column directly instead of going through FromDomain, and
// it is THE path agent outputs take. A regression there is invisible — the read
// path passes unprefixed values through — so it gets its own check.
func TestSetResultEncryptsAgentOutputs(t *testing.T) {
	enc := reverseEncryptor{}

	encrypted, err := encryptCommandResult(map[string]string{"content": "root:x:0:0"}, enc, false)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	if encrypted["content"] == "root:x:0:0" {
		t.Fatal("agent output must not be persisted in the clear")
	}
	if got := decryptCommandResult(encrypted, enc); got["content"] != "root:x:0:0" {
		t.Errorf("round trip failed: %v", got["content"])
	}
}

// A legitimate value that BEGINS with the cipher token — a file
// whose stored bytes start "encv1:", a script echoing one — used to be skipped
// by the write-path encrypt on the prefix alone: stored in the CLEAR, and then
// dropped on every read because it does not decrypt. The write paths now
// encrypt everything; only the backfill, which sweeps raw rows where the
// prefix really does mark our own ciphertext, preserves prefixed values.
func TestPrefixedPlaintextIsEncryptedOnWriteAndSurvivesRoundTrip(t *testing.T) {
	enc := reverseEncryptor{}
	tricky := commandCipherPrefix + "not actually ciphertext"

	// Result write path (agent outputs).
	encryptedResult, err := encryptCommandResult(map[string]string{"content": tricky}, enc, false)
	if err != nil {
		t.Fatalf("encrypt result: %v", err)
	}
	if encryptedResult["content"] == tricky {
		t.Fatal("prefixed plaintext was stored in the clear on the result path")
	}
	if got := decryptCommandResult(encryptedResult, enc); got["content"] != tricky {
		t.Fatalf("result round trip lost the value: %q", got["content"])
	}

	// Payload write path.
	encryptedPayload, err := encryptCommandPayload(map[string]interface{}{"script": tricky}, enc, false)
	if err != nil {
		t.Fatalf("encrypt payload: %v", err)
	}
	if encryptedPayload["script"] == tricky {
		t.Fatal("prefixed plaintext was stored in the clear on the payload path")
	}
	if got, _ := decryptCommandPayload(encryptedPayload, enc); got["script"] != tricky {
		t.Fatalf("payload round trip lost the value: %v", got["script"])
	}

	// The backfill sweeps RAW rows: there the prefix marks ciphertext this
	// package already wrote — possibly under a dropped key — and must be left
	// verbatim for the rewrap machinery, never re-wrapped.
	preserved, err := encryptCommandResult(map[string]string{"content": tricky}, enc, true)
	if err != nil {
		t.Fatalf("backfill encrypt: %v", err)
	}
	if preserved["content"] != tricky {
		t.Fatal("the backfill must leave prefixed rows verbatim")
	}
}

// A payload field the current key cannot read must be REPORTED, not merely
// dropped.
//
// Dropping it is right for display — ciphertext tells an operator nothing — and
// catastrophic for dispatch: a queued apply_access_config waiting for an offline
// node, sent after SECRETS_ENCRYPTION_KEY_OLD was removed, would go out missing
// users or keys and settle SUCCESS. A revocation turned into a grant, with the
// device convinced it is converged.
func TestAnUndecryptablePayloadFieldIsNamed(t *testing.T) {
	enc := reverseEncryptor{}
	payload := map[string]interface{}{
		"users":  []interface{}{"ops"},
		"script": "echo hi",
	}
	encrypted, err := encryptCommandPayload(payload, enc, false)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// The key that sealed it is gone.
	got, dropped := decryptCommandPayload(encrypted, &alwaysFailingDecryptor{})
	if len(dropped) == 0 {
		t.Fatal("nothing was reported as undecryptable, so a partial payload would be dispatched as if it were whole")
	}
	for _, k := range dropped {
		if _, present := got[k]; present {
			t.Errorf("%q was reported as dropped but is still in the payload", k)
		}
	}
}

// alwaysFailingDecryptor stands in for "the key that sealed this is gone".
type alwaysFailingDecryptor struct{}

func (alwaysFailingDecryptor) Encrypt(v string) (string, error) { return v, nil }
func (alwaysFailingDecryptor) Decrypt(string) (string, error)   { return "", errFake }
