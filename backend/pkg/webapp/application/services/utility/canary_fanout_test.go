package utility

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

type multiDispatcher struct {
	mu   sync.Mutex
	cmds []node_command_cmd.BatchNodeCommandCommand
}

func (d *multiDispatcher) SendBatchCommand(_ context.Context, cmd node_command_cmd.BatchNodeCommandCommand) (*node_command_cmd.BatchCommandResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cmds = append(d.cmds, cmd)
	id := uuid.New()
	return &node_command_cmd.BatchCommandResult{
		Results: []node_command_cmd.NodeCommandResultItem{{Success: true, CommandID: &id}},
	}, nil
}

func (d *multiDispatcher) snapshot() []node_command_cmd.BatchNodeCommandCommand {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]node_command_cmd.BatchNodeCommandCommand, len(d.cmds))
	copy(out, d.cmds)
	return out
}

type canaryFakeAssignments struct {
	repository.UtilityAssignmentRepository
	rows []utility.Assignment
}

func (f *canaryFakeAssignments) ListByAccount(context.Context, uuid.UUID) ([]utility.Assignment, error) {
	return f.rows, nil
}

func (f *canaryFakeAssignments) GetByID(_ context.Context, _, id uuid.UUID) (*utility.Assignment, error) {
	for i := range f.rows {
		if f.rows[i].ID == id {
			return &f.rows[i], nil
		}
	}
	return nil, context.DeadlineExceeded
}

type canaryFakeNodeRepo struct {
	repository.NodeRepository
	nodes []node.Node
}

func (f *canaryFakeNodeRepo) GetAllByAccountID(context.Context, uuid.UUID) ([]node.Node, error) {
	return f.nodes, nil
}

func (f *canaryFakeNodeRepo) GetByID(_ context.Context, _, nodeID uuid.UUID) (*node.Node, error) {
	for i := range f.nodes {
		if f.nodes[i].ID == nodeID {
			return &f.nodes[i], nil
		}
	}
	return nil, context.DeadlineExceeded
}

func canaryFixture(t *testing.T, nodes []node.Node) (*Service, *multiDispatcher, uuid.UUID, utility.Assignment) {
	t.Helper()
	accountID := uuid.New()
	assignment := utility.Assignment{
		ID:          uuid.New(),
		AccountID:   accountID,
		UtilityID:   "docker",
		AccountWide: true,
		Enabled:     true,
		Config:      map[string]interface{}{},
	}
	catalog := &utility.Catalog{Utilities: []utility.Definition{{
		ID: "docker", Name: "Docker", Class: utility.ClassReconciled,
		Params: []workflow_block.ParamSpec{{Name: "channel", Type: workflow_block.ParamString, Default: "distro"}},
	}}}
	dispatcher := &multiDispatcher{}
	svc := &Service{
		catalog:     catalog,
		assignments: &canaryFakeAssignments{rows: []utility.Assignment{assignment}},
		states:      applyFakeStates{},
		nodeRepo:    &canaryFakeNodeRepo{nodes: nodes},
		logger:      slog.Default(),
		dispatcher:  dispatcher,
	}
	return svc, dispatcher, accountID, assignment
}

// A multi-node fan-out dispatches to exactly ONE node — a connected one when
// any is — and parks the rest with a reason naming the canary.
func TestApplyAssignmentDispatchesCanaryFirst(t *testing.T) {
	online := node.Node{ID: uuid.New(), Name: "canary-box", Status: node.StatusOnline}
	off1 := node.Node{ID: uuid.New(), Name: "away-1", Status: node.StatusOffline}
	off2 := node.Node{ID: uuid.New(), Name: "away-2", Status: node.StatusOffline}
	svc, dispatcher, accountID, assignment := canaryFixture(t, []node.Node{off1, online, off2})

	results, err := svc.ApplyAssignment(context.Background(), accountID, uuid.New(), assignment.ID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	cmds := dispatcher.snapshot()
	if len(cmds) != 1 {
		t.Fatalf("dispatched %d commands, want exactly the canary", len(cmds))
	}
	if cmds[0].NodeIDs[0] != online.ID {
		t.Fatal("the canary must be a CONNECTED node when any is")
	}
	if _, ok := cmds[0].Params["canary"]; !ok {
		t.Fatal("the canary command must carry the marker SettleApply continues from")
	}

	parked := 0
	for _, r := range results {
		if r.Skipped && strings.Contains(r.Reason, "canary-box") {
			parked++
		}
	}
	if parked != 2 {
		t.Fatalf("parked = %d, want 2 with a reason naming the canary", parked)
	}
}

// A single-target apply is not a fan-out: no marker, no parking.
func TestApplyAssignmentSingleNodeHasNoCanary(t *testing.T) {
	only := node.Node{ID: uuid.New(), Name: "solo", Status: node.StatusOnline}
	svc, dispatcher, accountID, assignment := canaryFixture(t, []node.Node{only})

	if _, err := svc.ApplyAssignment(context.Background(), accountID, uuid.New(), assignment.ID); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cmds := dispatcher.snapshot()
	if len(cmds) != 1 {
		t.Fatalf("dispatched %d, want 1", len(cmds))
	}
	if _, ok := cmds[0].Params["canary"]; ok {
		t.Fatal("a single-node apply must not carry the canary marker")
	}
}

// The successful settle of a canary command dispatches the remaining nodes —
// recomputed from the assignment as it is NOW — and those commands carry no
// marker, so the fan-out cannot cascade.
func TestSettleApplyContinuesCanaryFanOut(t *testing.T) {
	canaryNode := node.Node{ID: uuid.New(), Name: "canary-box", Status: node.StatusOnline}
	rest1 := node.Node{ID: uuid.New(), Name: "rest-1", Status: node.StatusOnline}
	rest2 := node.Node{ID: uuid.New(), Name: "rest-2", Status: node.StatusOffline}
	svc, dispatcher, accountID, assignment := canaryFixture(t, []node.Node{canaryNode, rest1, rest2})

	cmd := &node_command.NodeCommand{
		Slug:      node_command.SlugApplyUtilityConfig,
		AccountID: accountID,
		NodeID:    canaryNode.ID,
		Payload: map[string]interface{}{
			"union_hash": "h",
			"utilities":  []interface{}{},
			"canary": map[string]interface{}{
				"assignment_id": assignment.ID.String(),
				"user_id":       uuid.New().String(),
			},
		},
	}
	svc.SettleApply(context.Background(), cmd, string(node_command.StatusSuccess), map[string]string{})

	deadline := time.Now().Add(2 * time.Second)
	for {
		cmds := dispatcher.snapshot()
		if len(cmds) == 2 {
			for _, c := range cmds {
				if c.NodeIDs[0] == canaryNode.ID {
					t.Fatal("the canary must not be re-dispatched by its own settle")
				}
				if _, ok := c.Params["canary"]; ok {
					t.Fatal("continuation commands must carry no marker, or the fan-out cascades")
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("continuation dispatched %d commands, want 2", len(cmds))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A FAILED canary continues nothing: that is the whole point of the ordering.
func TestSettleApplyFailedCanaryStopsFanOut(t *testing.T) {
	canaryNode := node.Node{ID: uuid.New(), Name: "canary-box", Status: node.StatusOnline}
	rest := node.Node{ID: uuid.New(), Name: "rest-1", Status: node.StatusOnline}
	svc, dispatcher, accountID, assignment := canaryFixture(t, []node.Node{canaryNode, rest})

	cmd := &node_command.NodeCommand{
		Slug:      node_command.SlugApplyUtilityConfig,
		AccountID: accountID,
		NodeID:    canaryNode.ID,
		Payload: map[string]interface{}{
			"union_hash": "h",
			"utilities":  []interface{}{},
			"canary": map[string]interface{}{
				"assignment_id": assignment.ID.String(),
				"user_id":       uuid.New().String(),
			},
		},
	}
	svc.SettleApply(context.Background(), cmd, string(node_command.StatusFailed), map[string]string{})

	time.Sleep(150 * time.Millisecond)
	if got := len(dispatcher.snapshot()); got != 0 {
		t.Fatalf("a failed canary dispatched %d commands, want 0", got)
	}
}
