package ssh_key

import (
	"context"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/google/uuid"
)

// Deleting a key must clear the provisioning columns that point at it, and must
// do so BEFORE the key row goes away — the ordering is the whole guarantee.
// While a row references a key that no longer exists,
// ResolveEffectiveProvisioning fails for that node, which takes install_os
// fill-in, playbook facts and apply_access_config with it.
func TestDeleteClearsProvisioningReferencesBeforeDeletingTheKey(t *testing.T) {
	svc, store, sink := auditKeyService()
	store.cleared = map[string]int64{"nodes": 2, "organizational_units": 1}
	accountID, userID := uuid.New(), uuid.New()
	ctx := context.Background()

	key, err := svc.Create(ctx, ssh_key_create.SSHKeyCreateCommand{
		ID: uuid.New(), Alias: "laptop", KeyContent: auditTestPublicKey,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	store.calls = nil

	if err := svc.Delete(ctx, accountID, userID, key.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if len(store.calls) != 2 || store.calls[0] != "clear" || store.calls[1] != "delete" {
		t.Fatalf("call order = %v, want [clear delete]", store.calls)
	}

	// The counts belong in the audit row: "every node in this OU stopped getting
	// a provisioning key" needs an author and a scale, not just a key id.
	var detail string
	for _, event := range sink.Events() {
		if event.Action == audit_event.ActionSSHKeyDelete {
			detail = event.Detail
		}
	}
	if !strings.Contains(detail, "cleared_provisioning_refs") || !strings.Contains(detail, "nodes:2") {
		t.Errorf("audit detail = %q, want it to record the cleared references", detail)
	}
}
