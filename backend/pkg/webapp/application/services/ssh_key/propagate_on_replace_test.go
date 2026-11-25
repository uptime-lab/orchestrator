package ssh_key

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_update"
)

// A second valid ed25519 public key, so a content replacement is a real change.
const replacementPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEr6xEkH4+GWfWZv2rzZDo4xw8sZ8xN0oGnvPbXohEbo replacement@example"

type countingPropagator struct{ calls int }

func (p *countingPropagator) PropagateKeyChange(context.Context, uuid.UUID) { p.calls++ }

// Replacing the key MATERIAL is a fleet-wide revocation of the old material,
// exactly like deleting the key — and it used to push nothing. The database said
// the old public key was gone while every node still authorised it in
// /etc/ssh/authorized_keys.d/%u, and nothing corrected that until the node
// happened to reconnect (the applied-generation self-heal runs from
// UpdateSystemInfo, not on a timer). A node up for a month kept honouring a key
// the operator believed they had rotated away.
func TestReplacingKeyContentPushesToTheFleet(t *testing.T) {
	svc, _, _ := auditKeyService()
	prop := &countingPropagator{}
	svc.SetAccessPropagator(prop)
	accountID, userID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	key, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{
		ID: uuid.New(), Alias: "laptop", KeyContent: auditTestPublicKey, AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	prop.calls = 0

	content := replacementPublicKey
	if _, err := svc.Update(ctx, ssh_key_update.SSHKeyUpdateCommand{
		ID: key.ID, KeyContent: &content, AuthContext: auth,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if prop.calls != 1 {
		t.Errorf("propagated %d times after replacing the key material, want 1", prop.calls)
	}
}

// ...and only on a content change. An alias edit, a default flip or a tag change
// alters nothing the device is holding, so pushing an apply to every node in the
// account would make renaming a key a fleet event.
func TestMetadataEditsDoNotPushToTheFleet(t *testing.T) {
	svc, _, _ := auditKeyService()
	prop := &countingPropagator{}
	svc.SetAccessPropagator(prop)
	accountID, userID := uuid.New(), uuid.New()
	auth := commands.NewAuthContext(accountID, userID)
	ctx := context.Background()

	key, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{
		ID: uuid.New(), Alias: "laptop", KeyContent: auditTestPublicKey, AuthContext: auth,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	prop.calls = 0

	alias := "laptop-renamed"
	isDefault := true
	if _, err := svc.Update(ctx, ssh_key_update.SSHKeyUpdateCommand{
		ID: key.ID, Alias: &alias, IsDefault: &isDefault, AuthContext: auth,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if prop.calls != 0 {
		t.Errorf("propagated %d times for a metadata-only edit, want 0", prop.calls)
	}
}
