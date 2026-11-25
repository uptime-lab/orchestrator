package organizational_unit

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// Stubs embed their interface so an unexpected call panics rather than
// returning a zero value (the convention of this package's other tests).

type oneUnitRepo struct {
	repository.OrganizationalUnitRepository
	unit organizational_unit.OrganizationalUnit
}

func (r *oneUnitRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	copied := r.unit
	return &copied, nil
}

type unitAttachmentsRepo struct {
	repository.SecretAttachmentRepository
}

func (r *unitAttachmentsRepo) ListByOU(context.Context, uuid.UUID, uuid.UUID) ([]secret.SecretAttachment, error) {
	return nil, nil
}

func (r *unitAttachmentsRepo) ListByNode(context.Context, uuid.UUID, uuid.UUID) ([]secret.SecretAttachment, error) {
	return nil, nil
}

func unitResolver(unit organizational_unit.OrganizationalUnit, n node.Node) *Service {
	return &Service{
		ouRepo:            &oneUnitRepo{unit: unit},
		nodeRepo:          &oneNodeRepo{n: n},
		accountPolicyRepo: &emptyPolicyRepo{},
		attachmentRepo:    &unitAttachmentsRepo{},
		secretRepo:        &noSecretsRepo{},
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// The dispatch path refuses a username that resolves with no credential beside
// it, and that refusal has to name the level that supplied the username and the
// unit to go and fix. Both facts are carried on the resolved provisioning, so a
// resolution that drops them turns an actionable refusal back into "the user
// would be created with no way to log in" and nothing else.
func TestResolveEffectiveProvisioningReportsAnInheritedUsernameAndItsUnit(t *testing.T) {
	accountID, nodeID, ouID := uuid.New(), uuid.New(), uuid.New()
	inherited := "orchestrator"
	svc := unitResolver(
		organizational_unit.OrganizationalUnit{ID: ouID, AccountID: accountID, Name: "Homelab", DefaultUsername: &inherited},
		node.Node{ID: nodeID, AccountID: accountID, OUID: &ouID},
	)

	out, err := svc.ResolveEffectiveProvisioning(context.Background(), accountID, nodeID)
	if err != nil {
		t.Fatalf("ResolveEffectiveProvisioning() = %v, want nil", err)
	}
	if out.Username != inherited {
		t.Fatalf("Username = %q, want the unit's default", out.Username)
	}
	if out.UsernameSource != organizational_unit.SourceOU {
		t.Errorf("UsernameSource = %q, want %q", out.UsernameSource, organizational_unit.SourceOU)
	}
	if out.OUName != "Homelab" {
		t.Errorf("OUName = %q, want the node's own unit", out.OUName)
	}
	if out.RootOUName != "" {
		t.Errorf("RootOUName = %q, want empty for a top-level unit", out.RootOUName)
	}
}

// A node that overrides the username owns it, and an unassigned node has no
// unit to name — quoting an empty one would send the operator nowhere.
func TestResolveEffectiveProvisioningReportsANodeUsernameWithNoUnit(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	svc := &Service{
		nodeRepo:          &oneNodeRepo{n: node.Node{ID: nodeID, AccountID: accountID, DefaultUsername: "deploy"}},
		accountPolicyRepo: &emptyPolicyRepo{},
		attachmentRepo:    &unitAttachmentsRepo{},
		secretRepo:        &noSecretsRepo{},
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	out, err := svc.ResolveEffectiveProvisioning(context.Background(), accountID, nodeID)
	if err != nil {
		t.Fatalf("ResolveEffectiveProvisioning() = %v, want nil", err)
	}
	if out.UsernameSource != organizational_unit.SourceNode {
		t.Errorf("UsernameSource = %q, want %q", out.UsernameSource, organizational_unit.SourceNode)
	}
	if out.OUName != "" {
		t.Errorf("OUName = %q, want empty for a node in no unit", out.OUName)
	}
}
