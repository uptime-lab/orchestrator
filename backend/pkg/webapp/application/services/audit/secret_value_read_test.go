package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// Serving plaintext secret material has to leave a row.
//
// secret.read_value gated the plaintext and recorded nothing, so a holder could
// page the whole account's values, or replay any prior run_script result, and
// leave no trace — and the first question after a leak is "who saw this
// credential before it was rotated".
func TestServingPlaintextIsRecorded(t *testing.T) {
	recorder, sink := newTestRecorder()
	accountID, userID, secretID := uuid.New(), uuid.New(), uuid.New()

	recorder.RecordSecretValuesRead(context.Background(), UserActor(accountID, userID),
		ScopeSecret, audit_event.TargetSecret, secretID.String())

	if len(sink.events) != 1 {
		t.Fatalf("no row was written; %d events", len(sink.events))
	}
	event := sink.events[0]
	if event.Action != audit_event.ActionSecretValuesRead {
		t.Fatalf("action = %q, want %q", event.Action, audit_event.ActionSecretValuesRead)
	}
	if event.TargetID != secretID.String() {
		t.Errorf("target_id = %q, want the secret's id", event.TargetID)
	}
	if event.ActorUserID == nil || *event.ActorUserID != userID {
		t.Errorf("the row does not name the caller: %v", event.ActorUserID)
	}
}

// The detail must never carry credential material — not the value, and not the
// NAME either: a secret's name IS its {{secrets.<name>}} identifier, and
// audit_events is on no rewrap list, so anything copied there outlives a key
// rotation as plaintext nobody knows to look for.
func TestTheRowCarriesNoCredentialMaterial(t *testing.T) {
	recorder, sink := newTestRecorder()
	accountID, userID := uuid.New(), uuid.New()

	recorder.RecordSecretValuesRead(context.Background(), UserActor(accountID, userID),
		ScopeAccountSecrets, audit_event.TargetAccount, accountID.String())

	if len(sink.events) != 1 {
		t.Fatal("no row was written")
	}
	event := sink.events[0]
	for _, forbidden := range []string{"tskey-", "ssh-ed25519", "BEGIN ", "password", "token="} {
		if strings.Contains(strings.ToLower(event.Detail), strings.ToLower(forbidden)) {
			t.Errorf("the detail looks like it carries credential material: %q", event.Detail)
		}
	}
	if !strings.Contains(event.Detail, string(ScopeAccountSecrets)) {
		t.Errorf("detail = %q, want it to name the scope so a reader can tell a list from one secret", event.Detail)
	}
}

// A REFUSED read writes nothing: a caller who saw nothing did not read
// anything, and a row per refused request would bury the reads that matter.
func TestARefusalWritesNothing(t *testing.T) {
	recorder, sink := newTestRecorder()
	accountID, userID := uuid.New(), uuid.New()

	reader := NewSecretValueReader(
		func(context.Context, uuid.UUID, uuid.UUID) bool { return false },
		recorder,
	)
	if reader.Allowed(context.Background(), accountID, userID, ScopeSecret, audit_event.TargetSecret, "x") {
		t.Fatal("a refused caller was allowed")
	}
	if len(sink.events) != 0 {
		t.Fatalf("a refusal wrote %d rows", len(sink.events))
	}
}

// And an allowed one writes exactly one.
func TestAnAllowedReadWritesOneRow(t *testing.T) {
	recorder, sink := newTestRecorder()
	accountID, userID := uuid.New(), uuid.New()

	reader := NewSecretValueReader(
		func(context.Context, uuid.UUID, uuid.UUID) bool { return true },
		recorder,
	)
	if !reader.Allowed(context.Background(), accountID, userID, ScopeSecret, audit_event.TargetSecret, "x") {
		t.Fatal("an allowed caller was refused")
	}
	if len(sink.events) != 1 {
		t.Fatalf("%d rows written for one read, want 1", len(sink.events))
	}
}
