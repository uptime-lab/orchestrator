package ssh_key

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_generate"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
)

// SSH keys are inseparable from the access entries that reference them:
// auditing "key X was attached to user Y" while X's content could be replaced
// silently would leave the same hole one level down. Deleting a key is a
// fleet-wide revocation — the cascade strips it from every entry, and an entry
// left with no keys is a LOCKED OS user.

// A real ed25519 public key, so ssh_key.NewSSHKey's parsing accepts it.
const auditTestPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExYxDErlWSKmw2GdyOkKFDMDmDcMRNq4zqIiJmMCcRr audit-test"

type auditKeyStore struct {
	repository.SSHKeyRepository
	byID map[uuid.UUID]*ssh_key.SSHKey
	// calls records the order of the mutations Delete performs, so a test can
	// assert that references are cleared BEFORE the key row disappears.
	calls   []string
	cleared map[string]int64
}

func newAuditKeyStore() *auditKeyStore {
	return &auditKeyStore{byID: map[uuid.UUID]*ssh_key.SSHKey{}}
}

func (s *auditKeyStore) Create(_ context.Context, key *ssh_key.SSHKey) error {
	copied := *key
	s.byID[key.ID] = &copied
	return nil
}

func (s *auditKeyStore) Update(_ context.Context, key *ssh_key.SSHKey) error {
	copied := *key
	s.byID[key.ID] = &copied
	return nil
}

func (s *auditKeyStore) GetByID(_ context.Context, _, id uuid.UUID) (*ssh_key.SSHKey, error) {
	k, ok := s.byID[id]
	if !ok {
		return nil, app_errors.ErrSSHKeyNotFound
	}
	copied := *k
	return &copied, nil
}

func (s *auditKeyStore) GetAllByAccountID(context.Context, uuid.UUID) ([]ssh_key.SSHKey, error) {
	out := make([]ssh_key.SSHKey, 0, len(s.byID))
	for _, k := range s.byID {
		out = append(out, *k)
	}
	return out, nil
}

func (s *auditKeyStore) Delete(_ context.Context, _, id uuid.UUID) error {
	s.calls = append(s.calls, "delete")
	delete(s.byID, id)
	return nil
}

func (s *auditKeyStore) UnsetDefaultForAccount(context.Context, uuid.UUID) error { return nil }

func (s *auditKeyStore) ClearProvisioningReferences(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	s.calls = append(s.calls, "clear")
	return s.cleared, nil
}

func auditKeyService() (*Service, *auditKeyStore, *audittest.Sink) {
	recorder, sink := audittest.New()
	store := newAuditKeyStore()
	return NewService(store, recorder, slog.New(slog.NewTextHandler(io.Discard, nil))), store, sink
}

func TestSSHKeyCreateUpdateDeleteAreAudited(t *testing.T) {
	svc, _, sink := auditKeyService()
	accountID, userID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	key, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{
		ID: uuid.New(), Alias: "laptop", KeyContent: auditTestPublicKey, AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	alias := "laptop-renamed"
	if _, err := svc.Update(ctx, ssh_key_update.SSHKeyUpdateCommand{
		ID: key.ID, Alias: &alias, AuthContext: auth,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := svc.Delete(ctx, accountID, userID, key.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	for _, action := range []string{
		audit_event.ActionSSHKeyCreate,
		audit_event.ActionSSHKeyUpdate,
		audit_event.ActionSSHKeyDelete,
	} {
		event, ok := sink.Find(action)
		if !ok {
			t.Fatalf("no %q event; recorded %v", action, sink.Actions())
		}
		if event.ActorUserID == nil || *event.ActorUserID != userID {
			t.Errorf("%s actor = %v, want %v", action, event.ActorUserID, userID)
		}
		if event.TargetType != audit_event.TargetSSHKey || event.TargetID != key.ID.String() {
			t.Errorf("%s target = %s/%s, want ssh_key/%s", action, event.TargetType, event.TargetID, key.ID)
		}
	}

	// Replacing key_content re-points every entry referencing this key at
	// different key material without touching a single entry, so the update row
	// has to distinguish that from a rename.
	update, _ := sink.Find(audit_event.ActionSSHKeyUpdate)
	if !strings.Contains(update.Detail, "laptop-renamed") {
		t.Errorf("update detail %q does not record the rename", update.Detail)
	}
	if strings.Contains(update.Detail, "key content replaced") {
		t.Errorf("a rename was recorded as a key-content replacement: %q", update.Detail)
	}
}

func TestSSHKeyUpdateRecordsAKeyContentReplacement(t *testing.T) {
	svc, _, sink := auditKeyService()
	accountID, userID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	key, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{
		ID: uuid.New(), Alias: "laptop", KeyContent: auditTestPublicKey, AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	replacement := auditTestPublicKey
	if _, err := svc.Update(ctx, ssh_key_update.SSHKeyUpdateCommand{
		ID: key.ID, KeyContent: &replacement, AuthContext: auth,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	event, _ := sink.Find(audit_event.ActionSSHKeyUpdate)
	if !strings.Contains(event.Detail, "key content replaced") {
		t.Errorf("detail %q does not record that the key material changed", event.Detail)
	}
}

// A generated connection keypair's PRIVATE half is encrypted at rest. It must
// never reach the trail, and the row must be distinguishable from an upload.
func TestGenerateConnectionKeyIsAuditedWithoutThePrivateKey(t *testing.T) {
	svc, _, sink := auditKeyService()
	accountID, userID := uuid.New(), uuid.New()

	key, err := svc.GenerateConnectionKey(context.Background(), ssh_key_generate.SSHKeyGenerateCommand{
		ID: uuid.New(), Alias: "platform", AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if key.PrivateKey == "" {
		t.Fatal("expected a generated private key to assert against")
	}

	event, ok := sink.Find(audit_event.ActionSSHKeyGenerate)
	if !ok {
		t.Fatalf("no ssh_key.generate event; recorded %v", sink.Actions())
	}
	for _, event := range sink.Events() {
		if strings.Contains(event.Detail, key.PrivateKey) {
			t.Fatalf("%s leaked the private key into audit detail", event.Action)
		}
	}
	if !strings.Contains(event.Detail, "platform") {
		t.Errorf("detail %q does not name the key", event.Detail)
	}
}

// The delete row must name what was revoked from the pre-delete row: afterwards
// the id resolves to nothing, and "which key stopped working fleet-wide" is the
// question.
func TestSSHKeyDeleteNamesTheRevokedKey(t *testing.T) {
	svc, _, sink := auditKeyService()
	accountID, userID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	key, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{
		ID: uuid.New(), Alias: "contractor", KeyContent: auditTestPublicKey, AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.Delete(ctx, accountID, userID, key.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	event, _ := sink.Find(audit_event.ActionSSHKeyDelete)
	if !strings.Contains(event.Detail, "contractor") {
		t.Errorf("detail %q does not name the deleted key", event.Detail)
	}
	// It was the account's only key, so it was the default — worth recording,
	// because deleting the default promotes another one behind the operator's back.
	if !strings.Contains(event.Detail, "was_default=true") {
		t.Errorf("detail %q does not record that the default key was removed", event.Detail)
	}
}

func TestSSHKeyNewServiceRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewService(newAuditKeyStore(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
