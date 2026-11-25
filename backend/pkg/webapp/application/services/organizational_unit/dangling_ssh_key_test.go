package organizational_unit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/google/uuid"
)

// Stubs embed their interface so an unexpected call panics on the nil embedded
// value rather than returning a zero one (same convention as delete_cascade_test).

type oneNodeRepo struct {
	repository.NodeRepository
	n node.Node
}

func (r *oneNodeRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	copied := r.n
	return &copied, nil
}

type missingKeyRepo struct {
	repository.SSHKeyRepository
	err     error
	askedID uuid.UUID
}

func (r *missingKeyRepo) GetByID(_ context.Context, _, keyID uuid.UUID) (*ssh_key.SSHKey, error) {
	r.askedID = keyID
	return nil, r.err
}

type emptyPolicyRepo struct {
	repository.AccountPolicyRepository
}

func (r *emptyPolicyRepo) Get(context.Context, uuid.UUID) (*organizational_unit.AccountPolicy, error) {
	return nil, nil
}

type noAttachmentsRepo struct {
	repository.SecretAttachmentRepository
}

func (r *noAttachmentsRepo) ListByNode(context.Context, uuid.UUID, uuid.UUID) ([]secret.SecretAttachment, error) {
	return nil, nil
}

type noSecretsRepo struct {
	repository.SecretRepository
}

func (r *noSecretsRepo) GetByIDs(context.Context, uuid.UUID, []uuid.UUID) ([]secret.Secret, error) {
	return nil, nil
}

func resolverWithKeyError(keyErr error, n node.Node) (*Service, *missingKeyRepo) {
	keys := &missingKeyRepo{err: keyErr}
	svc := &Service{
		nodeRepo:          &oneNodeRepo{n: n},
		sshKeyRepo:        keys,
		accountPolicyRepo: &emptyPolicyRepo{},
		attachmentRepo:    &noAttachmentsRepo{},
		secretRepo:        &noSecretsRepo{},
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return svc, keys
}

// A node pointing at a key that no longer exists must still resolve. Returning
// the error took down every caller of this function for that node — install_os
// fill-in, playbook facts and the whole access config — until someone edited
// the database by hand.
func TestResolveEffectiveProvisioningTolluratesADanglingSSHKey(t *testing.T) {
	accountID, nodeID, keyID := uuid.New(), uuid.New(), uuid.New()
	svc, keys := resolverWithKeyError(app_errors.ErrSSHKeyNotFound, node.Node{
		ID: nodeID, AccountID: accountID, SSHKeyID: &keyID, DefaultUsername: "orchestrator",
	})

	out, err := svc.ResolveEffectiveProvisioning(context.Background(), accountID, nodeID)
	if err != nil {
		t.Fatalf("ResolveEffectiveProvisioning() = %v, want nil for a dangling key reference", err)
	}
	if keys.askedID != keyID {
		t.Errorf("looked up key %s, want %s", keys.askedID, keyID)
	}
	if out.SSHKey != "" || out.SSHKeyType != "" {
		t.Errorf("SSHKey = %q/%q, want empty", out.SSHKey, out.SSHKeyType)
	}
	if out.Username != "orchestrator" {
		t.Errorf("Username = %q, want the rest of the resolution to survive", out.Username)
	}
}

// Only "not found" degrades. A database that is down must not quietly resolve
// to a node with no key — that is a config the fleet would then be handed.
func TestResolveEffectiveProvisioningStillFailsOnARealKeyError(t *testing.T) {
	accountID, nodeID, keyID := uuid.New(), uuid.New(), uuid.New()
	boom := errors.New("connection refused")
	svc, _ := resolverWithKeyError(boom, node.Node{ID: nodeID, AccountID: accountID, SSHKeyID: &keyID})

	if _, err := svc.ResolveEffectiveProvisioning(context.Background(), accountID, nodeID); !errors.Is(err, boom) {
		t.Fatalf("ResolveEffectiveProvisioning() = %v, want the underlying error", err)
	}
}
