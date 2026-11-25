package node

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// approvingRepo is the cap harness's repository with an approval that sticks,
// so the dispatch after it reads the node as approved, as the database would.
type approvingRepo struct {
	*capRepo
}

func (r approvingRepo) ApproveUnderDeviceCap(ctx context.Context, nodeID uuid.UUID, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	admission, err := r.capRepo.ApproveUnderDeviceCap(ctx, nodeID, limits)
	if err == nil {
		r.byID.ApprovalStatus = node.ApprovalStatusApproved
	}
	return admission, err
}

// accessCmdService has no command history and records what is registered.
type accessCmdService struct {
	capturingCmdService
}

func (f *accessCmdService) GetNodeCommandsByNodeID(context.Context, uuid.UUID, uuid.UUID, int, int) ([]node_command.NodeCommand, error) {
	return nil, nil
}

// grantingResolver resolves an access config with a working login.
type grantingResolver struct {
	genFakeResolver
}

func (f *grantingResolver) ResolveAccessConfig(context.Context, uuid.UUID, uuid.UUID, bool, bool) (services.AccessConfig, error) {
	return services.AccessConfig{
		Hash:  "desired",
		Users: []services.AccessConfigUser{{Username: "ops", AuthorizedKeys: []string{"ssh-ed25519 AAAA"}}},
	}, nil
}

func pendingConnectedNode(accountID uuid.UUID) *node.Node {
	bootDevice := "nvme"
	return &node.Node{
		ID: uuid.New(), AccountID: accountID, Name: "pending-1",
		Status:         node.StatusOffline,
		ApprovalStatus: node.ApprovalStatusPendingApproval,
		AgentVersion:   "0.8.6",
		SystemInfo: &node.SystemInfo{Hardware: &node.HardwareInfo{Boot: &node.BootInfo{
			CurrentBootDevice: bootDevice,
		}}},
	}
}

// A node waiting for approval takes no commands, so the access apply it
// would have received on connect was refused, and nothing retried it: the
// approved node ran without the access an operator had given it. Approval
// is the moment it can land.
func TestApprovalAppliesTheNodesAccess(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	pending := pendingConnectedNode(accountID)
	h := newCapHarness(nil, 0, fixedCap{cap: 25})
	h.repo.byID = pending
	h.svc.nodeRepo = approvingRepo{capRepo: h.repo}
	cmds := &accessCmdService{}
	h.svc.nodeCommandService = cmds
	h.svc.SetAccessGenerationResolvers(&grantingResolver{}, &grantingResolver{})

	if _, err := h.svc.ApproveNode(context.Background(), accountID, userID, pending.ID); err != nil {
		t.Fatalf("ApproveNode: %v", err)
	}
	if cmds.registered == nil || cmds.registered.Slug != node_command.SlugApplyAccessConfig {
		t.Fatalf("approval dispatched no access apply (registered %+v)", cmds.registered)
	}
}

// While a node awaits approval, an access edit that reaches it is a skip, not
// a failure: the propagation that reported "failed:1" for such a node was
// reporting nothing wrong.
func TestAccessApplyToAPendingNodeIsASkip(t *testing.T) {
	accountID := uuid.New()
	pending := pendingConnectedNode(accountID)
	h := newCapHarness(nil, 0, fixedCap{cap: 25})
	h.repo.byID = pending
	cmds := &accessCmdService{}
	h.svc.nodeCommandService = cmds

	if err := h.svc.EnqueueApplyAccessConfig(context.Background(), accountID, pending.ID); err != nil {
		t.Fatalf("an access apply to a pending node failed: %v", err)
	}
	if cmds.registered != nil {
		t.Fatalf("a command was registered for a node that takes none: %+v", cmds.registered)
	}
}
