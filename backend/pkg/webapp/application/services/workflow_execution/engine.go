package workflow_execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	node_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// engineRun is one pass of the graph engine over a single execution node. It
// mutates the node's GraphState in place; the caller persists and notifies
// once the pass settles.
type engineRun struct {
	svc       *Service
	ctx       context.Context
	execNode  *wen.WorkflowExecutionNode
	execution *workflow_execution.WorkflowExecution
	template  *workflow_template.WorkflowTemplate
	graph     *workflow_template.WorkflowGraph
	rc        resolveContext
	failed    bool
	// abandoned records that the stored row was settled — cancelled, or its node
	// deleted — while this pass was in flight. It stops the walk WITHOUT writing
	// anything: unlike `failed`, this run's outcome is not ours to record.
	abandoned bool
}

// processNodeGraph advances one execution node by walking the pinned graph:
// it starts the trigger's successors on first entry, and on later entries
// progresses running blocks (agent commands and timed waits), cascading
// through server-executed blocks synchronously.
func (s *Service) processNodeGraph(ctx context.Context, executionNodeID uuid.UUID, workerID int) {
	execNode, err := s.executionNodeRepo.GetByID(ctx, executionNodeID)
	if err != nil {
		s.logger.Error("failed to get execution node", "worker_id", workerID, "execution_node_id", executionNodeID, "error", err)
		return
	}
	if execNode.Status == wen.StatusCompleted || execNode.Status == wen.StatusFailed {
		return
	}

	execution, err := s.executionRepo.GetByID(ctx, execNode.AccountID, execNode.ExecutionID)
	if err != nil {
		s.logger.Error("failed to get execution", "worker_id", workerID, "execution_id", execNode.ExecutionID, "error", err)
		return
	}

	// Run against the snapshot pinned at start; fall back to the live template
	// only for executions created before snapshotting existed.
	var template *workflow_template.WorkflowTemplate
	if execution.TemplateSnapshot != nil {
		template = execution.TemplateSnapshot.ToTemplate(execNode.AccountID)
	} else {
		template, err = s.templateRepo.GetByID(ctx, execNode.AccountID, execution.TemplateID)
		if err != nil {
			s.logger.Error("failed to get template", "worker_id", workerID, "template_id", execution.TemplateID, "error", err)
			return
		}
	}

	run := &engineRun{
		svc:       s,
		ctx:       ctx,
		execNode:  execNode,
		execution: execution,
		template:  template,
		graph:     template.EffectiveGraph(),
	}
	if execNode.GraphState == nil {
		execNode.GraphState = map[string]wen.GraphNodeState{}
	}

	rc, err := s.buildResolveContext(ctx, execNode)
	if err != nil {
		// Fail CLOSED and visibly. Proceeding with an empty resolve context is the
		// worst possible outcome: {{secrets.*}} and {{node.attributes.*}} substitute
		// to "", so write_file/run_script/http_request are dispatched to the device
		// with blank credentials and a control.condition takes the branch that reads
		// a missing fact — with no log and no failure, the run goes green. A failed
		// run the operator can see beats a green run that did the wrong thing.
		// The error is real by construction: ResolveEffectiveProvisioning
		// degrades benign cases (dangling key/OU, undecryptable password) to flags and
		// empties, so a non-nil error is a genuine node-read or DB/decryption failure.
		s.logger.Error("failed to resolve run context; failing the execution node",
			"worker_id", workerID, "execution_node_id", execNode.ID, "error", err)
		execNode.Fail(fmt.Sprintf("could not resolve secrets or node facts for this run: %v", err))
		run.failed = true
		run.finalize(workerID)
		return
	}
	run.rc = rc

	// A pass that died between dispatching and persisting leaves a live device
	// command the stored state knows nothing about: dispatchAgent registers the
	// command immediately, while the block's GraphState is written only at
	// finalize. A process death or a discarded UpdateActive in between, plus the
	// restart re-enqueue, re-enters this function and sends the command a SECOND
	// time. Device-exclusivity catches install_os, erase_drive and capture_image;
	// it does not catch run_script, install_packages, download_file,
	// manage_service, write_file or reboot.
	//
	// The check runs on EVERY pass, not only the first. It was gated on "pending
	// with no graph state", which is the run's first block alone — so a lost pass
	// mid-run left the stored state saying the previous block was running, the
	// next enqueue took the progress() branch, and it dispatched the NEXT block
	// a second time with nothing looking.
	//
	// "Unrecorded" is what makes it generic: a live command whose id appears in
	// no GraphState entry cannot be the one a running block is waiting on, so it
	// can only be the residue of a pass that did not finish.
	if adopted, unattributed, err := run.reconcileDispatches(); err != nil {
		// Fail CLOSED. We cannot tell whether a command is already on its way
		// to the device, and the cost of guessing wrong is a duplicated
		// reboot or run_script. Registering the next command needs this same
		// database anyway, so proceeding would very likely fail one step later
		// with a less honest message.
		s.logger.ErrorContext(ctx, "cannot check for an earlier dispatch; refusing to start blocks",
			"worker_id", workerID, "execution_node_id", execNode.ID, "error", err)
		execNode.Fail("could not verify whether an earlier attempt already dispatched a command; refusing to send a duplicate")
		run.failed = true
	} else if unattributed != nil {
		// A live command we cannot tie to a block (dispatched by a build that
		// predates the block-id bookkeeping). Adoption is impossible, so the
		// old fail-closed answer stands: a failed run the operator can re-run
		// beats a duplicated device command.
		s.logger.ErrorContext(ctx, "execution node was re-entered after a lost pass; refusing to re-dispatch",
			"worker_id", workerID, "execution_node_id", execNode.ID,
			"command_id", unattributed.ID, "command", unattributed.Slug, "command_status", unattributed.Status)
		execNode.Fail(fmt.Sprintf(
			"an earlier attempt already dispatched '%s' (command %s, %s) and its progress was lost; not sending it again — re-run the playbook once that command has settled",
			unattributed.Slug, unattributed.ID, unattributed.Status))
		run.failed = true
	} else if adopted > 0 {
		// A lost pass was reconciled: its dispatches are back in GraphState as
		// the running commands of their blocks, and the ordinary progress path
		// settles them — outputs, retry policy, superseded/expired handling —
		// exactly as if the pass had persisted.
		if execNode.Status == wen.StatusPending {
			// The lost pass was the FIRST one, so begin()'s bookkeeping was
			// lost with it. Redo the parts that do not dispatch: without the
			// trigger marked completed, a sibling branch the lost pass never
			// reached would look unreachable rather than stalled, and the run
			// could finish green with blocks it silently never entered.
			execNode.Status = wen.StatusRunning
			now := time.Now()
			execNode.StartedAt = &now
			if trigger, ok := run.graph.TriggerNode(); ok {
				execNode.MarkGraphNodeCompleted(trigger.ID)
			}
		}
		run.progress()
	} else if execNode.Status == wen.StatusPending && len(execNode.GraphState) == 0 {
		run.begin()
	} else {
		run.progress()
	}
	run.finalize(workerID)
}

// reconcileDispatches accounts for every command dispatched for this execution
// node that the stored graph state knows nothing about.
//
// A running block records its command id in GraphState, so a command that IS
// recorded is the ordinary case — the run is waiting on it, and progress()
// handles it. An UNRECORDED command is the residue of a pass that dispatched
// and then died before finalize persisted the block. This used to be handled
// with two blunt answers — fail the run for an active orphan, look away from a
// terminal one — and the second answer dispatched the same device command
// twice: the orphan's own terminal report re-enqueues the node, the new pass
// sees "nothing in flight", and re-advances through the completed predecessor
// into a second dispatch of the same block.
//
// Every engine dispatch now records its BLOCK id in the command payload, so an
// orphan can instead be ADOPTED: written back into its block's GraphState as
// the running command, after which the ordinary progress path settles it —
// outputs, retry policy, superseded/expired handling — exactly as if the lost
// pass had persisted. Three shapes remain unadoptable, each deliberately:
//
//   - a TERMINAL orphan whose block already settled, or whose block records a
//     NEWER command: that is the ordinary residue of the retry path, which
//     overwrites GraphState.CommandID and legitimately strands the failed
//     attempt (adopting it would re-fail a block its retry already saved);
//   - a TERMINAL orphan with no block id (a pre-bookkeeping build, or a
//     command from before this run's engine was restarted across the deploy):
//     not evidence of anything in flight — the pinned superseded/expired
//     semantics — so it is ignored, exactly as before;
//   - an ACTIVE orphan with no block id: adoption is impossible and ignoring
//     it dispatches a duplicate, so it is returned as unattributed and the
//     caller fails closed, exactly as before.
func (r *engineRun) reconcileDispatches() (adopted int, unattributed *node_command_domain.NodeCommand, err error) {
	cmds, err := r.svc.nodeCommandRepo.GetByWorkflowExecutionNodeID(r.ctx, r.execNode.ID)
	if err != nil {
		return 0, nil, err
	}
	recorded := make(map[uuid.UUID]bool, len(r.execNode.GraphState))
	for _, st := range r.execNode.GraphState {
		if st.CommandID != nil {
			recorded[*st.CommandID] = true
		}
	}
	byID := make(map[uuid.UUID]*node_command_domain.NodeCommand, len(cmds))
	for i := range cmds {
		byID[cmds[i].ID] = &cmds[i]
	}

	// Newest orphan per block: two lost passes for one block can only both
	// matter as history, and the newer one is the block's real story.
	newestByBlock := map[string]*node_command_domain.NodeCommand{}
	for i := range cmds {
		c := &cmds[i]
		if recorded[c.ID] {
			continue
		}
		blockID, _ := c.Payload[results.WorkflowBlockIDKey].(string)
		node, inGraph := r.graph.NodeByID(blockID)
		block, _ := workflow_block.FindByType(node.Type)
		if blockID == "" || !inGraph || !block.IsAgent() {
			if c.Status.IsActive() && unattributed == nil {
				unattributed = c
			}
			continue
		}
		if cur := newestByBlock[blockID]; cur == nil || c.CreatedAt.After(cur.CreatedAt) {
			newestByBlock[blockID] = c
		}
	}
	if unattributed != nil {
		return 0, unattributed, nil
	}

	for blockID, c := range newestByBlock {
		st, exists := r.execNode.GraphState[blockID]
		if exists && st.Status != "" && st.Status != wen.GraphNodeRunning {
			continue // the block settled; this orphan is history
		}
		if exists && st.CommandID != nil {
			if rec, ok := byID[*st.CommandID]; ok && !rec.CreatedAt.Before(c.CreatedAt) {
				continue // the recorded command is the newer attempt; the orphan is a retired retry
			}
		}
		id := c.ID
		startedAt := c.CreatedAt
		st.Status = wen.GraphNodeRunning
		st.CommandID = &id
		// The dispatch moment, not now(): the block's policy timeout must
		// count from when the device was actually told, or a lost pass buys
		// the command a fresh deadline every time it is rediscovered.
		st.StartedAt = &startedAt
		st.Attempts++
		r.execNode.GraphState[blockID] = st
		r.svc.logger.InfoContext(r.ctx, "adopted a dispatch from a lost pass into its block",
			"execution_node_id", r.execNode.ID, "block", blockID,
			"command_id", id, "command_status", c.Status)
		adopted++
	}
	return adopted, nil, nil
}

// buildResolveContext gathers node facts, effective secrets and prior block
// outputs for {{...}} resolution.
// buildResolveContext returns an error rather than swallowing one: an empty
// resolve context is not a safe default here, because the engine substitutes it
// straight into device commands and condition branches. Only a genuine failure
// reaches this return — the node read failing, or ResolveEffectiveProvisioning
// failing, which it does only for a real DB/decryption/key error (benign cases
// like a dangling key or an undecryptable password degrade to flags and empties).
func (s *Service) buildResolveContext(ctx context.Context, execNode *wen.WorkflowExecutionNode) (resolveContext, error) {
	rc := resolveContext{
		nodeFacts:  map[string]string{},
		secrets:    map[string]string{},
		outputs:    execNode.GraphState,
		logger:     s.logger,
		execNodeID: execNode.ID,
	}
	n, err := s.nodeRepo.GetByID(ctx, execNode.AccountID, execNode.NodeID)
	if err != nil {
		return resolveContext{}, fmt.Errorf("reading node facts: %w", err)
	}
	rc.nodeFacts = nodeFactsOf(n)
	if s.secretsResolver != nil {
		eff, err := s.secretsResolver.ResolveEffectiveProvisioning(ctx, execNode.AccountID, execNode.NodeID)
		if err != nil {
			return resolveContext{}, fmt.Errorf("resolving effective secrets: %w", err)
		}
		for _, sec := range eff.Secrets {
			rc.secrets[sec.Name] = sec.Value
			// AD-style custom attributes surface under {{node.attributes.<key>}}
			// (in addition to {{secrets.<name>}}). They are node-attached
			// secrets whose reserved name encodes the running node + key.
			if key, ok := secret_domain.ParseNodeAttributeKey(sec.Name, execNode.NodeID); ok {
				rc.nodeFacts["attributes."+key] = sec.Value
			}
		}
	}
	return rc, nil
}

// begin marks the run started and enters the trigger's successors.
func (r *engineRun) begin() {
	if r.execution.Status == workflow_execution.StatusPending {
		// Two columns, not the whole row. This copy was read by a worker the
		// creation loop enqueued BEFORE the node count was known, so a full-row
		// write reverted node_count to 0 — a 50-node run then reported "0 nodes"
		// for the rest of its life. MarkStarted also returns the stored row, so
		// what this run holds (and what the frontend is told) carries the count
		// rather than the copy's stale zero.
		started := time.Now()
		if stored, err := r.svc.executionRepo.MarkStarted(r.ctx, r.execution.AccountID, r.execution.ID, started); err == nil && stored != nil {
			r.execution = stored
			r.svc.notifyExecutionUpdated(r.execution)
		} else if err != nil {
			r.svc.logger.WarnContext(r.ctx, "failed to mark the execution started",
				"execution_id", r.execution.ID, "error", err)
		}
	}
	r.execNode.Status = wen.StatusRunning
	now := time.Now()
	r.execNode.StartedAt = &now

	trigger, ok := r.graph.TriggerNode()
	if !ok {
		r.execNode.Fail("graph has no trigger block")
		r.failed = true
		return
	}
	r.execNode.MarkGraphNodeCompleted(trigger.ID)
	r.advanceFrom(trigger.ID, workflow_block.DefaultOutputPort)
}

// progress inspects every running block and advances the ones that finished.
func (r *engineRun) progress() {
	// Snapshot ids to iterate deterministically (map mutates during advance).
	var running []string
	for id, st := range r.execNode.GraphState {
		if st.Status == wen.GraphNodeRunning {
			running = append(running, id)
		}
	}
	for _, id := range running {
		if r.failed || r.abandoned {
			return
		}
		st := r.execNode.GraphState[id]
		if st.Status != wen.GraphNodeRunning {
			continue // already advanced this pass
		}
		node, ok := r.graph.NodeByID(id)
		if !ok {
			continue
		}
		block, _ := workflow_block.FindByType(node.Type)

		if block.IsAgent() {
			r.progressAgentBlock(id, node, st)
			continue
		}
		if node.Type == workflow_block.TypeWait {
			if st.WaitUntil != nil && !time.Now().Before(*st.WaitUntil) {
				r.completeAndAdvance(id, nil, workflow_block.DefaultOutputPort)
			} else if st.WaitUntil != nil {
				r.svc.scheduleReenqueue(r.execNode.ID, time.Until(*st.WaitUntil))
			}
		}
		if node.Type == workflow_block.TypeWaitForNode {
			r.progressWaitForNode(id, node, st)
		}
	}
}

func (r *engineRun) progressAgentBlock(id string, node workflow_template.GraphNode, st wen.GraphNodeState) {
	if st.CommandID == nil {
		return
	}
	cmd, err := r.svc.nodeCommandRepo.GetByIDForUpdate(r.ctx, *st.CommandID)
	if err != nil {
		return
	}
	switch cmd.Status {
	case node_command_domain.StatusRegistered, node_command_domain.StatusSent, node_command_domain.StatusInProgress:
		if r.blockTimedOut(node, st) {
			// Take back a command that was never sent. A queued command whose
			// block already gave up is not "late", it is a different event: a
			// reboot delivered hours after its run failed. The conditional
			// update is what makes this safe against the dispatcher — if the
			// command got sent between our read and this write, the update
			// matches nothing and we know it is genuinely in flight.
			cancelled := false
			if cmd.Status == node_command_domain.StatusRegistered {
				ok, cerr := r.svc.nodeCommandRepo.UpdateStatusIfCurrent(r.ctx, cmd.ID,
					node_command_domain.StatusRegistered, node_command_domain.StatusExpired, 0,
					"workflow block timed out before the command was delivered")
				if cerr != nil {
					r.svc.logger.Warn("failed to take back a queued command after block timeout",
						"command_id", cmd.ID, "error", cerr)
				}
				cancelled = cerr == nil && ok
			}
			if cancelled {
				// The device never saw the command, so a retry (per policy) may
				// honestly dispatch a fresh one.
				r.settleFailure(id, node, fmt.Sprintf("block timed out after %ds (queued command %s was taken back before delivery)",
					node.Policy.TimeoutSeconds, cmd.ID), true)
			} else {
				// Sent or in progress: there is no protocol-level cancel, so
				// the command keeps running on the device. A retry here would
				// race it with a SECOND copy of itself — for install_os or
				// erase_drive that is two writers on one disk — so the retry
				// policy is deliberately not honored.
				r.settleFailure(id, node, fmt.Sprintf("block timed out after %ds (command %s is %s and cannot be cancelled on the device; retries are not attempted while it runs)",
					node.Policy.TimeoutSeconds, cmd.ID, cmd.Status), false)
			}
		} else if node.Policy != nil && node.Policy.TimeoutSeconds > 0 && st.StartedAt != nil {
			// The dispatch-time AfterFunc timer does not survive a backend
			// restart (nor a dropped queue push), so re-arm the deadline check
			// whenever we observe a still-running timed block — otherwise an
			// agent that never reports would hang the block forever, the exact
			// case the timeout guards against.
			remaining := time.Until(st.StartedAt.Add(time.Duration(node.Policy.TimeoutSeconds) * time.Second))
			r.svc.scheduleReenqueue(r.execNode.ID, remaining+time.Second)
		}
		return
	case node_command_domain.StatusSuccess:
		r.completeAndAdvance(id, cmd.Result, workflow_block.DefaultOutputPort)
	case node_command_domain.StatusFailed:
		r.handleFailure(id, node, cmd.Details)
	default:
		// superseded / expired / skipped: the command will never report again,
		// so the block must settle now instead of staying running forever.
		r.handleFailure(id, node, fmt.Sprintf("command ended as %s: %s", cmd.Status, cmd.Details))
	}
}

// maxHTTPRequestTimeoutSeconds bounds how long one integration.http_request may
// hold a workflow worker. Ten minutes is far above any real API call and far
// below "indefinitely", which is what the block accepted before.
const maxHTTPRequestTimeoutSeconds = 600

// blockTimedOut reports whether a running block has exceeded its policy
// timeout. The agent command itself is not cancelled; the block just settles.
func (r *engineRun) blockTimedOut(node workflow_template.GraphNode, st wen.GraphNodeState) bool {
	if node.Policy == nil || node.Policy.TimeoutSeconds <= 0 || st.StartedAt == nil {
		return false
	}
	return time.Since(*st.StartedAt) >= time.Duration(node.Policy.TimeoutSeconds)*time.Second
}

// completeAndAdvance records outputs, marks the block completed, follows the
// chosen output port, and skips the branches on the other ports so a
// downstream join isn't left waiting on a branch that will never run.
func (r *engineRun) completeAndAdvance(id string, outputs map[string]string, port string) {
	st := r.execNode.GraphState[id]
	st.Status = wen.GraphNodeCompleted
	now := time.Now()
	st.CompletedAt = &now
	if len(outputs) > 0 {
		st.Outputs = outputs
	}
	st.ChosenPort = port
	r.execNode.GraphState[id] = st
	// Skip the untaken branches BEFORE following the taken one. A join enters
	// only once every predecessor has settled, so a join reached through the
	// taken port while the other branch is still stateless reads as "a
	// predecessor is still pending" and is left alone — and nothing ever
	// revisits it, so the run finishes COMPLETED with everything past the join
	// skipped. Settling the dead branch first makes the join's own check see
	// the truth. The reverse order is only safe when the taken path leaves a
	// block Running, which the simplest branch-and-rejoin graph does not.
	r.skipUntaken(id, port)
	r.advanceFrom(id, port)
}

// skipUntaken marks the subtrees reachable only through this node's
// not-chosen output ports as skipped.
func (r *engineRun) skipUntaken(id, chosenPort string) {
	for _, edge := range r.graph.EdgesFrom(id, "") {
		if edge.Port() != chosenPort {
			r.trySkip(edge.To)
		}
	}
}

// trySkip marks a node skipped when every incoming path is dead (a skipped
// source, or a completed condition that routed away from it), then recurses.
// A join with any still-live incoming path is left alone.
func (r *engineRun) trySkip(nodeID string) {
	if st, ok := r.execNode.GraphState[nodeID]; ok && st.Status != "" {
		return // already running, done, or skipped
	}
	for _, edge := range r.graph.EdgesTo(nodeID) {
		src := r.execNode.GraphState[edge.From]
		dead := src.Status == wen.GraphNodeSkipped
		if src.Status == wen.GraphNodeCompleted && src.ChosenPort != "" && src.ChosenPort != edge.Port() {
			dead = true
		}
		if !dead {
			// A live path can still reach this node, so it must not be skipped. But
			// for a JOIN that live path may already have COMPLETED in an earlier
			// pass — its arrive() returned then because THIS branch was still
			// pending, and now that we have just skipped this branch nothing else
			// will call arrive() for the join again. Re-arrive idempotently so a
			// join whose predecessors are now all settled (one completed, the rest
			// skipped) is entered rather than stranded as "graph stalled".
			// arrive() re-checks join semantics and is a no-op if a predecessor
			// is genuinely still pending.
			if node, ok := r.graph.NodeByID(nodeID); ok && node.Type == workflow_block.TypeJoin {
				r.arrive(nodeID)
			}
			return // a live path can still reach this node
		}
	}
	r.execNode.GraphState[nodeID] = wen.GraphNodeState{Status: wen.GraphNodeSkipped}
	for _, edge := range r.graph.EdgesFrom(nodeID, "") {
		r.trySkip(edge.To)
	}
}

// advanceFrom follows every outgoing edge on the given port to its target.
func (r *engineRun) advanceFrom(nodeID, port string) {
	for _, edge := range r.graph.EdgesFrom(nodeID, port) {
		if r.failed || r.abandoned {
			return
		}
		r.arrive(edge.To)
	}
}

// arrive enters a target block, honoring join semantics.
func (r *engineRun) arrive(targetID string) {
	st, seen := r.execNode.GraphState[targetID]
	if seen && st.Status != "" {
		return // already running or done (idempotent for joins)
	}
	node, ok := r.graph.NodeByID(targetID)
	if !ok {
		return
	}
	if node.Type == workflow_block.TypeJoin {
		anyCompleted := false
		for _, pred := range r.graph.EdgesTo(targetID) {
			switch r.execNode.GraphState[pred.From].Status {
			case wen.GraphNodeCompleted:
				anyCompleted = true
			case wen.GraphNodeSkipped:
				// settled but dead — doesn't block the join
			default:
				return // a predecessor is still pending
			}
		}
		if !anyCompleted {
			return // all predecessors skipped: this join is itself dead
		}
	}
	r.startBlock(targetID, node)
}

// settledElsewhere re-reads the stored row and reports whether this execution
// node has been settled since this pass began.
//
// The pass holds a copy read at its start, and a pass is not instantaneous: one
// `integration.http_request` may park it for up to 600 s. An operator who
// cancels during that window has every reason to expect no further work — and
// the work in question includes `erase_drive`, `install_os` and `reboot`
// against real hardware. `UpdateActive` stops the
// stale copy from RESURRECTING the settled row afterwards, but a discarded
// write does not un-send a device command; only not sending it does.
//
// A read error other than "gone" is deliberately NOT treated as settled: a
// database blip must not silently stop every run in flight, and the same blip
// will fail the finalize write anyway. A row that has VANISHED is settled by
// definition — there is nothing left to run for.
func (r *engineRun) settledElsewhere() bool {
	stored, err := r.svc.executionNodeRepo.GetByID(r.ctx, r.execNode.ID)
	if err != nil {
		if errors.Is(err, app_errors.ErrNotFound) {
			return true
		}
		r.svc.logger.WarnContext(r.ctx, "could not re-read an execution node before starting a block; proceeding",
			"execution_node_id", r.execNode.ID, "error", err)
		return false
	}
	return stored.Status == wen.StatusCompleted || stored.Status == wen.StatusFailed
}

// runActor is who a server block's write is recorded against: the member who
// started the run, or for a scheduled run the playbook's author — the member
// whose right the scheduler re-checks before every fire, so the one the
// unattended run acts for. Only a run with neither (a scheduled playbook with
// no recorded author, which the scheduler fires only when nothing in it is
// privileged) is the platform's own.
func (r *engineRun) runActor() audit.Actor {
	accountID := r.execNode.AccountID
	if r.execution != nil && r.execution.StartedBy != nil && *r.execution.StartedBy != uuid.Nil {
		return audit.UserActor(accountID, *r.execution.StartedBy)
	}
	if r.template != nil && r.template.AuthorID != nil && *r.template.AuthorID != uuid.Nil {
		return audit.UserActor(accountID, *r.template.AuthorID)
	}
	return audit.SystemActor(accountID)
}

// startBlock dispatches an agent block or executes a server block inline.
func (r *engineRun) startBlock(id string, node workflow_template.GraphNode) {
	// Asked before EVERY block rather than only after the ones known to block.
	// A list of "blocks that can park a pass" is a list someone has to remember
	// to extend, and the cost of being wrong is a device command dispatched for
	// a cancelled run; the cost of being right is one indexed row read per block
	// started. It also closes the narrower window where a cancel lands between
	// the top-of-pass read and the first dispatch.
	if r.settledElsewhere() {
		r.svc.logger.InfoContext(r.ctx, "execution node was settled while this pass ran; not starting further blocks",
			"execution_node_id", r.execNode.ID, "block", id)
		r.abandoned = true
		return
	}

	block, _ := workflow_block.FindByType(node.Type)
	// Fill declared defaults ONCE, here, for every executor kind. `node` is a
	// copy, so this replaces the map rather than mutating the pinned snapshot.
	// Before this, each consumption site re-implemented its own default as a
	// literal and agent blocks got none at all — an absent `install_os.wipe` or
	// `update_agent.agent_version` reached the device, and whatever the agent
	// did with a missing field was the real default.
	node.Params = block.ApplyDefaults(node.Params)
	st := r.execNode.GraphState[id]
	now := time.Now()
	st.Status = wen.GraphNodeRunning
	st.StartedAt = &now
	st.Attempts++
	r.execNode.GraphState[id] = st

	if block.IsAgent() {
		r.dispatchAgent(id, node)
		return
	}
	// Server-executed blocks run synchronously and cascade.
	switch node.Type {
	case workflow_block.TypeCondition:
		r.execCondition(id, node)
	case workflow_block.TypeWait:
		r.execWait(id, node)
	case workflow_block.TypeJoin:
		r.completeAndAdvance(id, nil, workflow_block.DefaultOutputPort)
	case workflow_block.TypeSecretUpsert:
		r.execSecretUpsert(id, node)
	case workflow_block.TypeAttributeUpsert:
		r.execAttributeUpsert(id, node)
	case workflow_block.TypeHTTPRequest:
		r.execHTTPRequest(id, node)
	case workflow_block.TypeSetBootPolicy:
		r.execSetBootPolicy(id, node)
	case workflow_block.TypeRebootToRecovery:
		r.execRebootToRecovery(id, node)
	case workflow_block.TypeWaitForNode:
		r.execWaitForNode(id, node)
	case workflow_block.TypeApplyWatchdog:
		r.execApplyWatchdog(id, node)
	default:
		r.handleFailure(id, node, fmt.Sprintf("server block '%s' is not implemented", node.Type))
	}
}

func (r *engineRun) dispatchAgent(id string, node workflow_template.GraphNode) {
	block, _ := workflow_block.FindByType(node.Type)
	authContext := commands.NewAuthContext(r.execNode.AccountID, uuid.Nil)
	params := r.rc.resolveParams(node.Params)
	batch := node_command_cmd.BatchNodeCommandCommand{
		NodeIDs:                 []uuid.UUID{r.execNode.NodeID},
		Command:                 block.Slug,
		Params:                  params,
		AuthContext:             authContext,
		WorkflowExecutionNodeID: &r.execNode.ID,
		// Read off the UNRESOLVED template: once substituted, a secret is
		// indistinguishable from an operator-typed literal, so this is the only
		// point where we can still tell which params the API must redact.
		SecretDerivedParams: SecretDerivedParams(node.Params),
		// The block id rides the payload so a pass lost between this dispatch
		// and finalize can be reconciled by reconcileDispatches instead of
		// failing the run or dispatching the block twice.
		WorkflowBlockID: id,
	}
	batchResult, err := r.svc.nodeService.SendBatchCommand(r.ctx, batch)
	if err != nil {
		r.handleFailure(id, node, fmt.Sprintf("failed to send command: %v", err))
		return
	}
	if len(batchResult.Results) == 0 || batchResult.Results[0].CommandID == nil {
		msg := "command failed to send"
		if len(batchResult.Results) > 0 && batchResult.Results[0].Error != "" {
			msg = batchResult.Results[0].Error
		}
		r.handleFailure(id, node, msg)
		return
	}
	commandID := *batchResult.Results[0].CommandID
	st := r.execNode.GraphState[id]
	st.CommandID = &commandID
	r.execNode.GraphState[id] = st
	// Keep CurrentCommandID/CurrentStepIndex populated so the completion
	// re-enqueue (which looks up by command) and the legacy views still work.
	r.execNode.CurrentCommandID = &commandID
	// A policy timeout has no external event to fire on — the agent may simply
	// never report — so schedule a check for just after the deadline.
	if p := node.Policy; p != nil && p.TimeoutSeconds > 0 {
		r.svc.scheduleReenqueue(r.execNode.ID, time.Duration(p.TimeoutSeconds)*time.Second+time.Second)
	}
}

func (r *engineRun) execCondition(id string, node workflow_template.GraphNode) {
	left := r.rc.resolve(str(node.Params["left"]))
	right := r.rc.resolve(str(node.Params["right"]))
	op := str(node.Params["op"])
	result := evalCondition(left, op, right)
	port := workflow_block.PortFalse
	if result {
		port = workflow_block.PortTrue
	}
	r.completeAndAdvance(id, map[string]string{"result": boolStr(result)}, port)
}

func evalCondition(left, op, right string) bool {
	switch op {
	case "eq":
		return left == right
	case "ne":
		return left != right
	case "contains":
		return strings.Contains(left, right)
	case "not_contains":
		return !strings.Contains(left, right)
	case "exists":
		return left != ""
	case "not_exists":
		return left == ""
	default:
		return false
	}
}

func (r *engineRun) execWait(id string, node workflow_template.GraphNode) {
	// The fallback here is the registry's, filled in by startBlock's
	// ApplyDefaults; the literal below only ever survives a spec that stops
	// declaring one, which registry_defaults_test.go refuses.
	seconds := intParam(node.Params["seconds"], 30)
	until := time.Now().Add(time.Duration(seconds) * time.Second)
	st := r.execNode.GraphState[id]
	st.WaitUntil = &until
	r.execNode.GraphState[id] = st
	r.svc.scheduleReenqueue(r.execNode.ID, time.Duration(seconds)*time.Second)
}

// waitForNodePollInterval is how often a wait-for-node block re-reads the node.
// A reboot takes a minute or more, so polling faster buys nothing and every
// poll is a row read per waiting run.
const waitForNodePollInterval = 15 * time.Second

// execRebootToRecovery sends the node into the netboot recovery image for ONE
// boot.
//
// It deliberately calls the same service the dedicated HTTP route does rather
// than composing set_boot_policy + reboot, because the one-shot bookkeeping is
// the whole point: the platform records the previous boot target and restores
// it as soon as the node reconnects netbooted. A playbook built from the two
// primitives instead would leave a node stranded in recovery whenever the run
// failed between them — which, for a capture or an install, is exactly when it
// is most likely to fail.
func (r *engineRun) execRebootToRecovery(id string, node workflow_template.GraphNode) {
	// uuid.Nil is the engine's system actor, the same one dispatchAgent uses:
	// the human who started the run is recorded on the execution.
	if err := r.svc.nodeService.RebootToRecovery(r.ctx, r.execNode.AccountID, uuid.Nil, r.execNode.NodeID); err != nil {
		r.handleFailure(id, node, fmt.Sprintf("failed to reboot into recovery: %v", err))
		return
	}
	r.completeAndAdvance(id, map[string]string{"requested": "true"}, workflow_block.DefaultOutputPort)
}

// execWaitForNode parks the run until the device reaches the requested state.
//
// The deadline is stored as an absolute time in WaitUntil so it survives a
// restart: the block is re-entered from the stored graph state, not from a
// timer this process happens to still hold.
func (r *engineRun) execWaitForNode(id string, node workflow_template.GraphNode) {
	// Clamped with the same ceiling every other blocking param uses: this parks
	// a worker, so an unbounded value is the engine's problem, not the
	// operator's.
	timeout, bound := node_command_domain.ClampBlockSeconds(float64(intParam(node.Params["timeout_seconds"], 900)))
	if bound {
		r.svc.logger.WarnContext(r.ctx, "wait_for_node timeout clamped",
			"execution_node_id", r.execNode.ID, "block", id, "seconds", timeout)
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	st := r.execNode.GraphState[id]
	st.WaitUntil = &deadline
	r.execNode.GraphState[id] = st

	// Check immediately: the node may already be where the playbook wants it,
	// and a run that waits 15 seconds to discover that reads as broken.
	if r.checkWaitForNode(id, node, st) {
		return
	}
	r.svc.scheduleReenqueue(r.execNode.ID, waitForNodePollInterval)
}

func (r *engineRun) progressWaitForNode(id string, node workflow_template.GraphNode, st wen.GraphNodeState) {
	if r.checkWaitForNode(id, node, st) {
		return
	}
	r.svc.scheduleReenqueue(r.execNode.ID, waitForNodePollInterval)
}

// checkWaitForNode reports whether the block settled (completed or failed).
func (r *engineRun) checkWaitForNode(id string, node workflow_template.GraphNode, st wen.GraphNodeState) bool {
	n, err := r.svc.nodeRepo.GetByID(r.ctx, r.execNode.AccountID, r.execNode.NodeID)
	if err != nil {
		// A read failure is not evidence about the device, so it is not a
		// reason to fail the wait — the deadline below is what ends it.
		r.svc.logger.WarnContext(r.ctx, "wait_for_node could not read the node",
			"execution_node_id", r.execNode.ID, "block", id, "error", err)
		n = nil
	}

	want := strOrDefault(str(node.Params["state"]), "online")
	if n != nil && nodeIsInState(n, want) {
		r.completeAndAdvance(id, map[string]string{
			"state":               want,
			"current_boot_device": n.CurrentBootDevice(),
		}, workflow_block.DefaultOutputPort)
		return true
	}

	if st.WaitUntil != nil && !time.Now().Before(*st.WaitUntil) {
		observed := "unknown"
		if n != nil {
			observed = string(n.Status)
			if n.IsRecoveryBootstrap() {
				observed += " (recovery)"
			}
		}
		r.handleFailure(id, node, fmt.Sprintf(
			"node did not reach state '%s' before the timeout (last seen: %s)", want, observed))
		return true
	}
	return false
}

// nodeIsInState answers the three states the block offers.
//
// `managed_os` is deliberately "online AND NOT the recovery bootstrap" rather
// than a boot-device test: the same bootstrap reports "http" when netbooted and
// "sd" when booted from a provisioning card, so the boot medium cannot separate
// a running OS from an initramfs — the mistake IsRecoveryBootstrap exists to
// prevent.
func nodeIsInState(n *node_domain.Node, want string) bool {
	switch want {
	case "recovery":
		return n.Status == node_domain.StatusOnline && n.IsRecoveryBootstrap()
	case "managed_os":
		return n.Status == node_domain.StatusOnline && !n.IsRecoveryBootstrap()
	default: // "online"
		return n.Status == node_domain.StatusOnline
	}
}

func strOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (r *engineRun) execSecretUpsert(id string, node workflow_template.GraphNode) {
	if r.svc.secretWriter == nil {
		r.handleFailure(id, node, "secret writer not configured")
		return
	}
	name := strings.TrimSpace(r.rc.resolve(str(node.Params["name"])))
	value := r.rc.resolve(str(node.Params["value"]))
	kind := secret_domain.SecretKind(str(node.Params["kind"])) // default from the registry
	key := r.rc.resolve(str(node.Params["env_key"]))
	if name == "" {
		r.handleFailure(id, node, "secret name is required")
		return
	}
	opts := secret_domain.UpsertOptions{}
	if raw, ok := node.Params["sensitive"]; ok {
		sensitive := boolParam(raw, true)
		opts.Sensitive = &sensitive
	}
	if rawTags := strings.TrimSpace(r.rc.resolve(str(node.Params["tags"]))); rawTags != "" {
		opts.Tags = strings.Split(rawTags, ",")
	}
	opts.RunID = r.execNode.ExecutionID
	// Bound to the run's node: the service refuses a name that belongs to a
	// secret delivered anywhere else, and the reserved attribute names.
	if err := r.svc.secretWriter.UpsertForNode(r.ctx, r.runActor(),
		r.execNode.AccountID, r.execNode.NodeID, name, kind, key, value, opts); err != nil {
		r.handleFailure(id, node, fmt.Sprintf("failed to save secret: %v", err))
		return
	}
	r.completeAndAdvance(id, map[string]string{"name": name}, workflow_block.DefaultOutputPort)
}

// execAttributeUpsert writes a custom attribute on the run's node through the
// same service method the attribute HTTP endpoints use, so the key charset
// check, the secret quota (an attribute IS a secret) and the audit record are
// inherited. The target node is always the execution node the block runs on —
// a run cannot name another node's attribute.
func (r *engineRun) execAttributeUpsert(id string, node workflow_template.GraphNode) {
	if r.svc.secretWriter == nil {
		r.handleFailure(id, node, "secret writer not configured")
		return
	}
	key := strings.TrimSpace(r.rc.resolve(str(node.Params["key"])))
	if key == "" {
		r.handleFailure(id, node, "attribute key is required")
		return
	}
	value := r.rc.resolve(str(node.Params["value"]))
	// The registry default (false) is filled in by startBlock's ApplyDefaults;
	// the literal below only survives a spec that stops declaring one.
	sensitive := boolParam(node.Params["sensitive"], false)
	if _, err := r.svc.secretWriter.UpsertNodeAttribute(r.ctx, r.runActor(),
		r.execNode.AccountID, r.execNode.NodeID, key, value, sensitive); err != nil {
		r.handleFailure(id, node, fmt.Sprintf("failed to save node attribute: %v", err))
		return
	}
	r.completeAndAdvance(id, map[string]string{"key": key}, workflow_block.DefaultOutputPort)
}

func (r *engineRun) execSetBootPolicy(id string, node workflow_template.GraphNode) {
	if r.svc.nodePolicyWriter == nil {
		r.handleFailure(id, node, "node policy writer not configured")
		return
	}
	bootToOS := policyFlagParam(node.Params["boot_to_os"])
	networkFirst := policyFlagParam(node.Params["network_first"])
	if bootToOS == nil && networkFirst == nil {
		r.handleFailure(id, node, "no policy change selected: both boot_to_os and network_first are 'unchanged'")
		return
	}
	if err := r.svc.nodePolicyWriter.SetNodePolicy(r.ctx, r.runActor(), r.execNode.AccountID, r.execNode.NodeID, networkFirst, bootToOS, nil); err != nil {
		r.handleFailure(id, node, fmt.Sprintf("failed to set boot policy: %v", err))
		return
	}
	outputs := map[string]string{}
	if bootToOS != nil {
		outputs["boot_to_os"] = string(*bootToOS)
	}
	if networkFirst != nil {
		outputs["network_first"] = string(*networkFirst)
	}
	r.completeAndAdvance(id, outputs, workflow_block.DefaultOutputPort)
}

// execApplyWatchdog arms or disarms the node's watchdog through the same
// service the API uses, so a run cannot produce a configuration the arming
// endpoint would refuse: the domain bounds, the stored desired state and the
// config hash the device compares against all come from SetWatchdog.
//
// "disabled" disarms while keeping the stored configuration, mirroring DELETE
// /watchdog — a fleet-wide disarm is the reason to reach for this block in a
// hurry, and it must not also erase what to re-arm.
//
// The block completes when the converge is ACCEPTED, not when the device
// confirms it: apply_watchdog_config is queueable, so an offline node gets it on
// reconnect. The outputs say which happened rather than letting a queued
// converge read as an armed watchdog.
func (r *engineRun) execApplyWatchdog(id string, node workflow_template.GraphNode) {
	presetName := strings.TrimSpace(r.rc.resolve(str(node.Params["preset"])))
	if presetName == "" {
		r.handleFailure(id, node, "preset is required")
		return
	}

	var cfg node_domain.WatchdogConfig
	if presetName == "disabled" {
		// Keep whatever is stored and only clear the enable flag, so a later
		// re-arm does not need the configuration retyped.
		if current, err := r.svc.nodeService.GetWatchdog(r.ctx, r.execNode.AccountID, r.execNode.NodeID); err == nil && current.Desired != nil {
			cfg = *current.Desired
		}
		cfg.Enabled = false
	} else {
		target := strings.TrimSpace(r.rc.resolve(str(node.Params["ping_target"])))
		preset, err := node_domain.WatchdogPreset(presetName, target)
		if err != nil {
			r.handleFailure(id, node, err.Error())
			return
		}
		cfg = preset
	}

	state, err := r.svc.nodeService.SetWatchdog(r.ctx, r.execNode.AccountID, uuid.Nil, r.execNode.NodeID, cfg)
	if err != nil {
		r.handleFailure(id, node, fmt.Sprintf("failed to set the watchdog: %v", err))
		return
	}
	outputs := map[string]string{
		"preset":  presetName,
		"enabled": strconv.FormatBool(cfg.Enabled),
		"queued":  strconv.FormatBool(state.Queued),
	}
	r.completeAndAdvance(id, outputs, workflow_block.DefaultOutputPort)
}

// policyFlagParam maps a set_boot_policy enum value onto the tri-state policy
// flag. "unchanged" (or anything unknown/absent) returns nil, which
// SetNodePolicy treats as "leave this field as is".
func policyFlagParam(v interface{}) *organizational_unit.PolicyFlag {
	switch s := str(v); organizational_unit.PolicyFlag(s) {
	case organizational_unit.PolicyInherit, organizational_unit.PolicyOn, organizational_unit.PolicyOff:
		f := organizational_unit.PolicyFlag(s)
		return &f
	default:
		return nil
	}
}

func (r *engineRun) execHTTPRequest(id string, node workflow_template.GraphNode) {
	method := str(node.Params["method"]) // default from the registry
	rawURL := r.rc.resolve(str(node.Params["url"]))
	if rawURL == "" {
		r.handleFailure(id, node, "url is required")
		return
	}
	// SSRF guard: only http(s), and reject internal targets. The client below
	// re-validates the resolved IP at dial time (DNS-rebind safe) and does not
	// follow redirects.
	if pu, perr := neturl.Parse(rawURL); perr != nil || (pu.Scheme != "http" && pu.Scheme != "https") {
		r.handleFailure(id, node, "url must be an absolute http(s) URL")
		return
	}
	url := rawURL
	body := r.rc.resolve(str(node.Params["body"]))
	// Clamped AT EXECUTION, not at save time: an unbounded timeout parks a
	// process-global workflow worker for as long as the author asks, and there
	// are 500 of them for the whole instance. Clamping here also covers every
	// playbook saved before this existed, which a save-time check never would.
	timeoutSeconds := intParam(node.Params["timeout_seconds"], 30) // registry default
	if timeoutSeconds > maxHTTPRequestTimeoutSeconds {
		// Logged, so the first person to meet the limit is visible rather than
		// silently slowed down.
		r.svc.logger.WarnContext(r.ctx, "clamping an http_request timeout to the ceiling",
			"execution_node_id", r.execNode.ID, "block", id,
			"requested_seconds", timeoutSeconds, "ceiling_seconds", maxHTTPRequestTimeoutSeconds)
		timeoutSeconds = maxHTTPRequestTimeoutSeconds
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	timeout := time.Duration(timeoutSeconds) * time.Second

	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	defer cancel()
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		r.handleFailure(id, node, safeURLError("invalid request", err, rawURL))
		return
	}
	for _, line := range strings.Split(str(node.Params["headers"]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, found := strings.Cut(line, ":")
		if found {
			req.Header.Set(strings.TrimSpace(k), r.rc.resolve(strings.TrimSpace(v)))
		}
	}
	resp, err := httpClientFor(timeout).Do(req)
	if err != nil {
		r.handleFailure(id, node, safeURLError("request failed", err, rawURL))
		return
	}
	defer resp.Body.Close()
	// Read one byte past the cap so a deliberate truncation and a body that
	// simply ended are different facts, and keep the read error: a server
	// that sends 200 and then drops the connection mid-body used to complete
	// the block with a partial body and no word about it.
	const bodyCap = 64 * 1024
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, bodyCap+1))
	truncated := len(respBody) > bodyCap
	if truncated {
		respBody = respBody[:bodyCap]
	}
	outputs := map[string]string{
		"status": fmt.Sprintf("%d", resp.StatusCode),
		"body":   string(respBody),
	}
	if truncated {
		outputs["truncated"] = "true"
	}
	if readErr != nil {
		r.recordOutputs(id, outputs)
		r.handleFailure(id, node, safeURLError("response body read failed", readErr, rawURL))
		return
	}
	if resp.StatusCode >= 400 {
		// Keep the outputs. `completeAndAdvance` is the only writer of Outputs,
		// so failing here used to settle the block with an EMPTY output map —
		// and on the `on_error: continue` branch that is a COMPLETED block whose
		// `status` and `body` the operator was told to branch on resolve to "".
		// A condition then compares "" and takes the false port, and the API's
		// own error message exists nowhere: `Error` holds only "HTTP 429".
		r.recordOutputs(id, outputs)
		r.handleFailure(id, node, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}
	r.completeAndAdvance(id, outputs, workflow_block.DefaultOutputPort)
}

// recordOutputs stores a block's outputs without settling it, so a failure path
// can keep what it learned. Used before handleFailure: the outputs of a call
// that FAILED are exactly what an `on_error: continue` branch needs to decide
// what to do about it.
func (r *engineRun) recordOutputs(id string, outputs map[string]string) {
	if len(outputs) == 0 {
		return
	}
	st := r.execNode.GraphState[id]
	st.Outputs = outputs
	r.execNode.GraphState[id] = st
}

// handleFailure applies the block's on-error policy: retry, continue, or fail
// the node.
func (r *engineRun) handleFailure(id string, node workflow_template.GraphNode, msg string) {
	r.settleFailure(id, node, msg, true)
}

// settleFailure is handleFailure with the retry decision explicit. allowRetry
// is false exactly when the block's command is still non-terminal on the
// device (a timeout on a sent/in-progress command): retrying there dispatches
// a second copy of a command that is still running — two writers on one disk
// for install_os/erase_drive. Every terminal-failure path allows retries.
func (r *engineRun) settleFailure(id string, node workflow_template.GraphNode, msg string, allowRetry bool) {
	policy := node.Policy
	st := r.execNode.GraphState[id]
	if allowRetry && policy != nil && policy.Retries > 0 && st.Attempts <= policy.Retries {
		r.svc.logger.Info("retrying failed block", "execution_node_id", r.execNode.ID, "block", id, "attempt", st.Attempts)
		r.startBlock(id, node) // increments Attempts
		return
	}
	if policy != nil && policy.OnError == workflow_template.OnErrorContinue {
		st.Status = wen.GraphNodeCompleted
		st.Error = msg
		now := time.Now()
		st.CompletedAt = &now
		r.execNode.GraphState[id] = st
		r.advanceFrom(id, workflow_block.DefaultOutputPort)
		return
	}
	// Terminal failure for the node.
	st.Status = wen.GraphNodeFailed
	st.Error = msg
	now := time.Now()
	st.CompletedAt = &now
	r.execNode.GraphState[id] = st
	r.execNode.Fail(msg)
	r.failed = true
}

// stalledBlocks returns the ids of graph nodes that carry no state although an
// edge taken by a completed block leads to them. It is only meaningful once no
// block is running: until then a stateless successor is simply work not reached
// yet. An edge counts as taken when its source completed and either chose that
// port or chose none at all (the trigger, and an on_error=continue settle, both
// leave ChosenPort empty and advance on the default port).
func (r *engineRun) stalledBlocks() []string {
	var stalled []string
	for _, node := range r.graph.Nodes {
		if st, ok := r.execNode.GraphState[node.ID]; ok && st.Status != "" {
			continue
		}
		for _, edge := range r.graph.EdgesTo(node.ID) {
			src := r.execNode.GraphState[edge.From]
			if src.Status != wen.GraphNodeCompleted {
				continue
			}
			taken := src.ChosenPort == edge.Port() ||
				(src.ChosenPort == "" && edge.Port() == workflow_block.DefaultOutputPort)
			if taken {
				stalled = append(stalled, node.ID)
				break
			}
		}
	}
	return stalled
}

// finalize persists the node, decides completion, and updates the execution.
func (r *engineRun) finalize(workerID int) {
	// A settler already owns this run's outcome and has already refreshed the
	// execution and freed its slot. Writing here could only be wrong: our copy
	// predates the settle, and UpdateActive would discard it anyway — this just
	// says so in one line instead of as a concurrency warning.
	if r.abandoned {
		r.svc.logger.InfoContext(r.ctx, "discarding this pass: the execution node was settled elsewhere",
			"worker_id", workerID, "execution_node_id", r.execNode.ID)
		return
	}

	if !r.failed && r.execNode.Status != wen.StatusFailed {
		anyRunning := false
		for _, st := range r.execNode.GraphState {
			if st.Status == wen.GraphNodeRunning {
				anyRunning = true
				break
			}
		}
		if !anyRunning {
			// Nothing is running, so nothing will advance the walk again. If a
			// block was never entered even though a taken edge leads to it, the
			// walk stalled — completing here would report a green run that never
			// executed the blocks the operator wrote. Fail loudly instead: a
			// wrong answer about whether a playbook ran is worse than a failure.
			if stalled := r.stalledBlocks(); len(stalled) > 0 {
				msg := fmt.Sprintf("graph stalled: block(s) %s were reachable but never entered",
					strings.Join(stalled, ", "))
				r.svc.logger.Error("workflow graph stalled",
					"execution_node_id", r.execNode.ID, "blocks", stalled)
				r.execNode.Fail(msg)
			} else {
				r.execNode.Complete()
			}
		}
	}

	// Persist through the active-only guard: if a CancelExecution or
	// AbandonNodeExecutions settled this node while this worker held its pre-settle
	// copy, the stored row is already terminal and this write is discarded rather
	// than resurrecting the run to `running`. The settler has already refreshed the
	// execution and freed the slot, so there is nothing more to do here.
	wrote, err := r.svc.executionNodeRepo.UpdateActive(r.ctx, r.execNode)
	if err != nil {
		r.svc.logger.Error("failed to update execution node", "worker_id", workerID, "execution_node_id", r.execNode.ID, "error", err)
		return
	}
	if !wrote {
		r.svc.logger.WarnContext(r.ctx, "execution node was settled concurrently; discarding this worker's write",
			"worker_id", workerID, "execution_node_id", r.execNode.ID)
		return
	}
	r.svc.notifyExecutionNodeUpdated(r.execNode)
	if r.execNode.Status == wen.StatusCompleted || r.execNode.Status == wen.StatusFailed {
		r.svc.updateExecutionStatus(r.ctx, r.execution)
	}
}

// scheduleReenqueue pushes the execution node back onto the queue after delay
// (used by control.wait timers). Best-effort — a lost timer only delays.
func (s *Service) scheduleReenqueue(execNodeID uuid.UUID, delay time.Duration) {
	if delay < 0 {
		delay = 0
	}
	deadline := time.Now().Add(delay)

	// Do not arm a deadline that is already armed.
	//
	// progressAgentBlock re-arms on EVERY pass over a still-running timed block,
	// recomputing the same absolute deadline from StartedAt — and each armed
	// timer re-enqueues the node, which arms another. That is an amplifying loop
	// whose rate is set by how often the node is processed, and it is the engine
	// under load exactly when a block is stuck. Skipping when a pending timer
	// already fires no later than needed leaves the behaviour identical (the
	// deadline is still checked) and the timer count bounded.
	if armed, ok := s.armedDeadlines.Load(execNodeID); ok {
		if at, isTime := armed.(time.Time); isTime && !at.After(deadline) {
			return
		}
	}
	s.armedDeadlines.Store(execNodeID, deadline)

	time.AfterFunc(delay, func() {
		// CompareAndDelete, not Delete: a shorter deadline armed after this one
		// owns the entry now, and clearing it would let the next pass arm a
		// duplicate.
		s.armedDeadlines.CompareAndDelete(execNodeID, deadline)
		s.enqueueNode(execNodeID, "wait deadline")
	})
}

// --- small param helpers ---

func str(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intParam(v interface{}, fallback int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return fallback
	}
}

func boolParam(v interface{}, fallback bool) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true":
			return true
		case "false":
			return false
		}
	}
	return fallback
}
