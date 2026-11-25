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
	"github.com/google/uuid"
)

// Stubs embed their interface so an unexpected call panics on the nil embedded
// value rather than returning a zero one (same convention as delete_cascade_test).
// Named for this file on purpose: other test files in this package add
// equivalents for ResolveEffectiveProvisioning, and two identically-named stubs
// in one package would collide.

type policyNodeRepo struct {
	repository.NodeRepository
	n node.Node
}

func (r *policyNodeRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	copied := r.n
	return &copied, nil
}

type nilAccountPolicyRepo struct {
	repository.AccountPolicyRepository
}

func (r *nilAccountPolicyRepo) Get(context.Context, uuid.UUID) (*organizational_unit.AccountPolicy, error) {
	return nil, nil
}

type missingOURepo struct {
	repository.OrganizationalUnitRepository
	err error
}

func (r *missingOURepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	return nil, r.err
}

func policyResolver(ouErr error, n node.Node) *Service {
	return &Service{
		ouRepo:            &missingOURepo{err: ouErr},
		nodeRepo:          &policyNodeRepo{n: n},
		accountPolicyRepo: &nilAccountPolicyRepo{},
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// A node pointing at a unit that no longer exists must still resolve a policy.
// The error used to reach the HTTP-boot handler, which answers it with a 404 for
// boot.sig — so a network-first node with a stale ou_id could never boot again
// and read as dead hardware.
func TestResolveEffectivePolicyTreatsAMissingOUAsUnassigned(t *testing.T) {
	accountID, nodeID, ouID := uuid.New(), uuid.New(), uuid.New()
	svc := policyResolver(app_errors.ErrOrganizationalUnitNotFound, node.Node{
		ID: nodeID, AccountID: accountID, OUID: &ouID,
	})

	policy, err := svc.ResolveEffectivePolicy(context.Background(), accountID, nodeID)
	if err != nil {
		t.Fatalf("ResolveEffectivePolicy() = %v, want nil for a stale ou_id", err)
	}
	// Whatever the defaults are, the point is that a policy was produced at all
	// and that nothing claims to have come from a unit that is gone.
	for _, source := range []organizational_unit.PolicySource{
		policy.NetworkFirst.Source,
		policy.BootToOS.Source,
		policy.DefaultUsername.Source,
		policy.SSHKeyID.Source,
	} {
		if source == organizational_unit.SourceOU || source == organizational_unit.SourceRootOU {
			t.Errorf("policy source = %v, want nothing attributed to the missing unit", source)
		}
	}
}

// Only "not found" degrades: a database that is down must not silently resolve
// every node to the account default and hand that to the fleet.
func TestResolveEffectivePolicyStillFailsOnARealOUError(t *testing.T) {
	accountID, nodeID, ouID := uuid.New(), uuid.New(), uuid.New()
	boom := errors.New("connection refused")
	svc := policyResolver(boom, node.Node{ID: nodeID, AccountID: accountID, OUID: &ouID})

	if _, err := svc.ResolveEffectivePolicy(context.Background(), accountID, nodeID); !errors.Is(err, boom) {
		t.Fatalf("ResolveEffectivePolicy() = %v, want the underlying error", err)
	}
}
