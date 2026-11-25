package workflow_execution

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
)

// The wound this file closes: node.Service.Delete cleans adoption codes,
// tracked files and file versions; workflow_execution_nodes are settled here
// (AbandonNodeExecutions). Left alone, an execution node belonging to a
// deleted device stays `pending` or `running` for good — its only other
// settler runs on RECONNECT,
// which for a deleted node never happens. The execution keeps `completed_at
// NULL`, so it counts against WORKFLOW_MAX_ACTIVE_EXECUTIONS_PER_ACCOUNT
// forever AND is never swept by retention, which keys on completed_at. A
// cap's worth of such runs would end playbooks for the account with no API
// remedy.
//
// The per-block timeout that could rescue it defaults to 0 (no timeout) and
// the editor leaves it blank, so nothing else would notice.

// abandonReasonNodeDeleted is what a settled block says. Written into the
// execution node's error message, so the run's own page explains itself rather
// than showing a step that simply stopped.
const abandonReasonNodeDeleted = "the target node was deleted while this run was in flight"

// abandonReasonCancelled is the operator escape hatch's reason.
const abandonReasonCancelled = "the run was cancelled by an operator"

// AbandonNodeExecutions settles every non-terminal execution node belonging to a
// node that no longer exists, then re-evaluates each affected execution so it can
// finish.
//
// Returns how many execution nodes were settled. Errors are returned but the
// caller (a node delete) must treat them as non-fatal: the node row is already
// gone, and refusing the delete because its bookkeeping failed would leave the
// operator with neither the node nor a way to retry.
func (s *Service) AbandonNodeExecutions(ctx context.Context, accountID, nodeID uuid.UUID) (int, error) {
	// Unfinished rows only: settleExecutionNodes skips terminal ones anyway, and
	// a node that has been running a frequent playbook has an unbounded number
	// of them.
	execNodes, err := s.executionNodeRepo.GetActiveByNodeID(ctx, accountID, nodeID)
	if err != nil {
		return 0, fmt.Errorf("failed to list execution nodes for a deleted node: %w", err)
	}
	return s.settleExecutionNodes(ctx, execNodes, abandonReasonNodeDeleted), nil
}

// settleExecutionNodes fails each non-terminal execution node and re-evaluates
// every execution it touched. Shared by the node-delete path and the operator
// cancel, because the mechanics are identical — only the recorded reason differs.
//
// Terminal nodes are left alone: a run that already finished must not have its
// recorded outcome rewritten by a later delete.
func (s *Service) settleExecutionNodes(
	ctx context.Context,
	execNodes []workflow_execution_node.WorkflowExecutionNode,
	reason string,
) int {
	// executionID -> accountID. Every read below is account-scoped (there is no
	// unscoped getter, deliberately — a tenant boundary is not a thing to add a
	// bypass for), and every execution node carries the account it belongs to.
	affected := make(map[uuid.UUID]uuid.UUID)
	settled := 0

	for i := range execNodes {
		execNode := execNodes[i]
		if execNode.Status != workflow_execution_node.StatusPending &&
			execNode.Status != workflow_execution_node.StatusRunning {
			continue
		}

		// Claim the row BEFORE touching anything else.
		//
		// This was a full-row Update, which writes by primary key and honours no
		// status condition, so a worker committing `completed` in the window
		// between the read above and this write was rewritten to `failed` — and
		// its outputs with it, including a capture_image's image_id, which names
		// an object that exists. UpdateActive is the guard the engine's own
		// writes already use: a locked read plus a status re-check in one
		// transaction, so whichever commits a terminal status first wins and the
		// loser leaves the recorded outcome intact.
		//
		// The in-flight command is settled only AFTER the claim succeeds. The
		// old order failed the device command first, so a block that had just
		// finished had its successful command marked failed even though the
		// block's own outcome then survived.
		//
		// The blocks still marked running are failed in the SAME write, so the
		// run view stops showing them as running; their commands are settled
		// below, once the claim is known to be ours.
		inFlight := execNode.FailRunningGraphNodes(reason)
		execNode.Fail(reason)
		wrote, err := s.executionNodeRepo.UpdateActive(ctx, &execNode)
		if err != nil {
			s.logger.WarnContext(ctx, "failed to settle an abandoned execution node",
				"execution_node_id", execNode.ID, "execution_id", execNode.ExecutionID, "error", err)
			continue
		}
		if !wrote {
			// A worker settled it first. Its outcome stands, and the execution
			// still needs re-evaluating so the run can finish.
			s.logger.InfoContext(ctx, "execution node finished before it could be abandoned; keeping its own outcome",
				"execution_node_id", execNode.ID, "execution_id", execNode.ExecutionID)
			affected[execNode.ExecutionID] = execNode.AccountID
			continue
		}

		// Without this the command row stays `registered`/`sent`/`in_progress`
		// and the node stays in maintenance: the queue-expiry sweep only retires
		// queueable commands, the reconnect settler needs a reconnect, and a
		// device still executing reports whenever it finishes. It also stops a
		// redelivery firing later for a run that is already over.
		s.settleInFlightCommands(ctx, &execNode, inFlight, reason)

		settled++
		affected[execNode.ExecutionID] = execNode.AccountID
		s.notifyExecutionNodeUpdated(&execNode)
	}

	// Re-evaluate each execution once, not once per node: updateExecutionStatus
	// reads every node of the execution anyway, so per-node calls would be N
	// identical passes and N websocket events for one outcome.
	for executionID, accountID := range affected {
		s.refreshExecution(ctx, accountID, executionID)
	}
	return settled
}

// settleInFlightCommands fails the node commands the stopped blocks were
// waiting on, so the command history shows why the run stopped instead of a
// step that is eternally "sent" or "in progress", and the node leaves the
// maintenance pin the command set. The device is not told — the platform has
// no way to stop a command already executing — and a report it sends later is
// refused because the command is already terminal.
func (s *Service) settleInFlightCommands(
	ctx context.Context,
	execNode *workflow_execution_node.WorkflowExecutionNode,
	commandIDs []uuid.UUID,
	reason string,
) {
	if s.nodeCommandService == nil {
		return
	}
	for _, commandID := range commandIDs {
		// Not the workflow-aware settler: the block is being failed by this
		// function, and letting the command's own completion path also drive
		// the block would race with the Fail above.
		if err := s.nodeCommandService.FailCommandOfStoppedRun(ctx, commandID, reason); err != nil {
			s.logger.WarnContext(ctx, "failed to settle the command of an abandoned block",
				"command_id", commandID, "execution_node_id", execNode.ID, "error", err)
		}
	}
}

// refreshExecution reloads an execution and re-runs the status evaluation that
// stamps completed_at — the field the active-executions cap and the retention
// sweep both key on.
func (s *Service) refreshExecution(ctx context.Context, accountID, executionID uuid.UUID) {
	execution, err := s.executionRepo.GetByID(ctx, accountID, executionID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to reload an execution after settling its nodes",
			"execution_id", executionID, "error", err)
		return
	}
	if execution.CompletedAt != nil {
		return
	}
	s.updateExecutionStatus(ctx, execution)
}

// CancelExecution is the operator escape hatch: it settles a run that is stuck
// for ANY reason, not only the deleted-node one.
//
// It exists because the deleted-node strand had no remedy at all — no DELETE,
// no force-complete — so an account that hit it lost playbooks permanently
// with nothing to call. A mechanism-specific fix would have left the next
// mechanism (a dropped enqueue, a wedged worker, a block whose agent never
// answers) equally
// unrecoverable.
//
// Idempotent: cancelling an already-finished run is a no-op and reports 0, so a
// double-click cannot rewrite a recorded outcome.
func (s *Service) CancelExecution(ctx context.Context, accountID, executionID uuid.UUID) (int, error) {
	execution, err := s.executionRepo.GetByID(ctx, accountID, executionID)
	if err != nil {
		return 0, err
	}
	if execution.CompletedAt != nil {
		return 0, nil
	}

	execNodes, err := s.executionNodeRepo.GetByExecutionID(ctx, executionID)
	if err != nil {
		return 0, fmt.Errorf("failed to list execution nodes: %w", err)
	}
	settled := s.settleExecutionNodes(ctx, execNodes, abandonReasonCancelled)

	// A run whose nodes were ALL already terminal is stuck for a different
	// reason — the finalize never ran, or ran against a stale read. Completing it
	// here is the point of an escape hatch: it must free the run whatever pinned
	// it, not only the cases we predicted.
	if settled == 0 {
		s.forceComplete(ctx, accountID, execution.ID)
	}
	return settled, nil
}

// forceComplete stamps completed_at on a run whose nodes are all terminal but
// which was never finalized.
func (s *Service) forceComplete(ctx context.Context, accountID, executionID uuid.UUID) {
	reloaded, err := s.executionRepo.GetByID(ctx, accountID, executionID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to reload an execution before force-completing",
			"execution_id", executionID, "error", err)
		return
	}
	if reloaded.CompletedAt != nil {
		return
	}
	// Try the ordinary evaluation first, so a genuinely finishable run gets its
	// real outcome (completed / failed / partially_failed) rather than a blanket
	// failure.
	s.updateExecutionStatus(ctx, reloaded)

	again, err := s.executionRepo.GetByID(ctx, accountID, executionID)
	if err != nil || again.CompletedAt != nil {
		return
	}
	again.Complete(workflow_execution.StatusPartiallyFailed)
	if err := s.executionRepo.Update(ctx, again); err != nil {
		s.logger.WarnContext(ctx, "failed to force-complete an execution",
			"execution_id", executionID, "error", err)
		return
	}
	s.logger.InfoContext(ctx, "force-completed a stuck execution", "execution_id", executionID)
	s.notifyExecutionUpdated(again)
}
