package organizational_unit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// recordingApplier records the apply_access_config dispatches a change produces.
type recordingApplier struct {
	nodes []uuid.UUID
	err   error
}

func (a *recordingApplier) EnqueueApplyAccessConfig(_ context.Context, _, nodeID uuid.UUID) error {
	a.nodes = append(a.nodes, nodeID)
	return a.err
}

// assignFakeMutationRepo accepts the assignment and records nothing else; the
// question here is what happens AFTER the column is written.
type assignFakeMutationRepo struct {
	repository.OUMutationRepository
	err error
}

func (f *assignFakeMutationRepo) AssignNodeOU(_ context.Context, _, _ uuid.UUID, _ *uuid.UUID) error {
	return f.err
}

func pushTestService(applier AccessApplier) (*Service, uuid.UUID) {
	nodeID := uuid.New()
	return &Service{
		ouMutationRepo: &assignFakeMutationRepo{},
		nodeRepo:       &policyFakeNodeRepo{n: &node.Node{ID: nodeID}},
		accessApplier:  applier,
		audit:          testRecorder(),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, nodeID
}

// A node's effective access is a union across node → OU → root → account, so
// moving it between units swaps every OU-level user it has. Writing the column
// alone left the OLD unit's users on the device — keys, sudo, and a password
// where the entry has one — until the node happened to reconnect, because the
// applied-generation self-heal runs from UpdateSystemInfo and not on a timer.
func TestAssignNodePushesAccessToTheDevice(t *testing.T) {
	applier := &recordingApplier{}
	svc, nodeID := pushTestService(applier)

	ouID := uuid.New()
	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, &ouID); err != nil {
		t.Fatalf("AssignNode: %v", err)
	}
	if len(applier.nodes) != 1 || applier.nodes[0] != nodeID {
		t.Errorf("dispatched %v, want exactly [%s]", applier.nodes, nodeID)
	}
}

// Clearing the unit is the same change in the other direction and must push too.
func TestUnassigningANodeAlsoPushes(t *testing.T) {
	applier := &recordingApplier{}
	svc, nodeID := pushTestService(applier)

	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, nil); err != nil {
		t.Fatalf("AssignNode(nil): %v", err)
	}
	if len(applier.nodes) != 1 {
		t.Errorf("dispatched %d applies when clearing the unit, want 1", len(applier.nodes))
	}
}

// A failed assignment must not push: nothing changed, and an apply would be a
// lie about what the platform just did.
func TestAFailedAssignmentPushesNothing(t *testing.T) {
	applier := &recordingApplier{}
	svc, nodeID := pushTestService(applier)
	svc.ouMutationRepo = &assignFakeMutationRepo{err: errors.New("unit is gone")}

	ouID := uuid.New()
	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, &ouID); err == nil {
		t.Fatal("expected the assignment to fail")
	}
	if len(applier.nodes) != 0 {
		t.Errorf("dispatched %d applies for a failed assignment, want 0", len(applier.nodes))
	}
}

// ssh_password_auth is written into sshd's config by apply_access_config, so
// turning it off and writing only the column left password logins open on a node
// the UI already showed as closed.
func TestSettingPasswordAuthPushesAccessToTheDevice(t *testing.T) {
	applier := &recordingApplier{}
	svc, nodeID := pushTestService(applier)

	if err := svc.SetNodePolicy(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID,
		nil, nil, flag(organizational_unit.PolicyOff)); err != nil {
		t.Fatalf("SetNodePolicy: %v", err)
	}
	if len(applier.nodes) != 1 {
		t.Fatalf("dispatched %d applies, want 1", len(applier.nodes))
	}
}

// Boot policy is read by the platform at boot time, not held by the device, so
// changing it must NOT push an access apply — otherwise every reboot-to-recovery
// becomes an access event on the node.
func TestBootPolicyAloneDoesNotPushAccess(t *testing.T) {
	applier := &recordingApplier{}
	svc, nodeID := pushTestService(applier)

	if err := svc.SetNodePolicy(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID,
		flag(organizational_unit.PolicyOn), flag(organizational_unit.PolicyOn), nil); err != nil {
		t.Fatalf("SetNodePolicy: %v", err)
	}
	if len(applier.nodes) != 0 {
		t.Errorf("dispatched %d applies for a boot-policy change, want 0", len(applier.nodes))
	}
}

// The push is best-effort: apply_access_config is queueable and superseding, so
// a dispatch failure means "later", not "the edit did not happen". Refusing the
// operator's change because its follow-up failed would be worse than the delay.
func TestAPushFailureDoesNotFailTheEdit(t *testing.T) {
	svc, nodeID := pushTestService(&recordingApplier{err: errors.New("node offline")})

	ouID := uuid.New()
	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, &ouID); err != nil {
		t.Errorf("AssignNode failed because its push did: %v", err)
	}
}

// No applier wired keeps working — the pre-existing behaviour, and what an
// install that has not been rewired relies on.
func TestNoApplierIsNotAFailure(t *testing.T) {
	svc, nodeID := pushTestService(nil)

	ouID := uuid.New()
	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, &ouID); err != nil {
		t.Errorf("AssignNode without an applier: %v", err)
	}
}
