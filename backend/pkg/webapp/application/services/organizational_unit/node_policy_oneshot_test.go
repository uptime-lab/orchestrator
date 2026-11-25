package organizational_unit

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// policyFakeNodeRepo records the columns SetNodePolicy writes. Everything else
// panics through the embedded nil interface, so the test also proves the call
// touches nothing more than it claims.
type policyFakeNodeRepo struct {
	repository.NodeRepository
	n       *node.Node
	written map[string]interface{}
}

func (f *policyFakeNodeRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	return f.n, nil
}

func (f *policyFakeNodeRepo) UpdateNodeColumns(_ context.Context, _, _ uuid.UUID, cols map[string]interface{}) error {
	f.written = cols
	return nil
}

func flag(f organizational_unit.PolicyFlag) *organizational_unit.PolicyFlag { return &f }

// A deliberate boot-target change must supersede an in-flight one-shot.
//
// reboot-to-recovery saves the node's boot target, forces netboot, and restores
// the saved value the next time the node reports it came up over HTTP. Setting
// the target by hand inside that window used to leave the saved value untouched,
// so the restore would later overwrite the operator's change with a value from
// before they made it — no error, no log, just a setting that reverts.
//
// The symptom is indirect: a node pinned to netboot permanently, and later
// install cycles failing with "came back netbooted" or "device has mounted
// partitions", neither of which names the cause.
func TestSettingBootTargetClearsAPendingOneShot(t *testing.T) {
	forced := false
	repo := &policyFakeNodeRepo{n: &node.Node{
		ID:                    uuid.New(),
		BootToOS:              &forced,
		RecoveryRevertPending: true,
		RecoveryPrevBootToOS:  "off", // the stale value that did the damage
	}}
	s := &Service{nodeRepo: repo, audit: testRecorder()}

	if err := s.SetNodePolicy(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), repo.n.ID,
		nil, flag(organizational_unit.PolicyOn), nil); err != nil {
		t.Fatalf("SetNodePolicy: %v", err)
	}

	if got, ok := repo.written["recovery_revert_pending"]; !ok || got != false {
		t.Errorf("recovery_revert_pending = %v (present=%v), want false — "+
			"the pending one-shot would otherwise undo this change later", got, ok)
	}
	if got, ok := repo.written["recovery_prev_boot_to_os"]; !ok || got != "" {
		t.Errorf("recovery_prev_boot_to_os = %q (present=%v), want empty", got, ok)
	}
	boot, ok := repo.written["boot_to_os"].(*bool)
	if !ok || boot == nil || !*boot {
		t.Errorf("boot_to_os = %v, want the requested on", repo.written["boot_to_os"])
	}
}

// The one-shot bookkeeping belongs to the boot target. A call that changes only
// other flags must not clear it, or an operator toggling password auth would
// silently cancel a reboot-to-recovery in flight.
func TestOtherPolicyFlagsLeaveTheOneShotAlone(t *testing.T) {
	forced := false
	repo := &policyFakeNodeRepo{n: &node.Node{
		ID:                    uuid.New(),
		BootToOS:              &forced,
		RecoveryRevertPending: true,
		RecoveryPrevBootToOS:  "on",
	}}
	s := &Service{nodeRepo: repo, audit: testRecorder()}

	if err := s.SetNodePolicy(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), repo.n.ID,
		nil, nil, flag(organizational_unit.PolicyOff)); err != nil {
		t.Fatalf("SetNodePolicy: %v", err)
	}

	if _, ok := repo.written["recovery_revert_pending"]; ok {
		t.Error("a password-auth change cleared the pending one-shot")
	}
	if _, ok := repo.written["boot_to_os"]; ok {
		t.Error("a password-auth change wrote boot_to_os")
	}
	if got := repo.written["ssh_password_auth"]; got == nil {
		t.Error("the flag that was actually requested was not written")
	}
}
