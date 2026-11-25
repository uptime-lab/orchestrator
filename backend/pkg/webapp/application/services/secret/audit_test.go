package secret

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/secret_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// The rule these tests enforce: a secret's NAME and KIND are identifiers and
// belong in the trail (the name is the {{secrets.<name>}} reference operators
// write); its VALUE never does. audit_events is not on any rewrap list, so a
// value copied there would outlive a key rotation as unreadable-but-present
// plaintext nobody knew to look for.

const auditSecretValue = "fake-secret-value-for-audit-test"

func auditService() (*Service, *audittest.Sink) {
	recorder, sink := audittest.New()
	return NewService(newFakeSecretRepo(), &fakeAttachmentRepo{},
		fakeNodeRepoForSecret{}, fakeOURepoForSecret{}, recorder, slog.Default()), sink
}

func TestSecretCreateUpdateDeleteAreAudited(t *testing.T) {
	svc, sink := auditService()
	accountID, userID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	sec, err := svc.Create(ctx, secret_create.SecretCreateCommand{
		ID:    uuid.New(),
		Name:  "ci-token",
		Kind:  secret.KindOpaque,
		Value: auditSecretValue,
		Tags:  []string{"ci"},

		AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newValue := "rotated-" + auditSecretValue
	if _, err := svc.Update(ctx, secret_update.SecretUpdateCommand{
		ID: sec.ID, Value: &newValue, AuthContext: auth,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := svc.Delete(ctx, secret_delete.SecretDeleteCommand{
		ID: sec.ID, AuthContext: auth,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	for _, action := range []string{
		audit_event.ActionSecretCreate,
		audit_event.ActionSecretUpdate,
		audit_event.ActionSecretDelete,
	} {
		event, ok := sink.Find(action)
		if !ok {
			t.Fatalf("no %q event; recorded %v", action, sink.Actions())
		}
		if event.ActorUserID == nil || *event.ActorUserID != userID {
			t.Errorf("%s actor = %v, want %v", action, event.ActorUserID, userID)
		}
		if event.TargetType != audit_event.TargetSecret || event.TargetID != sec.ID.String() {
			t.Errorf("%s target = %s/%s, want secret/%s", action, event.TargetType, event.TargetID, sec.ID)
		}
		// The name must be there — the delete row is written from a pre-delete read
		// precisely so it can name a secret whose row is already gone.
		if !strings.Contains(event.Detail, "ci-token") {
			t.Errorf("%s detail %q does not name the secret", action, event.Detail)
		}
	}

	// And the actual values appear nowhere.
	for _, event := range sink.Events() {
		for _, leaked := range []string{auditSecretValue, newValue} {
			if strings.Contains(event.Detail, leaked) {
				t.Fatalf("%s leaked a secret value: %q", event.Action, event.Detail)
			}
		}
	}
	update, _ := sink.Find(audit_event.ActionSecretUpdate)
	if !strings.Contains(update.Detail, "value replaced") {
		t.Errorf("an update that rotated the value does not say so: %q", update.Detail)
	}
}

// An attachment is what makes a secret reach a device; the secret merely existing
// changes nothing, so the attach is the operationally interesting event.
func TestAttachAndDetachAreAudited(t *testing.T) {
	svc, sink := auditService()
	accountID, userID, nodeID := uuid.New(), uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	sec, err := svc.Create(ctx, secret_create.SecretCreateCommand{
		ID: uuid.New(), Name: "db-url", Kind: secret.KindEnv, Key: "DB_URL",
		Value: "postgres://x", AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	att, err := svc.Attach(ctx, accountID, userID, sec.ID, nil, &nodeID)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := svc.Detach(ctx, accountID, userID, att.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}

	attach, ok := sink.Find(audit_event.ActionSecretAttach)
	if !ok {
		t.Fatalf("no attach event; recorded %v", sink.Actions())
	}
	if !strings.Contains(attach.Detail, nodeID.String()) {
		t.Errorf("attach detail %q does not say where it was attached", attach.Detail)
	}
	if _, ok := sink.Find(audit_event.ActionSecretDetach); !ok {
		t.Fatalf("no detach event; recorded %v", sink.Actions())
	}
}

// A tag attachment has DYNAMIC membership — it also delivers secrets tagged
// later — so it is a standing grant and its subject is the tag, not a secret.
func TestTagAttachIsAuditedAgainstTheTag(t *testing.T) {
	svc, sink := auditService()
	accountID, userID, nodeID := uuid.New(), uuid.New(), uuid.New()

	if _, err := svc.AttachTag(context.Background(), accountID, userID, "Rack-1", nil, &nodeID); err != nil {
		t.Fatalf("attach tag: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionSecretTagAttach)
	if !ok {
		t.Fatalf("no tag attach event; recorded %v", sink.Actions())
	}
	if event.TargetType != audit_event.TargetSecretTag {
		t.Errorf("target type = %q, want %q", event.TargetType, audit_event.TargetSecretTag)
	}
	// Normalized, as stored — a trail that records the raw input cannot be joined
	// against the tag on the secrets.
	if event.TargetID != "rack-1" {
		t.Errorf("target id = %q, want the normalized tag %q", event.TargetID, "rack-1")
	}
}

// One row for the whole declarative replace, aimed at the node or OU whose
// delivered set changed: the operator performed one action, and per-subject rows
// would bury everything around them.
func TestReplaceAttachmentsRecordsOneRowForTheTarget(t *testing.T) {
	svc, sink := auditService()
	accountID, userID, nodeID := uuid.New(), uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	sec, err := svc.Create(ctx, secret_create.SecretCreateCommand{
		ID: uuid.New(), Name: "a", Kind: secret.KindOpaque, Value: "1", AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.ReplaceAttachmentsForTarget(ctx, accountID, userID, nil, &nodeID,
		[]uuid.UUID{sec.ID}, []string{"ci"}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionSecretAttachmentsReplace)
	if !ok {
		t.Fatalf("no replace event; recorded %v", sink.Actions())
	}
	if event.TargetType != audit_event.TargetNode || event.TargetID != nodeID.String() {
		t.Errorf("target = %s/%s, want node/%s", event.TargetType, event.TargetID, nodeID)
	}
	for _, want := range []string{"secrets=1", "ci"} {
		if !strings.Contains(event.Detail, want) {
			t.Errorf("detail %q is missing %q", event.Detail, want)
		}
	}
}

// A node attribute is an env-kind secret behind a per-node API. The row targets
// the NODE and names the KEY, because an operator asking "who changed this node's
// CI token" is not thinking about the secrets table.
func TestNodeAttributeWritesTargetTheNodeAndNameTheKey(t *testing.T) {
	svc, sink := auditService()
	accountID, userID, nodeID := uuid.New(), uuid.New(), uuid.New()
	actor := audit.UserActor(accountID, userID)
	ctx := context.Background()

	if _, err := svc.UpsertNodeAttribute(ctx, actor, accountID, nodeID,
		"ci_agent_token", auditSecretValue, true); err != nil {
		t.Fatalf("upsert attribute: %v", err)
	}
	if err := svc.DeleteNodeAttribute(ctx, actor, accountID, nodeID, "ci_agent_token"); err != nil {
		t.Fatalf("delete attribute: %v", err)
	}

	for _, action := range []string{
		audit_event.ActionNodeAttributeSet,
		audit_event.ActionNodeAttributeDelete,
	} {
		event, ok := sink.Find(action)
		if !ok {
			t.Fatalf("no %q event; recorded %v", action, sink.Actions())
		}
		if event.TargetType != audit_event.TargetNode || event.TargetID != nodeID.String() {
			t.Errorf("%s target = %s/%s, want node/%s", action, event.TargetType, event.TargetID, nodeID)
		}
		if !strings.Contains(event.Detail, "ci_agent_token") {
			t.Errorf("%s detail %q does not name the attribute key", action, event.Detail)
		}
		if strings.Contains(event.Detail, auditSecretValue) {
			t.Fatalf("%s leaked the attribute value: %q", action, event.Detail)
		}
	}
}

// The secret_upsert workflow block is normally the engine, not a person, and it
// genuinely does not know whether it created or updated — hence its own action.
func TestUpsertForNodeRecordsTheCallerSuppliedActor(t *testing.T) {
	svc, sink := auditService()
	accountID, nodeID := uuid.New(), uuid.New()

	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID),
		accountID, nodeID, "captured", secret.KindOpaque, "", auditSecretValue,
		secret.UpsertOptions{}); err != nil {
		t.Fatalf("upsert for node: %v", err)
	}

	event, ok := sink.Find(audit_event.ActionSecretUpsert)
	if !ok {
		t.Fatalf("no upsert event; recorded %v", sink.Actions())
	}
	if event.ActorKind != audit_event.ActorSystem || event.ActorUserID != nil {
		t.Errorf("recorded as %q/%v, want a system actor", event.ActorKind, event.ActorUserID)
	}
	if !strings.Contains(event.Detail, "captured") || !strings.Contains(event.Detail, nodeID.String()) {
		t.Errorf("detail %q should name the secret and the node", event.Detail)
	}
	if strings.Contains(event.Detail, auditSecretValue) {
		t.Fatalf("the captured value leaked: %q", event.Detail)
	}
}

func TestSecretNewServiceRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewService(newFakeSecretRepo(), &fakeAttachmentRepo{},
		fakeNodeRepoForSecret{}, fakeOURepoForSecret{}, nil, slog.Default())
}
