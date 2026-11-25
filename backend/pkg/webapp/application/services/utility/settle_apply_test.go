package utility

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// The fakes embed their interface so only the methods SettleApply's path
// touches need bodies — anything else panics, which is the assertion that the
// path stayed narrow.

type settleFakeStates struct {
	repository.NodeUtilityStateRepository
	applied map[string]string // utilityID -> hash
	failed  map[string]string // utilityID -> message
}

func (f *settleFakeStates) RecordApplied(_ context.Context, _, _ uuid.UUID, utilityID, hash string) error {
	f.applied[utilityID] = hash
	return nil
}

func (f *settleFakeStates) RecordFailed(_ context.Context, _, _ uuid.UUID, utilityID, msg string) error {
	f.failed[utilityID] = msg
	return nil
}

type settleFakeAssignments struct {
	repository.UtilityAssignmentRepository
	rows []utility.Assignment
	err  error
}

func (f *settleFakeAssignments) ListByAccount(context.Context, uuid.UUID) ([]utility.Assignment, error) {
	return f.rows, f.err
}

type settleFakeNodeRepo struct {
	repository.NodeRepository
	err error
}

func (f *settleFakeNodeRepo) GetByID(_ context.Context, _, nodeID uuid.UUID) (*node.Node, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &node.Node{ID: nodeID}, nil // no OU: ouChainOf never touches ouRepo
}

func settleFixture(t *testing.T, assigned []string, nodeErr, listErr error) (*Service, *settleFakeStates, *node_command.NodeCommand) {
	t.Helper()
	accountID, nodeID := uuid.New(), uuid.New()
	catalog := &utility.Catalog{Utilities: []utility.Definition{{ID: "docker", Name: "Docker"}, {ID: "kiosk", Name: "Kiosk"}}}
	rows := make([]utility.Assignment, 0, len(assigned))
	for _, id := range assigned {
		rows = append(rows, utility.Assignment{ID: uuid.New(), AccountID: accountID, UtilityID: id, AccountWide: true, Enabled: true})
	}
	states := &settleFakeStates{applied: map[string]string{}, failed: map[string]string{}}
	svc := &Service{
		catalog:     catalog,
		assignments: &settleFakeAssignments{rows: rows, err: listErr},
		states:      states,
		nodeRepo:    &settleFakeNodeRepo{err: nodeErr},
		logger:      slog.Default(),
	}
	cmd := &node_command.NodeCommand{
		AccountID: accountID,
		NodeID:    nodeID,
		Slug:      node_command.SlugApplyUtilityConfig,
		Payload: map[string]interface{}{
			"utilities": []interface{}{
				map[string]interface{}{"utility_id": "docker", "config_hash": "h-docker"},
				map[string]interface{}{"utility_id": "kiosk", "config_hash": "h-kiosk"},
			},
		},
	}
	return svc, states, cmd
}

// The marker decides each rung regardless of the command's overall status: a
// converged sibling stays green when another utility fails, and a utility the
// agent never reached fails only when the whole command failed.
func TestSettleApplyMarkerRules(t *testing.T) {
	svc, states, cmd := settleFixture(t, []string{"docker", "kiosk"}, nil, nil)
	svc.SettleApply(context.Background(), cmd, string(node_command.StatusFailed), map[string]string{
		"utility.docker": "applied",
		"utility.kiosk":  "failed: unit refused to start",
	})
	if states.applied["docker"] != "h-docker" {
		t.Fatalf("docker: want applied hash recorded from the DISPATCH payload, got %q", states.applied["docker"])
	}
	if states.failed["kiosk"] == "" {
		t.Fatalf("kiosk: want the failure marker recorded")
	}
}

func TestSettleApplyFailureWithoutMarkerFailsEveryUtility(t *testing.T) {
	svc, states, cmd := settleFixture(t, []string{"docker", "kiosk"}, nil, nil)
	svc.SettleApply(context.Background(), cmd, string(node_command.StatusFailed), map[string]string{})
	if len(states.failed) != 2 {
		t.Fatalf("want both utilities failed, got %v", states.failed)
	}
	if len(states.applied) != 0 {
		t.Fatalf("nothing converged, got applied %v", states.applied)
	}
}

func TestSettleApplySuccessWithoutMarkerRecordsNothing(t *testing.T) {
	svc, states, cmd := settleFixture(t, []string{"docker", "kiosk"}, nil, nil)
	svc.SettleApply(context.Background(), cmd, string(node_command.StatusSuccess), map[string]string{})
	if len(states.applied) != 0 || len(states.failed) != 0 {
		t.Fatalf("success without a claim must stay declared/update_pending, got %v / %v", states.applied, states.failed)
	}
}

// A late terminal report must not resurrect a pruned ladder row: a utility no
// longer in the node's resolved set records nothing, converged or not.
func TestSettleApplySkipsUtilitiesNoLongerAssigned(t *testing.T) {
	svc, states, cmd := settleFixture(t, []string{"docker"}, nil, nil) // kiosk was unassigned in flight
	svc.SettleApply(context.Background(), cmd, string(node_command.StatusFailed), map[string]string{
		"utility.docker": "applied",
		"utility.kiosk":  "applied",
	})
	if states.applied["docker"] != "h-docker" {
		t.Fatalf("docker stays assigned and must settle, got %v", states.applied)
	}
	if _, ok := states.applied["kiosk"]; ok {
		t.Fatalf("kiosk was pruned; its settle must not re-create the row")
	}
}

// A settle for a DELETED node records nothing at all — CleanupNode dropped the
// ladder and anything written here would orphan a row for a gone node.
func TestSettleApplyDropsSettleForDeletedNode(t *testing.T) {
	svc, states, cmd := settleFixture(t, []string{"docker"}, app_errors.ErrNodeNotFound, nil)
	svc.SettleApply(context.Background(), cmd, string(node_command.StatusSuccess), map[string]string{
		"utility.docker": "applied",
	})
	if len(states.applied) != 0 || len(states.failed) != 0 {
		t.Fatalf("deleted node: want nothing recorded, got %v / %v", states.applied, states.failed)
	}
}

// A transient resolution error fails OPEN: losing a real settle over a read
// blip beats nothing, and the ladder self-corrects on the next apply.
func TestSettleApplyRecordsUnguardedOnResolutionError(t *testing.T) {
	svc, states, cmd := settleFixture(t, nil, nil, errors.New("db timeout"))
	svc.SettleApply(context.Background(), cmd, string(node_command.StatusSuccess), map[string]string{
		"utility.docker": "applied",
	})
	if states.applied["docker"] != "h-docker" {
		t.Fatalf("resolution error must fail open, got %v", states.applied)
	}
}
