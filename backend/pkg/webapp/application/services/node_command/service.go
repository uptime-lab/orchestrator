package node_command

import (
	"context"
	"fmt"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/node_commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

// WorkflowExecutionService interface for workflow execution service callback
type WorkflowExecutionService interface {
	ProcessNodeCommandCompletion(ctx context.Context, nodeCommandID uuid.UUID)
}

// RunCommandFilter is the run view's omission of commands whose presence
// answers a secret-fed condition (workflow_execution.Service) — the same
// function the REST command reads apply for a caller without
// secret.read_value, so the push channel cannot drift from them.
type RunCommandFilter interface {
	WithoutHiddenRunCommands(ctx context.Context, accountID uuid.UUID, commands []node_command.NodeCommand) ([]node_command.NodeCommand, error)
}

// maxRedeliverBatch bounds how many queued commands one reconnect dispatches,
// so a node returning after a long absence cannot flood the send channel.
const maxRedeliverBatch = 20

// Service handles node command-related business logic
type Service struct {
	repo                     repository.NodeCommandRepository
	websocketClient          websockets.WebsocketClient
	nodeService              services.NodeService     // Optional: injected after creation to avoid circular dependency
	workflowExecutionService WorkflowExecutionService // Optional: injected after creation to avoid circular dependency
	runCommands              RunCommandFilter         // Unset: every run command's frame goes to secret.read_value holders only
	commandClient            node_commands.NodeCommandClient
	agentVersions            AgentVersionChecker
	captureSettler           CaptureSettler // Unset: a capture record is settled only by its stream and the startup sweep
	audit                    *audit.Recorder
	logger                   *slog.Logger
}

// NewService creates a new node command service with all dependencies
func NewService(
	repo repository.NodeCommandRepository,
	websocketClient websockets.WebsocketClient,
	auditRecorder *audit.Recorder,
	logger *slog.Logger,
) *Service {
	if auditRecorder == nil {
		panic("node_command.Service requires an audit recorder")
	}
	return &Service{
		repo:            repo,
		websocketClient: websocketClient,
		audit:           auditRecorder,
		logger:          logger.With("service", "node_command"),
	}
}

// SetNodeService sets the node service (called after both services are created to avoid circular dependency)
func (s *Service) SetNodeService(nodeService services.NodeService) {
	s.nodeService = nodeService
}

// forceNetbootAfterFailedInstall is best-effort: it records a decision ABOUT a
// node, and a failure to record it must not change the command result the agent
// is waiting on.
func (s *Service) forceNetbootAfterFailedInstall(ctx context.Context, nodeID, accountID uuid.UUID) {
	if s.nodeService == nil {
		return
	}
	if err := s.nodeService.ForceNetbootAfterFailedInstall(ctx, accountID, nodeID); err != nil {
		s.logger.Warn("failed to force netboot after a failed install",
			"node_id", nodeID, "error", err)
	}
}

// settleInstallFailure applies the failed-install boot rule: an install_os that
// ends failed — reported by the agent, or settled by a sweep because the agent
// restarted or the node stopped answering mid-install — may have left a partial
// image on the disk, so the node must be pointed at netboot rather than at it.
func (s *Service) settleInstallFailure(ctx context.Context, cmd node_command.NodeCommand) {
	if cmd.Slug == node_command.SlugInstallOS {
		s.forceNetbootAfterFailedInstall(ctx, cmd.NodeID, cmd.AccountID)
	}
}

// settleEraseSuccess applies the same rule to a successful erase_drive: the
// disk the node would boot may now be empty. Left on the local OS, the board
// would loop on that disk, and with the boot failsafe off nothing would serve
// it recovery again. The node service decides whether the erase can have
// touched the boot disk (it acts only on a node running the recovery system;
// from a running OS the device refuses to erase the disk it runs from).
func (s *Service) settleEraseSuccess(ctx context.Context, cmd node_command.NodeCommand) {
	if cmd.Slug != node_command.SlugEraseDrive || s.nodeService == nil {
		return
	}
	if err := s.nodeService.ForceNetbootAfterDiskErase(ctx, cmd.AccountID, cmd.NodeID); err != nil {
		s.logger.Warn("failed to keep the node on recovery after an erase",
			"node_id", cmd.NodeID, "error", err)
	}
}

// CaptureSettler settles the image record of a capture_image command that ended
// without streaming (image.Service).
type CaptureSettler interface {
	FailAwaitingCapture(ctx context.Context, accountID, imageID uuid.UUID) error
}

// SetCaptureSettler wires the image service's capture-record settle.
func (s *Service) SetCaptureSettler(settler CaptureSettler) {
	s.captureSettler = settler
}

// settleCaptureRecord applies the capture rule: a capture_image that ends in
// any terminal state but success — refused by the agent on a live OS, failed,
// swept, cancelled, or never delivered — leaves its image record awaiting a
// stream that is not coming, so the record is failed with it. Unconditional on
// the boot rule's flag: a record exists whether or not the command left the
// platform. The repository moves only a record still in `capturing`, so a
// stream in flight, and one that already landed, keep their own state.
func (s *Service) settleCaptureRecord(ctx context.Context, cmd node_command.NodeCommand, status node_command.NodeCommandStatus) {
	if cmd.Slug != node_command.SlugCaptureImage || s.captureSettler == nil ||
		status == node_command.StatusSuccess || !isTerminalStatus(status) {
		return
	}
	raw, _ := cmd.Payload["image_id"].(string)
	imageID, err := uuid.Parse(raw)
	if err != nil {
		return
	}
	if err := s.captureSettler.FailAwaitingCapture(ctx, cmd.AccountID, imageID); err != nil {
		s.logger.Warn("failed to settle the image record of a capture that did not stream",
			"command_id", cmd.ID, "image_id", imageID, "error", err)
	}
}

// setStatus is the ONLY writer of a command's status in this service, so the
// failed-install boot rule cannot be skipped by a settle path added later:
// every write of "failed" to an install_os forces the node back to netboot,
// and every write of "success" to an erase_drive keeps it on recovery.
// applyBootRule is false in exactly two places: a dispatch that failed before
// the send (the command never left the platform, so no disk was touched and
// the operator's boot target stays as it was), and a node whose row is already
// deleted (its tombstone carries the rule instead). A structural test asserts
// nothing else in the package writes a status.
func (s *Service) setStatus(ctx context.Context, cmd node_command.NodeCommand, status node_command.NodeCommandStatus, progress int, details string, applyBootRule bool) error {
	if err := s.repo.UpdateStatus(ctx, cmd.ID, status, progress, details); err != nil {
		return err
	}
	if status == node_command.StatusFailed && applyBootRule {
		s.settleInstallFailure(ctx, cmd)
	}
	if status == node_command.StatusSuccess && applyBootRule {
		s.settleEraseSuccess(ctx, cmd)
	}
	s.settleCaptureRecord(ctx, cmd, status)
	return nil
}

// SetWorkflowExecutionService sets the workflow execution service (called after both services are created to avoid circular dependency)
func (s *Service) SetWorkflowExecutionService(workflowExecutionService WorkflowExecutionService) {
	s.workflowExecutionService = workflowExecutionService
}

// SetRunCommandFilter wires the decision of which run commands' frames a
// member without secret.read_value may receive.
func (s *Service) SetRunCommandFilter(f RunCommandFilter) {
	s.runCommands = f
}

// RegisterNodeCommand creates a node command with status "registered" and returns it
// Generates UUID internally
func (s *Service) RegisterNodeCommand(ctx context.Context, nodeID uuid.UUID, slug string, payload map[string]interface{}, authContext interface{}) (*node_command.NodeCommand, error) {
	return s.RegisterNodeCommandWithWorkflowNode(ctx, nodeID, slug, payload, authContext, nil)
}

// RegisterNodeCommandWithWorkflowNode creates a node command with optional workflow execution node ID
func (s *Service) RegisterNodeCommandWithWorkflowNode(ctx context.Context, nodeID uuid.UUID, slug string, payload map[string]interface{}, authContext interface{}, workflowExecutionNodeID *uuid.UUID) (*node_command.NodeCommand, error) {
	// Extract accountID from authContext
	var accountID uuid.UUID
	if authCtx, ok := authContext.(interface{ GetAccountID() uuid.UUID }); ok {
		accountID = authCtx.GetAccountID()
	} else {
		return nil, fmt.Errorf("invalid auth context: missing account ID")
	}

	// Refuse a malformed target_device BEFORE a row exists.
	//
	// It goes here rather than at the seven dispatch sites that read the field,
	// because this is the one choke point every device command passes through —
	// the HTTP handlers, the workflow engine, the cron scheduler and the
	// platform's own self-heal — so a slug added later is covered by
	// construction. The seven sites read it with bare type assertions and the
	// only check in the system was on the AGENT, reached from install_os alone,
	// while apply_access_config_offline and patch_file_offline MOUNT whatever
	// they are handed.
	if err := node_command.ValidateTargetDevicePayload(payload); err != nil {
		return nil, &app_errors.OperatorRefusalError{Err: err}
	}

	// Generate UUID for node command
	nodeCommandID := uuid.New()

	now := time.Now()
	queueable := node_command.IsQueueable(slug)
	nodeCmd := &node_command.NodeCommand{
		ID:        nodeCommandID,
		NodeID:    nodeID,
		AccountID: accountID,
		Slug:      slug,
		Payload:   payload,
		Status:    node_command.StatusRegistered,
		Progress:  0,
		Details:   "",
		Queueable: queueable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if queueable {
		// The supersede key is per node and per slug: a newer desired state for
		// the same node replaces an older one that never left the queue.
		nodeCmd.SupersedeKey = node_command.SupersedeKeyFor(slug, nodeID)
		expiry := now.Add(node_command.QueueTTL(slug))
		nodeCmd.ExpiresAt = &expiry
	}

	if err := s.repo.CreateWithWorkflowNode(ctx, nodeCmd, workflowExecutionNodeID); err != nil {
		return nil, fmt.Errorf("failed to create node command: %w", err)
	}
	// The stored row now carries the run link; the in-memory command says so
	// too, as a read of the row would, so the created frame is routed by it.
	nodeCmd.WorkflowExecutionNodeID = workflowExecutionNodeID

	// Retire any older queued command for the same target. Done after the insert
	// so a failure here cannot strand the node with nothing queued; the worst
	// case is two queued commands, and applying the newer one last still
	// converges (they are declarative and idempotent).
	if queueable {
		if superseded, err := s.repo.SupersedeRegisteredByKey(ctx, nodeID, nodeCmd.SupersedeKey, nodeCommandID, nodeCmd.CreatedAt); err != nil {
			s.logger.Warn("failed to supersede older queued commands",
				"node_id", nodeID, "supersede_key", nodeCmd.SupersedeKey, "error", err)
		} else if len(superseded) > 0 {
			s.logger.Info("superseded older queued commands",
				"node_id", nodeID, "supersede_key", nodeCmd.SupersedeKey, "count", len(superseded))
			// Run the same completion contract retireQueuedCommand honours for each
			// superseded row: notify, and — crucially — wake the workflow engine.
			// A superseded command linked to a workflow execution node would
			// otherwise leave its block `running` forever, so completed_at never
			// lands, retention never sweeps it, and after 20 the account's
			// active-execution cap answers 429 for every playbook.
			for _, id := range superseded {
				if updated, gerr := s.repo.GetByIDForUpdate(ctx, id); gerr == nil {
					s.notifyNodeCommandUpdated(updated)
				}
				if s.workflowExecutionService != nil {
					s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, id)
				}
			}
		}
	}

	// Update node status to maintenance if it's online. Queueable commands are
	// exempt: they may wait for an offline node, and flipping status at
	// registration would paint an entire fleet as "in maintenance" — indefinitely
	// for nodes that never come back. The transition happens when the command is
	// actually sent (MarkNodeCommandSent). Non-disruptive commands are exempt
	// for the opposite reason: identify changes nothing on the device, so the
	// flip to maintenance and the flip back a few hundred milliseconds later
	// were a visible status flash over an action that did nothing.
	if s.nodeService != nil && !queueable && !node_command.IsNonDisruptive(slug) {
		// Check if node has active commands (including the one we just created)
		hasActive, err := s.repo.HasActiveCommands(ctx, nodeID)
		if err == nil && hasActive {
			// A reboot pins the node to "restarting" right away: the agent's
			// own success report is unreliable (it may go down for the reboot
			// before the report leaves), and "restarting" is what the stale
			// -connection monitor preserves through the downtime.
			target := node.StatusMaintenance
			if node_command.CausesReboot(slug) {
				target = node.StatusRestarting
			}
			// Record the cause. "restarting" is a CLAIM that this command might
			// take the device down, and for a bootloader flash it usually does
			// not; without the slug a reader cannot tell that pin from a real
			// reboot and has to show the alarming word for both.
			if err := s.nodeService.UpdateStatusWithAccountPinnedBy(ctx, nodeID, accountID, string(target), slug); err != nil {
				s.logger.Warn("failed to set node status for command", "node_id", nodeID, "status", target, "error", err)
			}
		}
	}

	// Emit WebSocket event
	s.notifyNodeCommandCreated(nodeCmd)

	// AUDIT. This is the ONE place every device command passes through — the HTTP
	// handlers, the workflow engine, the cron scheduler and the platform's own
	// self-heal dispatches all land here — which is why the writer lives at this
	// level rather than in each caller. "Who rebooted my node" was unanswerable
	// before it, and a per-caller writer would have been complete on the day it
	// was written and incomplete by the next new caller.
	//
	// Every slug is recorded, not a curated "destructive" subset: run_script and
	// write_file are as consequential as a reboot, and any subset drifts from its
	// own definition the way the doc comment this closes did. The volume that
	// admits — a per-minute cron over N nodes writes N rows a minute — is
	// deliberate: a schedule touching hardware that often SHOULD be visible in the
	// trail, and the rows are small.
	s.recordCommandAudit(ctx, nodeCmd, authContext, workflowExecutionNodeID)

	return nodeCmd, nil
}

// recordCommandAudit appends the audit row for a dispatch. The actor is read
// from the auth context by interface, the same way the account is: a request
// carries commands.AuthContext (a user), while the engine and the internal
// re-apply paths carry a context with only an account, and those are recorded as
// the system rather than mis-attributed to a person.
func (s *Service) recordCommandAudit(
	ctx context.Context,
	nodeCmd *node_command.NodeCommand,
	authContext interface{},
	workflowExecutionNodeID *uuid.UUID,
) {
	actor := audit.SystemActor(nodeCmd.AccountID)
	if withUser, ok := authContext.(interface{ GetUserID() uuid.UUID }); ok {
		if userID := withUser.GetUserID(); userID != uuid.Nil {
			actor = audit.UserActor(nodeCmd.AccountID, userID)
		}
	}

	// The command id and, for a run, the execution node link the row back to the
	// full payload and status history. The PAYLOAD itself is never copied here: it
	// can carry credential material (which is why node_commands.payload has its
	// own encv1: encryption), and duplicating it into a table nobody treats as a
	// secret store would put it outside every rewrap list.
	detail := "command " + nodeCmd.ID.String()
	if nodeCmd.Queueable {
		// Worth distinguishing: a queued command has been ACCEPTED, not applied,
		// and may land hours later on reconnect.
		detail += " (queued)"
	}
	if workflowExecutionNodeID != nil {
		detail += " via workflow execution node " + workflowExecutionNodeID.String()
	}

	s.audit.RecordDetail(ctx, actor,
		audit_event.NodeCommandAction(nodeCmd.Slug), audit_event.TargetNode,
		nodeCmd.NodeID.String(), detail)
}

// GetRegisteredNodeCommands gets registered commands for a node (called by gRPC server)
func (s *Service) GetRegisteredNodeCommands(ctx context.Context, nodeID uuid.UUID, limit int) ([]node_command.NodeCommand, error) {
	return s.repo.GetRegisteredByNodeID(ctx, nodeID, limit)
}

// MarkNodeCommandFailed marks a command as failed with error details (called by gRPC server on send failure)
func (s *Service) MarkNodeCommandFailed(ctx context.Context, nodeCommandID uuid.UUID, errorDetails string) error {
	// Get current command status
	nodeCmd, err := s.repo.GetByIDForUpdate(ctx, nodeCommandID)
	if err != nil {
		return fmt.Errorf("failed to get node command: %w", err)
	}

	// Only update if status is still "registered" or "sent"
	if nodeCmd.Status != node_command.StatusRegistered && nodeCmd.Status != node_command.StatusSent {
		s.logger.Warn("node command status already changed, skipping mark as failed",
			"node_command_id", nodeCommandID,
			"current_status", nodeCmd.Status)
		return nil // Not an error, just a race condition
	}

	// Update to "failed" status. A "registered" command never left the
	// platform — the send itself failed — so no disk was touched; a "sent" one
	// may already be running on the device.
	reachedDevice := nodeCmd.Status == node_command.StatusSent
	if err := s.setStatus(ctx, *nodeCmd, node_command.StatusFailed, 0, errorDetails, reachedDevice); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	// Get updated command for WebSocket event
	updatedCmd, err := s.repo.GetByIDForUpdate(ctx, nodeCommandID)
	if err == nil {
		s.notifyNodeCommandUpdated(updatedCmd)
	}

	// Release the node from "maintenance" when nothing else is running. Without
	// this the pin set at registration outlives the command: every OTHER terminal
	// path goes through updateNodeStatusForCommand, but a command that dies on the
	// send path — an unbuildable payload, a resolution the dispatcher refuses —
	// only ever reached this function, so the node stayed in maintenance until an
	// unrelated command happened to complete or the agent reconnected. The uptime
	// reads wrong for as long as it lasts, because the UI switches its clock
	// source by status. Best-effort: the command IS failed, and a failure here
	// must not hide that from the caller.
	if err := s.updateNodeStatusForCommand(ctx, nodeCmd.NodeID, nodeCmd.AccountID, node_command.StatusFailed, nodeCmd.Slug); err != nil {
		s.logger.Warn("failed to release node status after a command failed to send",
			"node_id", nodeCmd.NodeID, "node_command_id", nodeCommandID, "error", err)
	}

	return nil
}

// FailCommandOfStoppedRun settles the command a playbook block was waiting on
// when the run itself was stopped (an operator cancel, the node's deletion, a
// contained panic), whatever stage the command had reached.
//
// MarkNodeCommandFailed is the SEND path's settler and acts only on
// registered/sent: the agent acknowledges a command with in_progress at once,
// so for a block that was actually running it found nothing to do, and the
// command, the node's maintenance pin and the block all outlived the run until
// the device happened to report.
//
// The device is not told: the platform has no way to stop a command already
// executing. Whatever it reports later is refused by the terminal-status guard
// in UpdateNodeCommandStatusWithResult, which is what keeps the run's recorded
// outcome. A failed install_os forces netboot through setStatus as on every
// other route, because the device may still be writing the disk.
//
// The node is released the way the other failure settles release it, with one
// exception: a command that may be taking the device down (CausesReboot) and
// has reached it keeps its pin, because "online" would be a claim about a
// device that may be mid-reboot. The reconnect, or the unreachable-node
// verdict, settles that pin as it settles every other reboot.
func (s *Service) FailCommandOfStoppedRun(ctx context.Context, nodeCommandID uuid.UUID, reason string) error {
	nodeCmd, err := s.repo.GetByIDForUpdate(ctx, nodeCommandID)
	if err != nil {
		return fmt.Errorf("failed to get node command: %w", err)
	}
	if isTerminalStatus(nodeCmd.Status) {
		// Finished before the run was stopped: its own outcome stands.
		return nil
	}

	reachedDevice := nodeCmd.Status != node_command.StatusRegistered
	if err := s.setStatus(ctx, *nodeCmd, node_command.StatusFailed, 0, reason, reachedDevice); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
	if updatedCmd, gerr := s.repo.GetByIDForUpdate(ctx, nodeCommandID); gerr == nil {
		s.notifyNodeCommandUpdated(updatedCmd)
	}

	if reachedDevice && node_command.CausesReboot(nodeCmd.Slug) {
		return nil
	}
	if err := s.updateNodeStatusForCommand(ctx, nodeCmd.NodeID, nodeCmd.AccountID, node_command.StatusFailed, nodeCmd.Slug); err != nil {
		s.logger.Warn("failed to release node status after settling the command of a stopped run",
			"node_id", nodeCmd.NodeID, "node_command_id", nodeCommandID, "error", err)
	}
	return nil
}

// MarkNodeCommandSent updates status to "sent" after successful gRPC send (called by gRPC server)
// Only updates if current status is "registered"; if status has already changed, logs warning and returns nil
func (s *Service) MarkNodeCommandSent(ctx context.Context, nodeCommandID uuid.UUID) error {
	// Get current command status
	nodeCmd, err := s.repo.GetByIDForUpdate(ctx, nodeCommandID)
	if err != nil {
		return fmt.Errorf("failed to get node command: %w", err)
	}

	// Only update if status is still "registered"
	if nodeCmd.Status != node_command.StatusRegistered {
		s.logger.Warn("node command status already changed, skipping mark as sent",
			"node_command_id", nodeCommandID,
			"current_status", nodeCmd.Status,
			"expected_status", node_command.StatusRegistered)
		return nil // Not an error, just a race condition
	}

	// Update to "sent" status using conditional update
	updated, err := s.repo.UpdateStatusIfCurrent(ctx, nodeCommandID,
		node_command.StatusRegistered,
		node_command.StatusSent,
		0,
		"")
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	if !updated {
		// Another goroutine already updated it
		s.logger.Warn("node command status changed during update",
			"node_command_id", nodeCommandID)
		return nil
	}

	// Update node status to maintenance if needed
	if s.nodeService != nil {
		if err := s.updateNodeStatusForCommand(ctx, nodeCmd.NodeID, nodeCmd.AccountID, node_command.StatusSent, nodeCmd.Slug); err != nil {
			s.logger.Warn("failed to update node status for command", "node_id", nodeCmd.NodeID, "error", err)
		}
	}

	// Get updated command for WebSocket event
	updatedCmd, err := s.repo.GetByIDForUpdate(ctx, nodeCommandID)
	if err == nil {
		s.notifyNodeCommandUpdated(updatedCmd)
	}

	return nil
}

// UpdateNodeCommandStatus updates from node reports via gRPC (called directly by gRPC server)
func (s *Service) UpdateNodeCommandStatus(ctx context.Context, nodeCommandID uuid.UUID, status string, progress int, details string) error {
	_, err := s.UpdateNodeCommandStatusWithResult(ctx, nodeCommandID, status, progress, details, nil)
	return err
}

// UpdateNodeCommandStatusWithResult is UpdateNodeCommandStatus plus structured
// agent outputs (persisted before the status change so a workflow step reading
// the outputs on completion sees them).
//
// It reports whether the report was APPLIED. A report against an
// already-finished command changes nothing here, and the caller must not act on
// it either: a device that replays a terminal frame for one of its own
// long-finished commands would otherwise re-drive every side effect keyed on
// that command. `result` is written below the guard for the same reason — the
// stored outputs of a finished command are evidence, and evidence an operator
// has already read must not be rewritten by the node it came from.
func (s *Service) UpdateNodeCommandStatusWithResult(ctx context.Context, nodeCommandID uuid.UUID, status string, progress int, details string, result map[string]string) (bool, error) {
	// Get current command to check node ID and slug
	currentCmd, err := s.repo.GetByIDForUpdate(ctx, nodeCommandID)
	if err != nil {
		return false, fmt.Errorf("failed to get node command: %w", err)
	}

	// A command that already reached a terminal state must not be reopened. A
	// queueable command can legitimately be delivered twice (a stream flap
	// requeues it while the agent is still executing), and without this guard the
	// second report would flip a finished command back to in_progress and fire
	// workflow completion for it a second time.
	if isTerminalStatus(currentCmd.Status) {
		s.logger.Info("ignoring status report for an already-finished command",
			"command_id", nodeCommandID, "current_status", currentCmd.Status, "reported", status)
		return false, nil
	}

	if len(result) > 0 {
		if err := s.repo.SetResult(ctx, nodeCommandID, result); err != nil {
			s.logger.Warn("failed to persist command result", "command_id", nodeCommandID, "error", err)
		}
	}

	// Convert string status to NodeCommandStatus
	var nodeCommandStatus node_command.NodeCommandStatus
	switch status {
	case "in_progress":
		nodeCommandStatus = node_command.StatusInProgress
	case "success":
		nodeCommandStatus = node_command.StatusSuccess
	case "failed":
		nodeCommandStatus = node_command.StatusFailed
	default:
		return false, fmt.Errorf("invalid status: %s", status)
	}

	// Special handling for reboot commands: keep them in "in_progress" until node comes back online
	// When agent reports "success" for reboot, we keep it as "in_progress" and wait for node to reconnect
	isRebootCommand := currentCmd.Slug == node_command.SlugReboot
	// Deliberately NARROWER than CausesReboot: this flag also drives the command
	// lifecycle (a reboot stays in_progress until the node reconnects) and the
	// workflow-completion gate below. Widening it would leave update_boot_order
	// steps in a playbook waiting forever. Only the STATUS pin is widened.
	if isRebootCommand && nodeCommandStatus == node_command.StatusSuccess {
		// Keep reboot command in "in_progress" instead of marking as "success"
		nodeCommandStatus = node_command.StatusInProgress
		// Update details to indicate reboot is in progress
		if details == "" {
			details = "Reboot initiated, waiting for node to come back online"
		}
	}

	// Handle -1 progress as "not applicable" (set to 0)
	if progress < 0 {
		progress = 0
	}

	// A failed OS install leaves a deliberately unbootable disk — the agent
	// never commits the image head unless the digest matched — so setStatus
	// sends the node back to netboot rather than pointing it at that disk.
	if err := s.setStatus(ctx, *currentCmd, nodeCommandStatus, progress, details, true); err != nil {
		return false, fmt.Errorf("failed to update node command status: %w", err)
	}

	// Update node status based on command status and type
	if s.nodeService != nil {
		// For reboot commands, keep the node in "restarting" through every
		// agent report (even though we keep the command in_progress) — the
		// generic path below would demote it to "maintenance".
		// Any command that ends by rebooting keeps the node in "restarting"
		// through its own reports. update_boot_order is the case that showed
		// why: it reports SUCCESS and only then goes down, so the generic
		// success path below would return a rebooting device to online.
		// ...unless the agent explicitly said it is NOT rebooting this board.
		// enable_eeprom is the case: it reboots only on a CM4, and on anything
		// else the node used to sit pinned to "restarting" waiting for a
		// reconnect that would never come. An older agent sends no such field,
		// and absence keeps the pin — the pin exists precisely because the
		// agent cannot be trusted to report a reboot it is about to perform.
		if node_command.CausesReboot(currentCmd.Slug) && !node_command.AgentDeclinedReboot(result) &&
			(status == "success" || status == "in_progress") {
			if err := s.nodeService.UpdateStatusWithAccountPinnedBy(ctx, currentCmd.NodeID, currentCmd.AccountID, string(node.StatusRestarting), currentCmd.Slug); err != nil {
				s.logger.Warn("failed to set node to restarting", "node_id", currentCmd.NodeID, "error", err)
			}
		} else if nodeCommandStatus == node_command.StatusSuccess {
			// For other commands that succeed, handle normally
			switch currentCmd.Slug {
			case node_command.SlugShutdown:
				if err := s.nodeService.UpdateStatusWithAccount(ctx, currentCmd.NodeID, currentCmd.AccountID, string(node.StatusShutdown)); err != nil {
					s.logger.Warn("failed to set node to shutdown", "node_id", currentCmd.NodeID, "error", err)
				}
			default:
				// For other commands, use standard status update logic
				if err := s.updateNodeStatusForCommand(ctx, currentCmd.NodeID, currentCmd.AccountID, nodeCommandStatus, currentCmd.Slug); err != nil {
					s.logger.Warn("failed to update node status for command", "node_id", currentCmd.NodeID, "error", err)
				}
			}
		} else {
			// For non-success statuses, use standard status update logic
			if err := s.updateNodeStatusForCommand(ctx, currentCmd.NodeID, currentCmd.AccountID, nodeCommandStatus, currentCmd.Slug); err != nil {
				s.logger.Warn("failed to update node status for command", "node_id", currentCmd.NodeID, "error", err)
			}
		}
	}

	// Get updated command for WebSocket event
	updatedCmd, err := s.repo.GetByIDForUpdate(ctx, nodeCommandID)
	if err == nil {
		s.notifyNodeCommandUpdated(updatedCmd)
	}

	// Notify workflow execution service if command is part of a workflow
	// For reboot commands, we don't notify yet - we'll notify when node comes back online
	if s.workflowExecutionService != nil && (nodeCommandStatus == node_command.StatusSuccess || nodeCommandStatus == node_command.StatusFailed) && !isRebootCommand {
		s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, nodeCommandID)
	}

	return true, nil
}

// HasActiveCommands checks if a node has any commands in progress
func (s *Service) HasActiveCommands(ctx context.Context, nodeID uuid.UUID) (bool, error) {
	return s.repo.HasActiveCommands(ctx, nodeID)
}

// FindActiveDeviceCommand reports which command currently owns this node's disk,
// if any. The slug set lives in the domain so the guard and the database query
// cannot drift apart.
func (s *Service) FindActiveDeviceCommand(ctx context.Context, nodeID uuid.UUID) (*node_command.NodeCommand, error) {
	return s.repo.FindActiveDeviceCommand(ctx, nodeID, node_command.DeviceExclusiveSlugs())
}

// LatestSucceededCommand returns the slug of the node's most recent
// successful command among slugs, "" when none is on record.
func (s *Service) LatestSucceededCommand(ctx context.Context, accountID, nodeID uuid.UUID, slugs []string) (string, error) {
	return s.repo.LatestSucceededSlugByNode(ctx, accountID, nodeID, slugs)
}

// lastBootOf reads the node's boot moment, or nil when it cannot be established
// — the node service is not wired, the row is unreadable, or the agent has never
// reported an uptime. Every one of those means "unknown", and the caller treats
// unknown as the old, permissive behaviour rather than as "has not booted".
func (s *Service) lastBootOf(ctx context.Context, nodeID, accountID uuid.UUID) *time.Time {
	if s.nodeService == nil {
		return nil
	}
	n, err := s.nodeService.GetByIDAndAccount(ctx, nodeID, accountID)
	if err != nil || n == nil {
		return nil
	}
	return n.LastBootAt
}

// CompleteRebootCommandsForNode completes the reboot commands a node has
// actually PERFORMED, and is called whenever it reconnects or reports fresh
// system info.
//
// "Reconnected" is not "rebooted". A reboot command carries a delay of up to an
// hour (node_command.MaxDelaySeconds), and any reconnect at all used to settle
// it: a backend deploy that brings the fleet back completed the command,
// released the `restarting` pin and let a playbook continue — and the device
// then rebooted up to an hour later, mid-run.
//
// The proof is node.LastBootAt, which is derived from the agent's UPTIME on the
// SERVER's clock, so it is immune both to the device's wall clock (a blank RTC
// puts it in the year 2000) and to skew between the two machines. A reboot
// command is complete only once the node's boot moment post-dates the moment the
// command was created.
//
// That stamp is refreshed in UpdateSystemInfo, not at ConnectionInit, which is
// why this is called from both: at connect the node's LastBootAt is still the
// PREVIOUS boot, so nothing completes, and the first system-info report settles
// it.
//
// A node that reports no boot moment at all — an agent too old to send uptime —
// falls back to the old behaviour and completes on reconnect. Fail-closed there
// would hang every reboot on those nodes forever, with no way for an operator to
// clear it.
func (s *Service) CompleteRebootCommandsForNode(ctx context.Context, nodeID uuid.UUID) error {
	// Find all in-progress reboot commands for this node
	commands, err := s.repo.GetInProgressRebootCommandsByNodeID(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get in-progress reboot commands: %w", err)
	}

	if len(commands) == 0 {
		// No pending reboot commands, nothing to do
		return nil
	}

	bootedAt := s.lastBootOf(ctx, nodeID, commands[0].AccountID)

	// Mark each command as success
	var accountID uuid.UUID
	var completed int
	for _, cmd := range commands {
		accountID = cmd.AccountID
		if bootedAt != nil && !bootedAt.After(cmd.CreatedAt) {
			// The node has not booted since this command was issued. Most often
			// a delayed reboot that has not fired yet, and completing it here
			// would release the run that is waiting for it.
			s.logger.InfoContext(ctx, "reboot command left pending: the node has not booted since it was issued",
				"command_id", cmd.ID, "node_id", nodeID,
				"issued_at", cmd.CreatedAt, "last_boot_at", *bootedAt)
			continue
		}
		completed++
		// Update command status to success
		if err := s.setStatus(ctx, cmd, node_command.StatusSuccess, 100, "Reboot completed, node is back online", true); err != nil {
			s.logger.Warn("failed to mark reboot command as success", "command_id", cmd.ID, "node_id", nodeID, "error", err)
			continue
		}

		// Get updated command for WebSocket event
		updatedCmd, err := s.repo.GetByIDForUpdate(ctx, cmd.ID)
		if err == nil {
			s.notifyNodeCommandUpdated(updatedCmd)
		}

		// Notify workflow execution service if command is part of a workflow
		if s.workflowExecutionService != nil {
			s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, cmd.ID)
		}
	}

	if completed == 0 {
		// Nothing settled, so the node keeps whatever pin it has: releasing it
		// here would say the reboot is over while the device is still going to
		// take it.
		return nil
	}
	s.logger.Info("completed reboot commands for node", "node_id", nodeID, "count", completed)

	// Revert the node out of "maintenance" if nothing else is active.
	if err := // No slug: this releases the node after a BATCH of reboot commands, not
		// for one of them.
		s.updateNodeStatusForCommand(ctx, nodeID, accountID, node_command.StatusSuccess, ""); err != nil {
		s.logger.Warn("failed to update node status after completing reboots", "node_id", nodeID, "error", err)
	}

	return nil
}

// updateNodeStatusForCommand updates node status based on command status changes.
//
// slug is the command driving the transition, or "" where the caller is
// releasing the node after a batch rather than acting for one command. A
// non-disruptive slug returns without touching the status at ALL — not just
// the maintenance pin but the revert to online too, because a command that
// never took the node out of service has no business putting it back in.
func (s *Service) updateNodeStatusForCommand(ctx context.Context, nodeID, accountID uuid.UUID, commandStatus node_command.NodeCommandStatus, slug string) error {
	if s.nodeService == nil {
		return nil // NodeService not set yet
	}
	if node_command.IsNonDisruptive(slug) {
		return nil
	}

	// Check if node has active commands
	hasActive, err := s.repo.HasActiveCommands(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to check active commands: %w", err)
	}

	// If command is starting (registered, sent, in_progress), set node to maintenance if online
	if commandStatus == node_command.StatusRegistered || commandStatus == node_command.StatusSent || commandStatus == node_command.StatusInProgress {
		if hasActive {
			// Set to maintenance - UpdateStatus will update regardless of current status
			// This is fine because maintenance is a valid state for nodes with active commands
			if err := s.nodeService.UpdateStatusWithAccountPinnedBy(ctx, nodeID, accountID, string(node.StatusMaintenance), slug); err != nil {
				return fmt.Errorf("failed to set node to maintenance: %w", err)
			}
		}
	} else if commandStatus == node_command.StatusSuccess || commandStatus == node_command.StatusFailed {
		// Command completed - check if there are other active commands
		if !hasActive {
			// No more active commands - set back to online
			// This will update the status to online regardless of current status
			// If node was offline or ready_for_adoption, it will become online (which might not be desired)
			// But in practice, if a node has commands, it should be online/maintenance
			if err := s.nodeService.UpdateStatusWithAccount(ctx, nodeID, accountID, string(node.StatusOnline)); err != nil {
				return fmt.Errorf("failed to set node to online: %w", err)
			}
		}
		// If hasActive is true, keep as maintenance (other commands still running)
		// Don't update status - it should already be maintenance
	}

	return nil
}

// FailStalePendingCommandsForNode marks non-reboot commands abandoned by a
// previous agent session as failed when a node (re)connects: those still in
// "registered"/"sent" (never acknowledged) and those stuck "in_progress" long
// past their last progress report (the agent died mid-execution). Neither
// resumes across sessions, so left alone they hang forever and keep the node
// pinned in "maintenance". Reboot is handled separately by
// CompleteRebootCommandsForNode; queueable commands are exempt from the
// unacknowledged arm and are redelivered instead (see
// RedeliverQueuedCommandsForNode). No-op when nothing is pending.
// The continuity verdict comes from the agent_instance_id comparison at
// ConnectionInit (see domain/node.AgentInstanceContinuity): Same skips the
// in_progress arm at the repository (that work is still being executed),
// Changed returns every in_progress command immediately, Unknown keeps the
// grace-period heuristic. Callers with no verdict pass Unknown.
func (s *Service) FailStalePendingCommandsForNode(ctx context.Context, nodeID uuid.UUID, continuity node.AgentInstanceContinuity) error {
	commands, err := s.repo.GetStalePendingNonRebootCommandsByNodeID(ctx, nodeID, continuity)
	if err != nil {
		return fmt.Errorf("failed to get stale pending commands: %w", err)
	}
	if len(commands) == 0 {
		return nil
	}

	s.logger.Info("failing stale pending commands for node", "node_id", nodeID, "count", len(commands))

	var accountID uuid.UUID
	for _, cmd := range commands {
		accountID = cmd.AccountID
		reason := "Command abandoned: the agent reconnected before acknowledging it"
		if cmd.Status == node_command.StatusInProgress {
			reason = "Command abandoned: the agent stopped reporting progress and reconnected"
			if continuity == node.AgentInstanceChanged {
				// Not a guess in this arm: the reconnecting process is a
				// different one, so the process that was executing this is gone.
				reason = "Command abandoned: the agent restarted before this command completed"
			}
		}
		if err := s.setStatus(ctx, cmd, node_command.StatusFailed, 0, reason, true); err != nil {
			s.logger.Warn("failed to mark stale command as failed", "command_id", cmd.ID, "node_id", nodeID, "error", err)
			continue
		}
		if updatedCmd, err := s.repo.GetByIDForUpdate(ctx, cmd.ID); err == nil {
			s.notifyNodeCommandUpdated(updatedCmd)
		}
		if s.workflowExecutionService != nil {
			s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, cmd.ID)
		}
	}

	// Revert the node out of "maintenance" if nothing else is active.
	if err := // No slug: same batch release, after clearing stale commands.
		s.updateNodeStatusForCommand(ctx, nodeID, accountID, node_command.StatusFailed, ""); err != nil {
		s.logger.Warn("failed to update node status after clearing stale commands", "node_id", nodeID, "error", err)
	}
	return nil
}

// abandonedSilenceGrace is how long an in_progress command may go without a
// word before the periodic backstop settles it.
//
// Three times the reconnect grace, because this arm judges a node that is
// still CONNECTED and the only evidence is silence. The case it exists for:
// an update_agent replaces the agent's own binary, and a command running in
// the same process fails as it shuts down — the failure reaches the device's
// journal and never reaches the platform. The replacement agent reconnects
// within milliseconds, so the reconnect sweep finds nothing to sweep and never
// looks again; without this clock the command would sit in_progress on an
// online node indefinitely, with every action on it withheld.
const abandonedSilenceGrace = 30 * time.Minute

// abandonedSweepLimit bounds one pass. A backlog larger than this is settled
// over successive passes rather than in one transaction-hogging sweep.
const abandonedSweepLimit = 200

// reasonNoTerminalReport is what an operator reads on a command the backstop
// gave up on. It says what the platform observed, not what happened on the
// device: a lost report and a wedged execution look identical from here, and
// the device's own journal is where the difference lives.
const reasonNoTerminalReport = "The node stopped reporting on this command and never finished it. Check the agent journal on the device for what it was doing"

// FailAbandonedInProgressCommands settles in_progress commands that have gone
// silent, across every account. Returns how many it settled.
//
// The third sweep, and the only one not triggered by a node going away. The
// other two fire on reconnect and on the unreachable verdict, so a command
// orphaned on a node that stays connected — a lost terminal report — was
// invisible to both. Device-exclusive slugs are excluded in the query; see
// GetAbandonedInProgressCommands for why a clock must not judge those.
func (s *Service) FailAbandonedInProgressCommands(ctx context.Context) (int, error) {
	commands, err := s.repo.GetAbandonedInProgressCommands(ctx, time.Now().Add(-abandonedSilenceGrace), abandonedSweepLimit)
	if err != nil {
		return 0, fmt.Errorf("failed to list abandoned commands: %w", err)
	}

	// Grouped so a node whose several commands were all abandoned has its
	// status released once, not once per command.
	touched := map[uuid.UUID]uuid.UUID{}
	settled := 0
	for _, cmd := range commands {
		if err := s.setStatus(ctx, cmd, node_command.StatusFailed, 0, reasonNoTerminalReport, true); err != nil {
			s.logger.Warn("failed to settle an abandoned command", "command_id", cmd.ID, "node_id", cmd.NodeID, "error", err)
			continue
		}
		settled++
		touched[cmd.NodeID] = cmd.AccountID
		if updatedCmd, gerr := s.repo.GetByIDForUpdate(ctx, cmd.ID); gerr == nil {
			s.notifyNodeCommandUpdated(updatedCmd)
		}
		// A workflow waiting on this would otherwise stay active forever, the
		// same wound the unreachable sweep closes by the other route.
		if s.workflowExecutionService != nil {
			s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, cmd.ID)
		}
	}

	for nodeID, accountID := range touched {
		// Releases "maintenance" when nothing else is active. Safe here in a
		// way it is not for an unreachable node: this arm only ever looks at
		// nodes the platform still hears from (the query excludes offline ones).
		if err := s.updateNodeStatusForCommand(ctx, nodeID, accountID, node_command.StatusFailed, ""); err != nil {
			s.logger.Warn("failed to release node status after settling abandoned commands", "node_id", nodeID, "error", err)
		}
	}
	if settled > 0 {
		s.logger.Info("settled commands that stopped reporting", "count", settled, "nodes", len(touched))
	}

	offline, err := s.failCommandsOfOfflineNodes(ctx)
	settled += offline
	if err != nil {
		return settled, err
	}
	return settled, nil
}

// offlineSilenceGrace is how long a command on an OFFLINE node may stay
// unsettled before the backstop fails it.
//
// The node being away is the evidence, so the grace is measured on BOTH
// clocks: the command has said nothing for this long AND the node itself has
// not been seen for this long. A command's own silence proves nothing — a
// capture's filesystem shrink or an install's download from storage can run
// quiet for longer than this, and neither needs the agent's stream — so a
// brief disconnect must not fail work that is still running. A late report on
// a command already settled is refused, which is why settling early would
// discard a real result.
const offlineSilenceGrace = 30 * time.Minute

// failCommandsOfOfflineNodes settles the commands a node was carrying when it
// went offline and never came back to report on.
//
// The monitor marks a stale node offline directly from 'maintenance' — only
// 'restarting' and 'shutdown' are held for the unreachable-node verdict — so a
// device that died mid-install_os reached 'offline' with its install still
// in_progress, and nothing looked at it again: the unreachable sweep only
// visits nodes still in a transient status, and the connected-node backstop
// skips device-exclusive slugs. Until the device reconnected the install read
// "in progress" forever, and any workflow waiting on it never finished.
//
// The node's status is NOT touched: it is offline, which is the truth, and the
// status helper's "nothing active left, back to online" rule would lie.
func (s *Service) failCommandsOfOfflineNodes(ctx context.Context) (int, error) {
	commands, err := s.repo.GetUnsettledCommandsOfOfflineNodes(ctx, time.Now().Add(-offlineSilenceGrace), abandonedSweepLimit)
	if err != nil {
		return 0, fmt.Errorf("failed to list commands of offline nodes: %w", err)
	}
	if len(commands) == 0 {
		return 0, nil
	}

	// Per node, the slug to stamp on its offline status: a reboot if one was
	// settled, else the last command settled — FailCommandsForUnreachableNode's
	// rule, so the two routes read the same in the UI.
	stamps := map[uuid.UUID]string{}
	settled := 0
	for _, cmd := range commands {
		reason := reasonNodeStoppedAnswering
		if cmd.Slug == node_command.SlugReboot {
			reason = reasonRebootNeverReturned
		}
		if err := s.setStatus(ctx, cmd, node_command.StatusFailed, 0, reason, true); err != nil {
			s.logger.Warn("failed to settle a command of an offline node", "command_id", cmd.ID, "node_id", cmd.NodeID, "error", err)
			continue
		}
		settled++
		if stamps[cmd.NodeID] != node_command.SlugReboot {
			stamps[cmd.NodeID] = cmd.Slug
		}
		if updatedCmd, gerr := s.repo.GetByIDForUpdate(ctx, cmd.ID); gerr == nil {
			s.notifyNodeCommandUpdated(updatedCmd)
		}
		if s.workflowExecutionService != nil {
			s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, cmd.ID)
		}
	}
	if s.nodeService != nil {
		for nodeID, slug := range stamps {
			if err := s.nodeService.MarkAwaitingReturn(ctx, nodeID, slug); err != nil {
				s.logger.Warn("failed to stamp awaiting-return", "node_id", nodeID, "error", err)
			}
		}
	}
	if settled > 0 {
		s.logger.Info("settled commands of nodes that went offline mid-command", "count", settled, "nodes", len(stamps))
	}
	return settled, nil
}

// reasonNodeDeleted is what an operator reads on a command that was still
// outstanding when its node was deleted.
const reasonNodeDeleted = "The node was deleted before this command completed"

// SettleCommandsForDeletedNode settles every outstanding command of a node
// that is being deleted, and returns how many it settled.
//
// Without it a deleted node's queue outlived the node: a queued command was
// still delivered if the node was restored, hours later and to whatever state
// the device was in by then, and an in_progress one read "in progress" for as
// long as the row existed. Queued commands are EXPIRED (they never left the
// platform); delivered ones are FAILED.
//
// keepSlugs are left alone: the delete itself dispatches release_identity to a
// connected node just before calling this, and that command must stay live.
func (s *Service) SettleCommandsForDeletedNode(ctx context.Context, nodeID uuid.UUID, keepSlugs ...string) (int, error) {
	commands, err := s.repo.GetUnsettledCommandsByNodeID(ctx, nodeID)
	if err != nil {
		return 0, fmt.Errorf("failed to list outstanding commands of a deleted node: %w", err)
	}
	settled := 0
	for _, cmd := range commands {
		if slices.Contains(keepSlugs, cmd.Slug) {
			continue
		}
		status := node_command.StatusFailed
		if cmd.Status == node_command.StatusRegistered {
			status = node_command.StatusExpired
		}
		// The row is gone, so there is no boot target to force here; Delete
		// records netboot on the tombstone when an install was in flight.
		if err := s.setStatus(ctx, cmd, status, 0, reasonNodeDeleted, false); err != nil {
			s.logger.Warn("failed to settle a command of a deleted node", "command_id", cmd.ID, "node_id", nodeID, "error", err)
			continue
		}
		settled++
		if updatedCmd, gerr := s.repo.GetByIDForUpdate(ctx, cmd.ID); gerr == nil {
			s.notifyNodeCommandUpdated(updatedCmd)
		}
		if s.workflowExecutionService != nil {
			s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, cmd.ID)
		}
	}
	return settled, nil
}

// FailCommandsForUnreachableNodeReason is what the operator reads on a command
// the platform gave up on. Two wordings, because "the node did not come back"
// is a diagnosis and "it stopped responding" is only an observation — and for a
// reboot the platform genuinely does know which one it is.
const (
	reasonRebootNeverReturned  = "The node did not come back after the reboot"
	reasonNodeStoppedAnswering = "The node stopped responding before this command completed"
)

// FailCommandsForUnreachableNode settles the commands of a node the monitor has
// concluded is not coming back.
//
// The same 10-minute abandonment rule as FailStalePendingCommandsForNode, run
// from the OTHER trigger. That difference is the whole point: the rule was
// wired only to a reconnect, so a device that dies mid-command — by far the
// commoner case — left the command non-terminal forever. A workflow waiting on
// it never finishes, it counts against the per-account cap of active
// executions, and the three scan endpoints show "Probing…" until someone
// deletes the node.
//
// Reboot is INCLUDED here and excluded from the reconnect path, for symmetric
// reasons: on reconnect a reboot has just succeeded, and here it certainly has
// not.
//
// The node's own status is deliberately NOT touched: the caller has already
// decided the device is unreachable and owns the transition to 'offline'.
// Routing it through updateNodeStatusForCommand instead would set the node
// ONLINE — that helper's rule is "no active commands left, so back to online",
// which is right after a reconnect and wrong for a corpse.
func (s *Service) FailCommandsForUnreachableNode(ctx context.Context, nodeID uuid.UUID) (int, error) {
	commands, err := s.repo.GetStalePendingCommandsByNodeID(ctx, nodeID)
	if err != nil {
		return 0, fmt.Errorf("failed to get pending commands for unreachable node: %w", err)
	}
	if len(commands) == 0 {
		return 0, nil
	}

	failed := 0
	for _, cmd := range commands {
		reason := reasonNodeStoppedAnswering
		if cmd.Slug == node_command.SlugReboot {
			reason = reasonRebootNeverReturned
		}
		if err := s.setStatus(ctx, cmd, node_command.StatusFailed, 0, reason, true); err != nil {
			s.logger.Warn("failed to fail command for unreachable node", "command_id", cmd.ID, "node_id", nodeID, "error", err)
			continue
		}
		failed++
		if updatedCmd, err := s.repo.GetByIDForUpdate(ctx, cmd.ID); err == nil {
			s.notifyNodeCommandUpdated(updatedCmd)
		}
		// Without this a workflow execution stays active forever on a node that
		// is gone: the same wound as an execution left active after its
		// node is deleted, reached by a commoner route.
		if s.workflowExecutionService != nil {
			s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, cmd.ID)
		}
	}
	if failed > 0 {
		s.logger.Info("failed commands for a node that stopped responding", "node_id", nodeID, "count", failed)
		// Stamp what the node was doing when it stopped answering, so its
		// OFFLINE reads as "never came back after <slug>" in the UI instead of
		// a bare offline. Prefer the reboot — that is the command whose whole
		// meaning was "come back" — else the last command settled. Best-effort:
		// the stamp is UI honesty, not bookkeeping the sweep depends on.
		stamp := commands[len(commands)-1].Slug
		for _, cmd := range commands {
			if cmd.Slug == node_command.SlugReboot {
				stamp = cmd.Slug
				break
			}
		}
		if s.nodeService != nil {
			if err := s.nodeService.MarkAwaitingReturn(ctx, nodeID, stamp); err != nil {
				s.logger.Warn("failed to stamp awaiting-return", "node_id", nodeID, "error", err)
			}
		}
	}
	return failed, nil
}

// AgentVersionChecker reports the agent version a node currently runs, so
// redelivery can re-apply the version gate: a node may return with an older
// agent than it had when the command was queued (rollback, reimage).
type AgentVersionChecker interface {
	AgentVersionForNode(ctx context.Context, accountID, nodeID uuid.UUID) (string, error)
}

// SetCommandDispatcher wires the channel used to hand a command to the gRPC
// server, plus the agent-version lookup used when redelivering.
func (s *Service) SetCommandDispatcher(client node_commands.NodeCommandClient, versions AgentVersionChecker) {
	s.commandClient = client
	s.agentVersions = versions
}

// RedeliverQueuedCommandsForNode dispatches commands that were queued while the
// node was offline. Called on reconnect, AFTER the connection is registered —
// the dispatch path checks connectivity, so redelivering earlier would fail
// every command instantly.
//
// Expired commands and those past MaxDeliveryAttempts are retired here rather
// than sent. The version gate is re-applied because the node may have come back
// on an older agent than the one it had when the command was queued.
func (s *Service) RedeliverQueuedCommandsForNode(ctx context.Context, nodeID uuid.UUID) error {
	if s.commandClient == nil {
		return nil
	}

	// Commands delivered but never acknowledged return to pending first,
	// otherwise they are stranded: the stale reaper exempts them and the query
	// below only sees "registered".
	if n, err := s.repo.ResetSentQueueableToRegistered(ctx, nodeID); err != nil {
		s.logger.Warn("failed to requeue unacknowledged commands", "node_id", nodeID, "error", err)
	} else if n > 0 {
		s.logger.Info("requeued unacknowledged commands", "node_id", nodeID, "count", n)
	}

	commands, err := s.repo.GetQueueableRegisteredByNodeID(ctx, nodeID, maxRedeliverBatch)
	if err != nil {
		return fmt.Errorf("failed to load queued commands: %w", err)
	}

	// Commands that only make sense on a managed OS are NOT delivered here: at
	// connect time the stored system info still describes the PREVIOUS boot, so
	// a node that just netbooted into recovery can look disk-booted (and vice
	// versa). They are delivered by RedeliverManagedOSQueuedCommandsForNode once
	// the session's first system-info report shows a non-recovery boot.
	deliverable := commands[:0]
	deferred := 0
	for _, cmd := range commands {
		if node_command.RequiresManagedOS(cmd.Slug) {
			deferred++
			continue
		}
		deliverable = append(deliverable, cmd)
	}
	if deferred > 0 {
		s.logger.Info("deferred queued commands until a managed-OS boot is confirmed",
			"node_id", nodeID, "count", deferred)
	}

	s.redeliverCommands(ctx, nodeID, deliverable)
	return nil
}

// RedeliverManagedOSQueuedCommandsForNode delivers queued commands that must
// run on a managed OS. Called when a system-info report confirms the node is
// NOT in the recovery bootstrap (current boot device != "http") — the only
// moment that fact is fresh rather than inherited from the previous boot.
// No-op when nothing matching is queued.
func (s *Service) RedeliverManagedOSQueuedCommandsForNode(ctx context.Context, nodeID uuid.UUID) error {
	if s.commandClient == nil {
		return nil
	}
	commands, err := s.repo.GetQueueableRegisteredByNodeID(ctx, nodeID, maxRedeliverBatch)
	if err != nil {
		return fmt.Errorf("failed to load queued commands: %w", err)
	}
	deliverable := commands[:0]
	for _, cmd := range commands {
		if node_command.RequiresManagedOS(cmd.Slug) {
			deliverable = append(deliverable, cmd)
		}
	}
	s.redeliverCommands(ctx, nodeID, deliverable)
	return nil
}

// redeliverCommands runs the shared delivery loop for queued commands: retire
// the expired and over-attempted, re-apply the agent-version gate, and hand the
// rest to the dispatch channel.
func (s *Service) redeliverCommands(ctx context.Context, nodeID uuid.UUID, commands []node_command.NodeCommand) {
	if len(commands) == 0 {
		return
	}

	now := time.Now()
	for _, cmd := range commands {
		if cmd.ExpiresAt != nil && cmd.ExpiresAt.Before(now) {
			s.retireQueuedCommand(ctx, cmd, node_command.StatusExpired,
				"Command expired before the node reconnected; re-apply the current configuration")
			continue
		}
		if cmd.Attempts >= node_command.MaxDeliveryAttempts {
			s.retireQueuedCommand(ctx, cmd, node_command.StatusFailed,
				fmt.Sprintf("Command could not be delivered after %d attempts", cmd.Attempts))
			continue
		}
		if min, required := node_command.MinAgentVersion(cmd.Slug); required && s.agentVersions != nil {
			version, verr := s.agentVersions.AgentVersionForNode(ctx, cmd.AccountID, cmd.NodeID)
			if verr != nil {
				s.logger.Warn("failed to read agent version for queued command",
					"command_id", cmd.ID, "node_id", nodeID, "error", verr)
				continue
			}
			if !node_command.MeetsMinAgentVersion(version, min) {
				// Skipped, not failed: the node came back on an agent too old for
				// this command (a rollback/reimage), which is a precondition, not
				// a failure to execute.
				s.retireQueuedCommand(ctx, cmd, node_command.StatusSkipped,
					fmt.Sprintf("Agent %s is older than the %s this command requires", displayVersion(version), min))
				continue
			}
		}

		if err := s.repo.IncrementAttempts(ctx, cmd.ID); err != nil {
			s.logger.Warn("failed to record delivery attempt", "command_id", cmd.ID, "error", err)
		}
		sendOp := node_commands.NodeCommandOperation{
			Type: node_commands.OperationTypeSendCommand,
			SendCommand: &node_commands.SendCommandOperation{
				NodeCommandID: cmd.ID,
				NodeID:        cmd.NodeID,
				AccountID:     cmd.AccountID,
			},
		}
		if err := s.commandClient.Send(sendOp); err != nil {
			s.logger.Warn("failed to queue redelivery", "command_id", cmd.ID, "node_id", nodeID, "error", err)
			continue
		}
		s.logger.Info("redelivered queued command", "command_id", cmd.ID, "node_id", nodeID, "slug", cmd.Slug)
	}
}

// retireQueuedCommand moves a queued command to a terminal state and notifies
// listeners, so a command that will never be delivered stops looking pending.
func (s *Service) retireQueuedCommand(ctx context.Context, cmd node_command.NodeCommand, status node_command.NodeCommandStatus, reason string) {
	if err := s.setStatus(ctx, cmd, status, 0, reason, true); err != nil {
		s.logger.Warn("failed to retire queued command", "command_id", cmd.ID, "status", status, "error", err)
		return
	}
	s.logger.Info("retired queued command", "command_id", cmd.ID, "node_id", cmd.NodeID, "status", status, "reason", reason)
	if updated, err := s.repo.GetByIDForUpdate(ctx, cmd.ID); err == nil {
		s.notifyNodeCommandUpdated(updated)
	}
	if s.workflowExecutionService != nil {
		s.workflowExecutionService.ProcessNodeCommandCompletion(ctx, cmd.ID)
	}
}

// displayVersion renders an agent version for operator-facing messages, naming
// the empty case rather than printing nothing.
func displayVersion(v string) string {
	if v == "" {
		return "(unknown version)"
	}
	return v
}

// isTerminalStatus reports a status no further agent report may change.
func isTerminalStatus(status node_command.NodeCommandStatus) bool {
	switch status {
	case node_command.StatusSuccess, node_command.StatusFailed,
		node_command.StatusSuperseded, node_command.StatusExpired,
		node_command.StatusSkipped:
		return true
	}
	return false
}

// GetNodeCommandByID gets a node command by ID with account validation (for HTTP queries)
func (s *Service) GetNodeCommandByID(ctx context.Context, accountID, nodeCommandID uuid.UUID) (*node_command.NodeCommand, error) {
	return s.repo.GetByID(ctx, accountID, nodeCommandID)
}

// GetNodeCommandsByNodeID gets paginated list of node commands for a node (for HTTP queries)
func (s *Service) GetNodeCommandsByNodeID(ctx context.Context, accountID, nodeID uuid.UUID, limit, offset int) ([]node_command.NodeCommand, error) {
	return s.repo.GetByNodeID(ctx, accountID, nodeID, limit, offset)
}

// scopeOUFor resolves the OU a command's node belongs to, so the WebSocket
// frame can be withheld from members who cannot read that unit.
//
// Returns (nil, false) when the OU cannot be determined — the caller then falls
// back to the unassigned scope, which reaches account-wide holders only. That
// is deliberate: an unresolvable node must not widen the audience.
func (s *Service) scopeOUFor(nodeCmd *node_command.NodeCommand) (*uuid.UUID, bool) {
	if s.nodeService == nil {
		return nil, false
	}
	n, err := s.nodeService.GetByIDAndAccount(context.Background(), nodeCmd.NodeID, nodeCmd.AccountID)
	if err != nil || n == nil {
		return nil, false
	}
	return n.OUID, true
}

// commandMessage addresses a command frame the way the REST command reads
// serve the command: to node.read holders of the node's unit, except that a
// command the run view withholds from a member without secret.read_value
// reaches only holders of that action — its mere presence answers a
// secret-fed condition. The decision is the REST filter's own function, asked
// only for a command a run dispatched. It fails closed: unwired, or unable to
// decide, the frame goes to secret.read_value holders only.
func (s *Service) commandMessage(nodeCmd *node_command.NodeCommand, frame websockets3.MessageWrapper) websockets3.Message {
	ouID, _ := s.scopeOUFor(nodeCmd)
	msg := websockets3.NewAccountMessageForOU(nodeCmd.AccountID, ouID, frame)
	if !s.visibleBelowSecretRead(nodeCmd) {
		msg = msg.WithRedactedVariant(authz.ActionSecretReadValue, nil)
	}
	return msg
}

// visibleBelowSecretRead reports whether a member without secret.read_value
// may see the command at all.
func (s *Service) visibleBelowSecretRead(nodeCmd *node_command.NodeCommand) bool {
	if nodeCmd.WorkflowExecutionNodeID == nil {
		return true
	}
	if s.runCommands == nil {
		return false
	}
	visible, err := s.runCommands.WithoutHiddenRunCommands(context.Background(), nodeCmd.AccountID, []node_command.NodeCommand{*nodeCmd})
	if err != nil {
		s.logger.Warn("cannot decide who may see a run command; sending its frame to secret.read_value holders only",
			"node_command_id", nodeCmd.ID, "error", err)
		return false
	}
	return len(visible) == 1
}

// notifyNodeCommandCreated emits a WebSocket event when a node command is created.
//
// The payload is OMITTED here, unlike the HTTP endpoints which redact
// per-caller. A WebSocket frame has no caller to evaluate
// ActionSecretReadValue against, so it cannot make that decision — and sending
// a "[redacted]" placeholder would be worse than sending nothing: clients merge
// pushed frames into a store that may already hold the real payload a
// privileged user fetched over HTTP, so the placeholder would overwrite data
// they are entitled to. An absent field lets the client keep what it has; one
// that needs the payload re-fetches through GET /node-commands/:id, where the
// permission check actually happens.
//
// The agent-reported DETAILS follow the same channel rule, but only for a
// command that carried secret material — see CommandDetailsForBroadcast. The
// live progress line is the point of this frame, and dropping it for every
// reboot would spend the whole feature to close a leak that only exists where a
// resolved {{secrets.*}} could be echoed back.
func (s *Service) notifyNodeCommandCreated(nodeCmd *node_command.NodeCommand) {
	// Convert to result type for WebSocket payload
	result := &results.NodeCommandResult{
		ID:        nodeCmd.ID.String(),
		NodeID:    nodeCmd.NodeID.String(),
		AccountID: nodeCmd.AccountID.String(),
		Slug:      nodeCmd.Slug,
		Payload:   nil,
		Status:    nodeCmd.Status.String(),
		Progress:  nodeCmd.Progress,
		Details:   results.CommandDetailsForBroadcast(nodeCmd.Details, nodeCmd.Payload),
		CreatedAt: nodeCmd.CreatedAt,
		UpdatedAt: nodeCmd.UpdatedAt,
	}

	msg := s.commandMessage(nodeCmd, websockets3.MessageWrapper{
		Type:    "node_command_created",
		Payload: result,
	})

	if err := s.websocketClient.Send(msg); err != nil {
		s.logger.Error("failed to send node command created event", "error", err, "node_command_id", nodeCmd.ID)
	}
}

// notifyNodeCommandUpdated emits a WebSocket event when a node command is
// updated. The payload is omitted — see notifyNodeCommandCreated for why the
// WebSocket channel cannot make this decision per recipient.
func (s *Service) notifyNodeCommandUpdated(nodeCmd *node_command.NodeCommand) {
	// Every caller passes a command read from its row, which carries the run
	// link already.
	var workflowExecutionNodeID *string
	if execNodeID := nodeCmd.WorkflowExecutionNodeID; execNodeID != nil {
		execNodeIDStr := execNodeID.String()
		workflowExecutionNodeID = &execNodeIDStr
	}

	// Convert to result type for WebSocket payload
	result := &results.NodeCommandResult{
		ID:                      nodeCmd.ID.String(),
		NodeID:                  nodeCmd.NodeID.String(),
		AccountID:               nodeCmd.AccountID.String(),
		Slug:                    nodeCmd.Slug,
		Payload:                 nil,
		Status:                  nodeCmd.Status.String(),
		Progress:                nodeCmd.Progress,
		Details:                 results.CommandDetailsForBroadcast(nodeCmd.Details, nodeCmd.Payload),
		WorkflowExecutionNodeID: workflowExecutionNodeID,
		CreatedAt:               nodeCmd.CreatedAt,
		UpdatedAt:               nodeCmd.UpdatedAt,
	}

	msg := s.commandMessage(nodeCmd, websockets3.MessageWrapper{
		Type:    "node_command_updated",
		Payload: result,
	})

	if err := s.websocketClient.Send(msg); err != nil {
		s.logger.Error("failed to send node command updated event", "error", err, "node_command_id", nodeCmd.ID)
	}
}
