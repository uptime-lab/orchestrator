package sql

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
)

func TestExtractCipherTokens(t *testing.T) {
	// Use a REAL ciphertext: base64 contains + / = , so a hand-written "AAAA"
	// token would not exercise the delimiter handling the parser must survive.
	enc := crypto.NewEncryptor("k")
	ct, _ := enc.Encrypt("some secret value long enough to base64 into + / = territory")
	tok := commandCipherPrefix + ct

	blob, _ := json.Marshal(map[string]string{"stdout": tok, "path": "/plain"})
	got := extractCipherTokens(string(blob))
	if len(got) != 1 || got[0] != tok {
		t.Fatalf("extractCipherTokens returned %v, want exactly [%s]", got, tok)
	}
	if len(extractCipherTokens(`{"path":"/no/cipher"}`)) != 0 {
		t.Error("a blob with no ciphertext must yield no tokens")
	}
	if len(extractCipherTokens("")) != 0 {
		t.Error("empty input must yield no tokens")
	}
}

// The rewrap must NEVER destroy a value it cannot decrypt. Reusing the model
// round-trip did exactly that (ToDomain drops undecryptable fields, FromDomain
// then persists their absence), silently deleting data during a rotation.
func TestRewrapTokensInNeverDestroysUndecryptableData(t *testing.T) {
	strangerKey := crypto.NewEncryptor("a-key-nobody-configured")
	orphan, _ := strangerKey.Encrypt("payload encrypted with an unknown key")
	blob := `{"stdout":"` + commandCipherPrefix + orphan + `","path":"/keep"}`

	enc := crypto.NewEncryptorWithFallbacks("primary", []string{"old"})

	out, changed, err := rewrapTokensIn(blob, enc)
	if err == nil {
		t.Fatal("expected an error for an undecryptable token; a silent skip would hide data at risk")
	}
	if changed {
		t.Error("changed must be false when the row could not be rewrapped")
	}
	if out != blob {
		t.Errorf("the blob must be returned UNMODIFIED on failure:\n got %s\nwant %s", out, blob)
	}
	if !strings.Contains(out, orphan) {
		t.Error("the original ciphertext must survive verbatim — losing it is unrecoverable")
	}
}

// A value already on the primary key must be left alone, so re-running the
// one-shot rewrap is free. This check was dead: the "encv1:" marker was handed
// to the crypto layer, which only understands "v1:", so every row looked
// un-migrated and was rewritten on every pass.
func TestRewrapTokensInIsIdempotent(t *testing.T) {
	enc := crypto.NewEncryptorWithFallbacks("primary", []string{"old"})
	ct, _ := enc.Encrypt("already current")
	blob := `{"stdout":"` + commandCipherPrefix + ct + `"}`

	out, changed, err := rewrapTokensIn(blob, enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("a blob already on the primary key must report changed=false")
	}
	if out != blob {
		t.Error("an unchanged blob must be byte-identical")
	}
}

// The real rotation shape: data written under the OLD key is re-encrypted under
// the new primary, surrounding JSON untouched, and afterwards the old key alone
// can no longer read it.
func TestRewrapTokensInMigratesOldKeyData(t *testing.T) {
	oldEnc := crypto.NewEncryptor("old")
	ct, _ := oldEnc.Encrypt("hunter2")
	blob := `{"stdout":"` + commandCipherPrefix + ct + `","exit_code":"0","path":"/etc/x"}`

	rotated := crypto.NewEncryptorWithFallbacks("new-primary", []string{"old"})
	out, changed, err := rewrapTokensIn(blob, rotated)
	if err != nil || !changed {
		t.Fatalf("expected a successful rewrap, got changed=%v err=%v", changed, err)
	}

	var parsed map[string]string
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("rewrap produced invalid JSON: %v", err)
	}
	if parsed["path"] != "/etc/x" || parsed["exit_code"] != "0" {
		t.Errorf("non-secret fields were altered: %v", parsed)
	}

	newTok := strings.TrimPrefix(parsed["stdout"], commandCipherPrefix)
	if got, err := rotated.Decrypt(newTok); err != nil || got != "hunter2" {
		t.Fatalf("rewrapped value did not round-trip: got %q err %v", got, err)
	}
	if _, err := oldEnc.Decrypt(newTok); err == nil {
		t.Error("the old key must NOT decrypt the rewrapped value")
	}
	if _, changedAgain, _ := rewrapTokensIn(out, rotated); changedAgain {
		t.Error("rewrap must be idempotent")
	}
}

// Repeated identical ciphertexts must all be migrated (the substitution is
// global) and the JSON must stay valid.
func TestRewrapTokensInHandlesRepeatedTokens(t *testing.T) {
	oldEnc := crypto.NewEncryptor("old")
	ct, _ := oldEnc.Encrypt("dup")
	tok := commandCipherPrefix + ct
	blob := `{"a":"` + tok + `","b":"` + tok + `"}`

	rotated := crypto.NewEncryptorWithFallbacks("new", []string{"old"})
	out, changed, err := rewrapTokensIn(blob, rotated)
	if err != nil || !changed {
		t.Fatalf("expected rewrap, got changed=%v err=%v", changed, err)
	}
	if strings.Contains(out, ct) {
		t.Error("every occurrence of the old ciphertext must be replaced")
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON after rewrap: %v", err)
	}
	if parsed["a"] != parsed["b"] {
		t.Error("both occurrences should end up identical")
	}
}
