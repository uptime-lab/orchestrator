package workflow_execution

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
)

// The lost-pass guard only caught a still-ACTIVE orphan, so a
// dispatched command that settled terminal before the engine re-entered passed
// the guard as "nothing in flight" — and the pass then re-advanced through the
// completed predecessor and dispatched the same block a second time. The
// window is not only a crash race: finalize's UpdateActive failing loses the
// pass without failing the node, and the orphan's own terminal report
// re-enqueues the node, so the duplicate was deterministic.
//
// Every engine dispatch now records its block id in the command payload, and
// the reconciler ADOPTS the orphan back into its block instead.

func blockPayload(blockID string) map[string]interface{} {
	return map[string]interface{}{results.WorkflowBlockIDKey: blockID}
}

// The repro: b1's pass completed and dispatched b2; the pass died before
// finalize; b2's command then SUCCEEDED on the device. The old guard saw
// nothing in flight and dispatched b2 again — a second reboot.
func TestLostTerminalDispatchIsAdoptedNotRepeated(t *testing.T) {
	recorded, lost := uuid.New(), uuid.New()
	startedAt := time.Now().Add(-time.Minute)
	state := map[string]wen.GraphNodeState{
		"start": {Status: wen.GraphNodeCompleted},
		"b1":    {Status: wen.GraphNodeRunning, CommandID: &recorded, StartedAt: &startedAt},
	}
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{
		{ID: recorded, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSuccess,
			CreatedAt: time.Now().Add(-2 * time.Minute)},
		{ID: lost, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSuccess,
			CreatedAt: time.Now().Add(-time.Minute), Payload: blockPayload("b2")},
	}}

	svc, nodeRepo, nodeSvc, execNodeID := midRunFixture(cmdRepo, wen.StatusRunning, state)
	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeSvc.dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0: the device already ran b2's command — a second dispatch is a second reboot", nodeSvc.dispatched)
	}
	if nodeRepo.written == nil {
		t.Fatal("nothing was persisted")
	}
	if nodeRepo.written.Status != wen.StatusCompleted {
		t.Fatalf("execution node = %s (%q), want completed: both blocks in fact succeeded",
			nodeRepo.written.Status, nodeRepo.written.ErrorMessage)
	}
	if st := nodeRepo.written.GraphState["b2"]; st.Status != wen.GraphNodeCompleted || st.CommandID == nil || *st.CommandID != lost {
		t.Fatalf("b2 state = %+v, want completed under the adopted command %s", st, lost)
	}
}

// A trap: the retry path overwrites GraphState.CommandID, so
// every retried block legitimately strands its FAILED earlier attempt as a
// terminal unrecorded command. Adopting that would re-fail a block its retry
// already saved.
func TestRetiredRetryAttemptIsNotAdopted(t *testing.T) {
	oldAttempt, newAttempt := uuid.New(), uuid.New()
	startedAt := time.Now().Add(-time.Minute)
	state := map[string]wen.GraphNodeState{
		"start": {Status: wen.GraphNodeCompleted},
		"b1":    {Status: wen.GraphNodeRunning, CommandID: &newAttempt, StartedAt: &startedAt, Attempts: 2},
	}
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{
		// The retired first attempt: failed, unrecorded, OLDER than the
		// recorded retry.
		{ID: oldAttempt, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusFailed,
			CreatedAt: time.Now().Add(-2 * time.Minute), Payload: blockPayload("b1")},
		// The recorded retry, still on its way.
		{ID: newAttempt, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSent,
			CreatedAt: time.Now().Add(-time.Minute), Payload: blockPayload("b1")},
	}}

	svc, nodeRepo, nodeSvc, execNodeID := midRunFixture(cmdRepo, wen.StatusRunning, state)
	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeSvc.dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0", nodeSvc.dispatched)
	}
	if nodeRepo.written == nil {
		t.Fatal("nothing was persisted")
	}
	if st := nodeRepo.written.GraphState["b1"]; st.CommandID == nil || *st.CommandID != newAttempt {
		t.Fatalf("b1's command = %v, want the recorded retry %s kept — adopting the retired attempt re-fails a block its retry saved", st.CommandID, newAttempt)
	}
	if nodeRepo.written.Status == wen.StatusFailed {
		t.Fatalf("the run was failed: %q", nodeRepo.written.ErrorMessage)
	}
}

// The inverse: the RETRY dispatch itself was the lost one. Stored state still
// records the old failed attempt; the newer command is the block's real story
// and must replace it — otherwise progress re-reads the old failure and
// dispatches attempt three while attempt two's outcome is discarded.
func TestLostRetryDispatchReplacesTheStaleRecordedAttempt(t *testing.T) {
	oldAttempt, lostRetry := uuid.New(), uuid.New()
	startedAt := time.Now().Add(-2 * time.Minute)
	state := map[string]wen.GraphNodeState{
		"start": {Status: wen.GraphNodeCompleted},
		"b1":    {Status: wen.GraphNodeRunning, CommandID: &oldAttempt, StartedAt: &startedAt, Attempts: 1},
	}
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{
		{ID: oldAttempt, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusFailed,
			CreatedAt: time.Now().Add(-3 * time.Minute), Payload: blockPayload("b1")},
		// The lost retry, which then SUCCEEDED on the device.
		{ID: lostRetry, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSuccess,
			CreatedAt: time.Now().Add(-time.Minute), Payload: blockPayload("b1")},
	}}

	svc, nodeRepo, nodeSvc, execNodeID := midRunFixture(cmdRepo, wen.StatusRunning, state)
	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeRepo.written == nil {
		t.Fatal("nothing was persisted")
	}
	if st := nodeRepo.written.GraphState["b1"]; st.Status != wen.GraphNodeCompleted || st.CommandID == nil || *st.CommandID != lostRetry {
		t.Fatalf("b1 = %+v, want completed under the adopted retry %s", st, lostRetry)
	}
	// b1's success advances the walk, so b2's dispatch is the ONE legitimate
	// send here — the lost retry itself must not be re-sent.
	if nodeSvc.dispatched != 1 {
		t.Fatalf("dispatched = %d, want exactly the advance to b2", nodeSvc.dispatched)
	}
}

// A lost FIRST pass: pending node, empty state, and the first block's command
// already succeeded. Adoption must also redo begin()'s lost bookkeeping — the
// trigger's completed mark — or a sibling branch the lost pass never reached
// would read unreachable instead of stalled and the run could finish green
// with blocks it never entered.
func TestLostFirstDispatchIsAdoptedFromPending(t *testing.T) {
	lost := uuid.New()
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{
		{ID: lost, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSuccess,
			CreatedAt: time.Now().Add(-time.Minute), Payload: blockPayload("b1")},
	}}

	svc, nodeRepo, nodeSvc, execNodeID := midRunFixture(cmdRepo, wen.StatusPending, map[string]wen.GraphNodeState{})
	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeRepo.written == nil {
		t.Fatal("nothing was persisted")
	}
	if st := nodeRepo.written.GraphState["start"]; st.Status != wen.GraphNodeCompleted {
		t.Fatalf("trigger state = %+v, want completed — without it the stall check goes blind", st)
	}
	if st := nodeRepo.written.GraphState["b1"]; st.Status != wen.GraphNodeCompleted {
		t.Fatalf("b1 = %+v, want completed under the adopted command", st)
	}
	// b1's success advances to b2: one legitimate dispatch, not a repeat of b1.
	if nodeSvc.dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1 (the advance to b2 only)", nodeSvc.dispatched)
	}
}

// An ACTIVE orphan that carries its block id no longer fails the run: the
// block adopts it and waits, which is strictly better than the old fail-closed
// answer — the command is real and the run can still finish honestly.
func TestActiveOrphanWithABlockIDIsAdoptedAndWaitedOn(t *testing.T) {
	recorded, lost := uuid.New(), uuid.New()
	startedAt := time.Now().Add(-time.Minute)
	state := map[string]wen.GraphNodeState{
		"start": {Status: wen.GraphNodeCompleted},
		"b1":    {Status: wen.GraphNodeRunning, CommandID: &recorded, StartedAt: &startedAt},
	}
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{
		{ID: recorded, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSuccess,
			CreatedAt: time.Now().Add(-2 * time.Minute)},
		{ID: lost, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSent,
			CreatedAt: time.Now().Add(-time.Minute), Payload: blockPayload("b2")},
	}}

	svc, nodeRepo, nodeSvc, execNodeID := midRunFixture(cmdRepo, wen.StatusRunning, state)
	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeSvc.dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0: b2's command is already on the device", nodeSvc.dispatched)
	}
	if nodeRepo.written == nil {
		t.Fatal("nothing was persisted")
	}
	if nodeRepo.written.Status == wen.StatusFailed {
		t.Fatalf("the run was failed for a reconcilable orphan: %q", nodeRepo.written.ErrorMessage)
	}
	if st := nodeRepo.written.GraphState["b2"]; st.Status != wen.GraphNodeRunning || st.CommandID == nil || *st.CommandID != lost {
		t.Fatalf("b2 = %+v, want running under the adopted command %s", st, lost)
	}
}
