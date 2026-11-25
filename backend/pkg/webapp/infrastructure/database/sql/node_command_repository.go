package sql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Verify interface implementation
var _ repository.NodeCommandRepository = (*NodeCommandRepository)(nil)

// NodeCommandRepository is the implementation of the node command repository using SQL
type NodeCommandRepository struct {
	db *gorm.DB
	// encryptor protects the secret-bearing payload/result fields at rest. The
	// API redacted them for callers while the database kept plaintext, so every
	// secret a playbook resolved was readable from a backup.
	encryptor commandFieldEncryptor
}

// autoMigrate creates the necessary database tables
func (r *NodeCommandRepository) autoMigrate() error {
	return r.db.AutoMigrate(&NodeCommandModel{})
}

// Setup initializes the repository and creates tables if needed
// Setup migrates the schema ONLY. The legacy-encryption sweep used to run here,
// and main.go treats every Setup() failure as os.Exit(1) — so a hygiene backfill
// over a table with no retention could crash-loop a live install under
// `restart: always`. The schema migration must stay fatal (a missing column
// breaks every query); the sweep must not, so it moved to BackfillEncryption.
func (r *NodeCommandRepository) Setup() error {
	return r.autoMigrate()
}

// BackfillEncryption encrypts legacy plaintext secret-bearing payload and result
// values. Idempotent, and deliberately called NON-fatally from main.go: a
// failure means "some rows are still plaintext", which the platform serves
// correctly, whereas aborting startup turns a hygiene task into an outage. This
// is the pattern the four provisioning-password backfills already use.
//
// It still runs on every boot rather than recording completion, and that is a
// decision rather than an oversight: an older backend deployed over this data
// writes plaintext again, and a permanent "done" marker would mean nothing ever
// repairs it. The same reasoning is why node_files keeps its repair sweep. What
// the sweep must not be is fatal.
func (r *NodeCommandRepository) BackfillEncryption() error {
	return backfillNodeCommandEncryption(r.db, r.encryptor)
}

// NewNodeCommandRepository creates a new node command repository
func NewNodeCommandRepository(db *gorm.DB, encryptor commandFieldEncryptor) *NodeCommandRepository {
	if encryptor == nil {
		panic("NodeCommandRepository requires an encryptor")
	}
	return &NodeCommandRepository{
		db:        db,
		encryptor: encryptor,
	}
}

// sealDetails prepares the details column for a raw column write.
//
// Details is not written through the model on these paths — a status update
// touches three columns, not a whole row — so the encryption the model does has
// to be done here too, or the same text is sealed on create and plaintext on
// every update after it. Two things happen in order, and both matter:
//
//   - URL credentials are stripped FIRST. The agent's transport errors and the
//     platform's own capability URLs carry them in a query string, and
//     encryption is not a licence to store one — a rotation makes the row
//     readable again, and every reader of the decrypted value would see it.
//   - Then the text is sealed, so it is not in a database dump or (on an
//     install that logs interpolated statements) in the container logs.
//
// Returns nil for empty details so the column is NULL rather than an encrypted
// empty string, which no reader distinguishes from text.
func (r *NodeCommandRepository) sealDetails(details string) interface{} {
	if details == "" {
		return nil
	}
	redacted := node_command.RedactURLCredentialsInDetails(details)
	sealed, ok := encryptCommandDetails(redacted, r.encryptor, false)
	if !ok {
		// Dropping the text loses an explanation; storing it in the clear is
		// what this exists to prevent, and the status still says what happened.
		slog.Error("dropping node-command details it could not encrypt")
		return nil
	}
	return sealed
}

// Create creates a new node command
func (r *NodeCommandRepository) Create(ctx context.Context, nodeCommand *node_command.NodeCommand) error {
	return r.CreateWithWorkflowNode(ctx, nodeCommand, nil)
}

// CreateWithWorkflowNode creates a new node command with optional workflow_execution_node_id
func (r *NodeCommandRepository) CreateWithWorkflowNode(ctx context.Context, nodeCommand *node_command.NodeCommand, workflowExecutionNodeID *uuid.UUID) error {
	model := &NodeCommandModel{}
	model.FromDomain(nodeCommand, r.encryptor)
	model.WorkflowExecutionNodeID = workflowExecutionNodeID

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// GetByID retrieves a node command by ID within an account (for HTTP queries)
func (r *NodeCommandRepository) GetByID(ctx context.Context, accountID, nodeCommandID uuid.UUID) (*node_command.NodeCommand, error) {
	var model NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", nodeCommandID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeCommandNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(r.encryptor), nil
}

// GetByIDForUpdate retrieves a node command by ID for update (no account check, for gRPC status updates)
func (r *NodeCommandRepository) GetByIDForUpdate(ctx context.Context, nodeCommandID uuid.UUID) (*node_command.NodeCommand, error) {
	var model NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("id = ?", nodeCommandID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeCommandNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(r.encryptor), nil
}

// GetRegisteredByNodeID retrieves registered commands for a node (called by gRPC server)
func (r *NodeCommandRepository) GetRegisteredByNodeID(ctx context.Context, nodeID uuid.UUID, limit int) ([]node_command.NodeCommand, error) {
	var models []NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("node_id = ? AND status = ?", nodeID, string(node_command.StatusRegistered)).
		Order("created_at ASC").
		Limit(limit).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	commands := make([]node_command.NodeCommand, len(models))
	for i, model := range models {
		domainCmd := model.ToDomain(r.encryptor)
		commands[i] = *domainCmd
	}

	return commands, nil
}

// ResetSentQueueableToRegistered returns queueable commands that were delivered
// but never acknowledged to the pending state, so reconnect redelivery picks
// them up. Without this they would be stranded: the stale reaper exempts
// queueable commands, and redelivery only looks at "registered". Re-sending is
// safe because every queueable command is declarative and idempotent.
func (r *NodeCommandRepository) ResetSentQueueableToRegistered(ctx context.Context, nodeID uuid.UUID) (int64, error) {
	result := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("node_id = ? AND status = ? AND queueable = ?",
			nodeID, string(node_command.StatusSent), true).
		Updates(map[string]interface{}{
			"status":     string(node_command.StatusRegistered),
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// GetQueueableRegisteredByNodeID retrieves registered commands that may wait
// for an offline node, oldest first. Used on reconnect to redeliver what was
// queued while the node was away.
func (r *NodeCommandRepository) GetQueueableRegisteredByNodeID(ctx context.Context, nodeID uuid.UUID, limit int) ([]node_command.NodeCommand, error) {
	var models []NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("node_id = ? AND status = ? AND queueable = ?",
			nodeID, string(node_command.StatusRegistered), true).
		Order("created_at ASC").
		Limit(limit).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	commands := make([]node_command.NodeCommand, len(models))
	for i, model := range models {
		commands[i] = *model.ToDomain(r.encryptor)
	}

	return commands, nil
}

// GetExpiredQueuedCommands returns queued commands whose TTL has passed. Used
// by the sweeper for nodes that never reconnect: without it their commands sit
// in "registered" forever and read as still pending.
func (r *NodeCommandRepository) GetExpiredQueuedCommands(ctx context.Context, now time.Time, limit int) ([]node_command.NodeCommand, error) {
	var models []NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("status = ? AND queueable = ? AND expires_at IS NOT NULL AND expires_at < ?",
			string(node_command.StatusRegistered), true, now).
		Order("expires_at ASC").
		Limit(limit).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	commands := make([]node_command.NodeCommand, len(models))
	for i, model := range models {
		commands[i] = *model.ToDomain(r.encryptor)
	}
	return commands, nil
}

// SupersedeRegisteredByKey marks still-unsent commands sharing a supersede key
// as superseded, so a newer desired state replaces a queued older one instead
// of both applying in sequence. Deliberately limited to "registered": a command
// already sent or in progress is executing on the node and cannot be recalled.
//
// Returns the ids it superseded — not just a count — so the caller can run the
// same completion contract retireQueuedCommand honours (notify +
// ProcessNodeCommandCompletion) for each. Without that, a superseded command
// linked to a workflow execution node leaves its block stuck `running` forever:
// completed_at never lands, retention never sweeps it, and after 20 the account's
// active-execution cap answers 429 for every playbook. The
// SELECT ... FOR UPDATE + UPDATE run in one transaction so a command cannot slip
// from registered to sent between reading the ids and retiring them.
//
// createdBefore bounds the sweep to commands OLDER than the caller's own: two
// concurrent registrations of the same slug would otherwise each retire the
// other — each excludes only its own id — leaving the node with ZERO pending
// commands. With the bound, only the newest survives; a exact
// created_at tie retires neither, which converges anyway (the commands are
// declarative and idempotent, and applying the newer one last wins).
func (r *NodeCommandRepository) SupersedeRegisteredByKey(ctx context.Context, nodeID uuid.UUID, supersedeKey string, excludeID uuid.UUID, createdBefore time.Time) ([]uuid.UUID, error) {
	if supersedeKey == "" {
		return nil, nil
	}
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&NodeCommandModel{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("node_id = ? AND supersede_key = ? AND status = ? AND id <> ? AND created_at < ?",
				nodeID, supersedeKey, string(node_command.StatusRegistered), excludeID, createdBefore).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Model(&NodeCommandModel{}).
			Where("id IN ?", ids).
			Updates(map[string]interface{}{
				"status":     string(node_command.StatusSuperseded),
				"details":    r.sealDetails("Replaced by a newer configuration before delivery"),
				"updated_at": time.Now(),
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// IncrementAttempts records a delivery try so a command that keeps failing to
// reach its node cannot be redelivered forever.
func (r *NodeCommandRepository) IncrementAttempts(ctx context.Context, nodeCommandID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("id = ?", nodeCommandID).
		UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error
}

// GetInProgressRebootCommandsByNodeID retrieves reboot commands awaiting the
// node's return: acknowledged ones (in_progress) and delivered-but-never-acked
// ones (sent — the agent went down for the reboot before acking, or an agent
// restart swallowed the ack). Both complete when the node reconnects;
// excluding "sent" left such reboots hanging forever and pinned the node in
// "maintenance".
func (r *NodeCommandRepository) GetInProgressRebootCommandsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_command.NodeCommand, error) {
	var models []NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("node_id = ? AND status IN ? AND slug = ?",
			nodeID,
			[]string{string(node_command.StatusInProgress), string(node_command.StatusSent)},
			node_command.SlugReboot).
		Order("created_at ASC").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	commands := make([]node_command.NodeCommand, len(models))
	for i, model := range models {
		domainCmd := model.ToDomain(r.encryptor)
		commands[i] = *domainCmd
	}

	return commands, nil
}

// abandonedInProgressGrace is how long an "in_progress" command may go without
// a progress report before a reconnect is allowed to declare it abandoned. A
// live long-running command (install_os) reports progress continuously, so its
// updated_at stays fresh across a mere stream blip; a command whose agent died
// mid-execution never updates again.
const abandonedInProgressGrace = 10 * time.Minute

// GetStalePendingNonRebootCommandsByNodeID returns commands a previous agent
// session abandoned for a node, excluding reboot (which legitimately completes
// on reconnect). Used to clear them so they don't hang forever and pin the node
// in "maintenance". Two cases:
//
//   - "registered"/"sent" and untouched for abandonedInProgressGrace: delivered
//     but never acknowledged, with no redelivery. The grace is what separates
//     "the agent process is gone" from "the stream blipped and the agent is
//     still holding it" — the two are indistinguishable at the instant of a
//     reconnect, and without it a reverse-proxy restart would fail every
//     command in flight across the fleet.
//   - "in_progress" with no progress report for abandonedInProgressGrace: the
//     agent died mid-execution (e.g. update_boot_order reboots the device to
//     apply the EEPROM and never reports again). Execution does not resume
//     across sessions, so it will never complete on its own.
func (r *NodeCommandRepository) GetStalePendingNonRebootCommandsByNodeID(ctx context.Context, nodeID uuid.UUID, continuity node.AgentInstanceContinuity) ([]node_command.NodeCommand, error) {
	return r.stalePendingCommands(ctx, nodeID, true, continuity)
}

// GetStalePendingCommandsByNodeID is the same query INCLUDING reboot, for the
// node that never came back.
//
// The reboot exclusion above is correct for a reconnect — a reboot completes
// precisely by the node reconnecting, so failing it there would fail the
// command at the moment it succeeded. It is exactly wrong for the opposite
// trigger: when the sweeper concludes the device is not returning, its reboot
// is the one command that certainly did not complete, and leaving it
// non-terminal is what pinned the node in 'restarting' forever.
func (r *NodeCommandRepository) GetStalePendingCommandsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_command.NodeCommand, error) {
	// The unreachable-node sweeper has no reconnect and therefore no instance
	// id to compare — it always uses the grace-period heuristic.
	return r.stalePendingCommands(ctx, nodeID, false, node.AgentInstanceUnknown)
}

// stalePendingCommands holds the predicate both callers share, so a change to
// what counts as abandoned cannot apply to one trigger and not the other.
// The continuity verdict (from the agent_instance_id in ConnectionInit)
// replaces the guess for the in_progress arm when it is available:
//
//   - Same: the process that acknowledged those commands reconnected and is
//     still executing them — the arm is skipped entirely. Failing them here is
//     the original defect: any disconnect longer than the grace made a live
//     install_os read as abandoned, because updated_at cannot advance while
//     the stream is down.
//   - Changed: a different process — execution does not resume across agent
//     restarts, so every in_progress command of the previous session is
//     certainly lost and is returned regardless of updated_at. Before the
//     verdict existed a crash-and-quick-reconnect left them hanging until the
//     grace expired AND another reconnect happened.
//   - Unknown (an agent below the field's floor, or no stored id): the
//     grace-period heuristic, exactly as before.
//
// The registered/sent arm keeps its grace in every mode, including Same: a
// frame lost mid-disconnect was never delivered, so "the process survived"
// says nothing about whether it ever received the command.
func (r *NodeCommandRepository) stalePendingCommands(ctx context.Context, nodeID uuid.UUID, excludeReboot bool, continuity node.AgentInstanceContinuity) ([]node_command.NodeCommand, error) {
	var models []NodeCommandModel
	// Queueable commands are exempt from the registered/sent arm: they are meant
	// to outlive a disconnect and are redelivered on reconnect instead of being
	// failed. They stay subject to the in_progress arm — an agent that died
	// mid-execution genuinely did not finish.
	//
	// The unacknowledged arm carries the SAME grace as the in_progress one. A
	// stream that blips — a reverse-proxy restart, a network switch, a backend
	// deploy — reconnects the node without restarting the agent PROCESS, which
	// is still holding the command and will still report it. Failing instantly
	// would settle a command the device is about to run, and on install_os that
	// means a run reported as failed while the disk is being written.
	//
	// The grace costs a genuinely lost command ten more minutes of pending, and
	// nothing else: while it is non-terminal the agent's own report still lands
	// and wins. That is the cheaper side of the trade.
	staleBefore := time.Now().Add(-abandonedInProgressGrace)
	unacked := "(status IN ? AND queueable = ? AND updated_at < ?)"
	unackedArgs := []interface{}{
		[]string{string(node_command.StatusRegistered), string(node_command.StatusSent)},
		false,
		staleBefore,
	}
	var predicate string
	args := []interface{}{nodeID}
	switch continuity {
	case node.AgentInstanceSame:
		predicate = unacked
		args = append(args, unackedArgs...)
	case node.AgentInstanceChanged:
		predicate = unacked + " OR status = ?"
		args = append(args, unackedArgs...)
		args = append(args, string(node_command.StatusInProgress))
	default:
		predicate = unacked + " OR (status = ? AND updated_at < ?)"
		args = append(args, unackedArgs...)
		args = append(args, string(node_command.StatusInProgress), staleBefore)
	}
	query := r.db.WithContext(ctx).
		Where("node_id = ? AND ("+predicate+")", args...)
	if excludeReboot {
		query = query.Where("slug <> ?", node_command.SlugReboot)
	}

	result := query.Order("created_at ASC").Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	commands := make([]node_command.NodeCommand, len(models))
	for i, model := range models {
		domainCmd := model.ToDomain(r.encryptor)
		commands[i] = *domainCmd
	}

	return commands, nil
}

// DeleteByWorkflowExecutionNodeIDs deletes the commands issued by the given
// workflow execution nodes (retention sweeps them with their executions).
// DeleteTerminalOlderThan implements the age-based sweep.
//
// The terminal-status filter lives HERE, in the statement, and not in the
// caller. A `registered` command is not old history: queueable slugs
// (apply_access_config, set_hostname, patch_file, apply_watchdog_config) stay
// registered for exactly as long as their node is offline, which can be
// months, and `sent`/`in_progress` are still owed a settle. Deleting either by
// age would silently drop work the platform promised to deliver on reconnect —
// so a future caller cannot get this wrong by passing a different filter.
//
// The subquery bounds one round: DELETE has no LIMIT in Postgres, and an
// unbounded first sweep over a year of per-minute cron history would hold row
// locks for the duration.
func (r *NodeCommandRepository) DeleteTerminalOlderThan(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	terminal := []string{
		string(node_command.StatusSuccess),
		string(node_command.StatusFailed),
		string(node_command.StatusSuperseded),
		string(node_command.StatusExpired),
		string(node_command.StatusSkipped),
	}
	result := r.db.WithContext(ctx).
		Where("id IN (?)",
			r.db.Model(&NodeCommandModel{}).
				Select("id").
				Where("created_at < ? AND status IN ?", cutoff, terminal).
				Limit(limit)).
		Delete(&NodeCommandModel{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// CountActiveByAccountAndSlug counts an account's live commands of one slug —
// the utility fleet-pacing bound (docs/UTILITIES-PLAN.md §3.7).
//
// `registered` is deliberately NOT counted: for a queueable slug it means
// "waiting for an offline (or netbooted) node", possibly for the whole 24 h
// TTL, and counting those let one large offline OU eat the entire budget and
// defer applies to nodes that are actually online.
// What the bound exists to limit is DEVICES WORKING RIGHT NOW — a live apply
// pins its node in maintenance — and that is sent/in_progress. The moment a
// queued command redelivers it turns sent and starts counting.
func (r *NodeCommandRepository) CountActiveByAccountAndSlug(ctx context.Context, accountID uuid.UUID, slug string) (int64, error) {
	active := []string{
		string(node_command.StatusSent),
		string(node_command.StatusInProgress),
	}
	var count int64
	err := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("account_id = ? AND slug = ? AND status IN ?", accountID, slug, active).
		Count(&count).Error
	return count, err
}

// LatestSucceededSlugByNode returns the slug of the node's most recent
// command among slugs that ended in success, "" when there is none on record.
// Ordered by creation: the commands this answers about take the node's disk
// for themselves and never overlap, so the newest created is the newest
// finished. Terminal rows are pruned by retention.
func (r *NodeCommandRepository) LatestSucceededSlugByNode(ctx context.Context, accountID, nodeID uuid.UUID, slugs []string) (string, error) {
	var found []string
	err := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("account_id = ? AND node_id = ? AND slug IN ? AND status = ?", accountID, nodeID, slugs, string(node_command.StatusSuccess)).
		Order("created_at DESC").
		Limit(1).
		Pluck("slug", &found).Error
	if err != nil || len(found) == 0 {
		return "", err
	}
	return found[0], nil
}

// CountLiveByNodeAndSlug counts one NODE's commands of a slug that are not
// yet terminal — registered (queued) included, unlike the pacing counter
// above: the reconcile loop asks "does anything already own this node's
// convergence", and a queued apply owns it just as much as a sent one.
func (r *NodeCommandRepository) CountLiveByNodeAndSlug(ctx context.Context, accountID, nodeID uuid.UUID, slug string) (int64, error) {
	live := []string{
		string(node_command.StatusRegistered),
		string(node_command.StatusSent),
		string(node_command.StatusInProgress),
	}
	var count int64
	err := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("account_id = ? AND node_id = ? AND slug = ? AND status IN ?", accountID, nodeID, slug, live).
		Count(&count).Error
	return count, err
}

func (r *NodeCommandRepository) DeleteByWorkflowExecutionNodeIDs(ctx context.Context, workflowExecutionNodeIDs []uuid.UUID) (int64, error) {
	if len(workflowExecutionNodeIDs) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).
		Where("workflow_execution_node_id IN ?", workflowExecutionNodeIDs).
		Delete(&NodeCommandModel{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// GetByNodeID retrieves node commands for a node with pagination (for HTTP queries)
func (r *NodeCommandRepository) GetByNodeID(ctx context.Context, accountID, nodeID uuid.UUID, limit, offset int) ([]node_command.NodeCommand, error) {
	var models []NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("node_id = ? AND account_id = ?", nodeID, accountID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	commands := make([]node_command.NodeCommand, len(models))
	for i, model := range models {
		domainCmd := model.ToDomain(r.encryptor)
		commands[i] = *domainCmd
	}

	return commands, nil
}

// Update updates an existing node command
func (r *NodeCommandRepository) Update(ctx context.Context, nodeCommand *node_command.NodeCommand) error {
	model := &NodeCommandModel{}
	model.FromDomain(nodeCommand, r.encryptor)

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	result := updateAllColumns(r.db.WithContext(ctx).
		Where("id = ?", nodeCommand.ID), model)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrNodeCommandNotFound
	}

	return nil
}

// UpdateStatus updates the status, progress, and details of a node command
func (r *NodeCommandRepository) UpdateStatus(ctx context.Context, nodeCommandID uuid.UUID, status node_command.NodeCommandStatus, progress int, details string) error {
	updates := map[string]interface{}{
		"status":     string(status),
		"progress":   progress,
		"updated_at": time.Now(),
	}

	updates["details"] = r.sealDetails(details)

	result := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("id = ?", nodeCommandID).
		Updates(updates)

	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrNodeCommandNotFound
	}

	return nil
}

// SetResult persists the structured agent outputs for a command (workflow blocks).
//
// This is THE write path for agent-reported outputs, and it updates the column
// directly rather than going through FromDomain — so it has to encrypt for
// itself. Missing that would have left everything read_file reads and
// run_script prints sitting in the clear, invisibly: the read path passes
// unprefixed values straight through, so nothing would have looked wrong.
func (r *NodeCommandRepository) SetResult(ctx context.Context, nodeCommandID uuid.UUID, result map[string]string) error {
	var raw []byte
	if len(result) > 0 {
		encrypted, err := encryptCommandResult(result, r.encryptor, false)
		if err != nil {
			// Storing the outputs in the clear is the one outcome worse than
			// losing them: refuse the write and let the caller see it fail.
			return fmt.Errorf("refusing to store command result unencrypted: %w", err)
		}
		if b, err := json.Marshal(encrypted); err == nil {
			raw = b
		}
	}
	res := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("id = ?", nodeCommandID).
		Update("result", raw)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return app_errors.ErrNodeCommandNotFound
	}
	return nil
}

// UpdateStatusIfCurrent updates status only if current status matches currentStatus (atomic conditional update)
func (r *NodeCommandRepository) UpdateStatusIfCurrent(ctx context.Context, nodeCommandID uuid.UUID, currentStatus, newStatus node_command.NodeCommandStatus, progress int, details string) (bool, error) {
	updates := map[string]interface{}{
		"status":     string(newStatus),
		"progress":   progress,
		"updated_at": time.Now(),
	}

	updates["details"] = r.sealDetails(details)

	result := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("id = ? AND status = ?", nodeCommandID, string(currentStatus)).
		Updates(updates)

	if result.Error != nil {
		return false, result.Error
	}

	// Return true if update was successful (rows affected > 0)
	return result.RowsAffected > 0, nil
}

// HasActiveCommands checks if a node has any commands with status registered, sent, or in_progress
func (r *NodeCommandRepository) HasActiveCommands(ctx context.Context, nodeID uuid.UUID) (bool, error) {
	var count int64
	// A queued-but-unsent queueable command does not make the node busy: it may
	// sit there for hours waiting for an offline node, and counting it would pin
	// the whole fleet in "maintenance" for the duration. Once actually sent it
	// counts like any other command.
	//
	// Nor does a non-disruptive one (identify): it changes nothing on the device,
	// so it must neither put the node into maintenance nor HOLD it there — and
	// the hold is the sharper half, because a lost terminal report would
	// otherwise leave the node reading as under maintenance until the
	// abandonment sweep, over a blinking LED.
	result := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("node_id = ? AND slug NOT IN ? AND (status IN ? OR (status = ? AND queueable = ?))",
			nodeID,
			node_command.NonDisruptiveSlugs(),
			[]string{string(node_command.StatusSent), string(node_command.StatusInProgress)},
			string(node_command.StatusRegistered),
			false,
		).
		Count(&count)
	if result.Error != nil {
		return false, result.Error
	}
	return count > 0, nil
}

// FindActiveDeviceCommand returns the oldest command for this node that owns a
// block device and has not finished, or nil when there is none.
//
// "Not finished" includes `registered`: unlike HasActiveCommands, a queued
// device command DOES block another. That distinction is the whole point —
// HasActiveCommands answers "is the node busy right now", which a command
// waiting for an offline node is not, while this answers "is a disk already
// spoken for", which it very much is. None of these slugs is queueable today, so
// the case is theoretical; encoding it here means it stays right if one becomes.
func (r *NodeCommandRepository) FindActiveDeviceCommand(ctx context.Context, nodeID uuid.UUID, slugs []string) (*node_command.NodeCommand, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	var model NodeCommandModel
	err := r.db.WithContext(ctx).
		Where("node_id = ? AND slug IN ? AND status IN ?",
			nodeID, slugs,
			[]string{
				string(node_command.StatusRegistered),
				string(node_command.StatusSent),
				string(node_command.StatusInProgress),
			},
		).
		Order("created_at ASC").
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return model.ToDomain(r.encryptor), nil
}

// GetByWorkflowExecutionNodeID retrieves all node commands for a workflow execution node
func (r *NodeCommandRepository) GetByWorkflowExecutionNodeID(ctx context.Context, workflowExecutionNodeID uuid.UUID) ([]node_command.NodeCommand, error) {
	var models []NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("workflow_execution_node_id = ?", workflowExecutionNodeID).
		Order("created_at ASC").
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	commands := make([]node_command.NodeCommand, len(models))
	for i, model := range models {
		domainCmd := model.ToDomain(r.encryptor)
		commands[i] = *domainCmd
	}

	return commands, nil
}

// GetWorkflowExecutionNodeID gets the workflow_execution_node_id for a command
func (r *NodeCommandRepository) GetWorkflowExecutionNodeID(ctx context.Context, nodeCommandID uuid.UUID) (*uuid.UUID, error) {
	var model NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("id = ?", nodeCommandID).
		Select("workflow_execution_node_id").
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeCommandNotFound
		}
		return nil, result.Error
	}

	return model.WorkflowExecutionNodeID, nil
}

// GetWorkflowLink gets the owning account/node and the workflow link of a
// command in one projection, without locking the row.
func (r *NodeCommandRepository) GetWorkflowLink(ctx context.Context, nodeCommandID uuid.UUID) (*node_command.WorkflowLink, error) {
	var model NodeCommandModel
	result := r.db.WithContext(ctx).
		Where("id = ?", nodeCommandID).
		Select("account_id", "node_id", "workflow_execution_node_id").
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNodeCommandNotFound
		}
		return nil, result.Error
	}

	return &node_command.WorkflowLink{
		AccountID:       model.AccountID,
		NodeID:          model.NodeID,
		ExecutionNodeID: model.WorkflowExecutionNodeID,
	}, nil
}

// UpdateWorkflowExecutionNodeID updates the workflow_execution_node_id for a command
func (r *NodeCommandRepository) UpdateWorkflowExecutionNodeID(ctx context.Context, nodeCommandID uuid.UUID, workflowExecutionNodeID *uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Where("id = ?", nodeCommandID).
		Update("workflow_execution_node_id", workflowExecutionNodeID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrNodeCommandNotFound
	}
	return nil
}

// ListActivity returns one page of the account's commands, newest first, plus
// the total matching the filter.
//
// The count and the page derive from ONE filtered base so they cannot
// disagree, and both ride the (account_id, created_at) composite index.
func (r *NodeCommandRepository) ListActivity(ctx context.Context, accountID uuid.UUID, filter repository.NodeCommandActivityFilter, offset, limit int) ([]node_command.NodeCommand, int64, error) {
	// A non-nil but empty visibility set is "this caller may see no node".
	// Answering it with an unfiltered query would hand an OU-scoped member the
	// whole account's activity.
	if filter.NodeIDs != nil && len(filter.NodeIDs) == 0 {
		return nil, 0, nil
	}

	base := r.db.WithContext(ctx).Model(&NodeCommandModel{}).Where("account_id = ?", accountID)
	if filter.NodeIDs != nil {
		base = base.Where("node_id IN ?", filter.NodeIDs)
	}
	if len(filter.Statuses) > 0 {
		base = base.Where("status IN ?", filter.Statuses)
	}
	if filter.Slug != "" {
		base = base.Where("slug = ?", filter.Slug)
	}
	if filter.Since != nil {
		base = base.Where("created_at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		base = base.Where("created_at <= ?", *filter.Until)
	}

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var models []NodeCommandModel
	if err := base.Session(&gorm.Session{}).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, 0, err
	}

	commands := make([]node_command.NodeCommand, len(models))
	for i := range models {
		commands[i] = *models[i].ToDomain(r.encryptor)
	}
	return commands, total, nil
}

// BackfillBootloaderFlashedAt seeds nodes.bootloader_flashed_at from the
// command log: the settle time of each node's most recent successful
// update_boot_order. The settle hook stamps the column going forward; this
// covers flashes that settled before the column existed. MONOTONIC, not
// NULL-only: the settle hook's write is warn-only, so a stamp it missed over
// an older non-NULL value would otherwise stay wrong forever — this way
// every boot is also the repair pass, and it can never move a stamp
// backwards. Called non-fatally from main.go (the BackfillEncryption
// reasoning: a hygiene backfill must not crash-loop a live install).
func (r *NodeCommandRepository) BackfillBootloaderFlashedAt() error {
	return r.db.Exec(`UPDATE nodes SET bootloader_flashed_at = sub.settled_at
		FROM (SELECT node_id, MAX(updated_at) AS settled_at FROM node_commands
			WHERE slug = ? AND status = ? GROUP BY node_id) sub
		WHERE nodes.id = sub.node_id
			AND (nodes.bootloader_flashed_at IS NULL OR nodes.bootloader_flashed_at < sub.settled_at)`,
		string(node_command.SlugUpdateBootOrder), string(node_command.StatusSuccess)).Error
}

// NodesWithActiveCommands returns each node's unsettled command.
//
// Newest first per node, so a node carrying several reports the one most
// recently started. Projected: this answers "what is the platform doing here",
// and hydrating the rows would decrypt every payload to learn nothing more.
func (r *NodeCommandRepository) NodesWithActiveCommands(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]repository.ActiveCommand, error) {
	var rows []struct {
		NodeID    uuid.UUID
		Slug      string
		Progress  int
		UpdatedAt time.Time
	}
	if err := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Select("DISTINCT ON (node_id) node_id, slug, progress, updated_at").
		Where("account_id = ? AND status IN ?", accountID, []string{
			string(node_command.StatusRegistered),
			string(node_command.StatusSent),
			string(node_command.StatusInProgress),
		}).
		Order("node_id, created_at DESC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]repository.ActiveCommand, len(rows))
	for _, row := range rows {
		out[row.NodeID] = repository.ActiveCommand{
			Slug:      row.Slug,
			Progress:  row.Progress,
			UpdatedAt: row.UpdatedAt,
		}
	}
	return out, nil
}

// LatestAttempts returns each node's most recent command per slug.
//
// DISTINCT ON does the "latest per group" in one indexed pass; the ORDER BY
// must lead with the same expressions or Postgres refuses it. No details, no
// payload, no result: see the port's note on why this is a projection.
func (r *NodeCommandRepository) LatestAttempts(ctx context.Context, accountID uuid.UUID, slugs []string) (map[uuid.UUID]map[string]repository.CommandAttempt, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	var rows []struct {
		ID        uuid.UUID
		NodeID    uuid.UUID
		Slug      string
		Status    string
		UpdatedAt time.Time
	}
	if err := r.db.WithContext(ctx).
		Model(&NodeCommandModel{}).
		Select("DISTINCT ON (node_id, slug) id, node_id, slug, status, updated_at").
		Where("account_id = ? AND slug IN ?", accountID, slugs).
		Order("node_id, slug, created_at DESC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]map[string]repository.CommandAttempt, len(rows))
	for _, row := range rows {
		bySlug := out[row.NodeID]
		if bySlug == nil {
			bySlug = map[string]repository.CommandAttempt{}
			out[row.NodeID] = bySlug
		}
		bySlug[row.Slug] = repository.CommandAttempt{
			CommandID: row.ID,
			Status:    row.Status,
			At:        row.UpdatedAt,
		}
	}
	return out, nil
}

// GetAbandonedInProgressCommands returns in_progress commands that have gone
// quiet for longer than staleBefore, for the periodic backstop.
//
// Device-exclusive slugs are EXCLUDED. install_os, erase_drive and
// capture_image are the operations that legitimately run for a long time, and
// a capture streaming a multi-gigabyte disk is the one case where "silent" and
// "wedged" cannot be told apart by a clock. Failing one of those mid-flight
// would destroy real work to tidy up bookkeeping; they keep the two
// evidence-based sweeps, which fire when the agent is demonstrably gone rather
// than when a timer expires.
//
// Commands of OFFLINE nodes are excluded too: they belong to
// GetUnsettledCommandsOfOfflineNodes, whose settle leaves the node's status
// alone. This pass releases the node back to online afterwards, which is only
// true of a node the platform still hears from.
func (r *NodeCommandRepository) GetAbandonedInProgressCommands(ctx context.Context, staleBefore time.Time, limit int) ([]node_command.NodeCommand, error) {
	excluded := make([]string, 0, 8)
	for _, slug := range node_command.DeviceExclusiveSlugs() {
		excluded = append(excluded, slug)
	}
	var models []NodeCommandModel
	if err := r.db.WithContext(ctx).
		Where("status = ? AND updated_at < ?", string(node_command.StatusInProgress), staleBefore).
		Where("slug NOT IN ?", excluded).
		Where("node_id NOT IN (?)", r.offlineNodeIDs(ctx)).
		Order("updated_at ASC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]node_command.NodeCommand, len(models))
	for i := range models {
		out[i] = *models[i].ToDomain(r.encryptor)
	}
	return out, nil
}

// offlineNodeIDs is the subquery naming every node the monitor has declared
// offline. One definition for both sweeps, so a command cannot fall between
// them or be claimed by both.
func (r *NodeCommandRepository) offlineNodeIDs(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).
		Model(&NodeModel{}).
		Select("id").
		Where("status = ?", string(node.StatusOffline))
}

// GetUnsettledCommandsOfOfflineNodes returns the commands still owed an outcome
// on nodes that are offline, have said nothing about them since staleBefore,
// and have not themselves been seen since staleBefore: in_progress ones of
// every slug, and delivered-or-registered ones that cannot wait for a
// reconnect (queueable commands are excluded — waiting for an offline node is
// their purpose, and their TTL sweep retires them).
//
// Device-exclusive slugs are INCLUDED here, unlike in the connected-node
// backstop. There a clock cannot tell a slow capture from a wedged one; here
// the evidence is the node itself being gone — which is why the node's own
// last sighting must be as old as the command's: a node marked offline a
// minute ago by a dropped stream may still be shrinking a filesystem or
// downloading an image, neither of which reports through that stream.
func (r *NodeCommandRepository) GetUnsettledCommandsOfOfflineNodes(ctx context.Context, staleBefore time.Time, limit int) ([]node_command.NodeCommand, error) {
	awayNodes := r.offlineNodeIDs(ctx).
		Where("(last_seen_at IS NULL OR last_seen_at < ?) AND (last_heartbeat IS NULL OR last_heartbeat < ?)",
			staleBefore, staleBefore)
	var models []NodeCommandModel
	if err := r.db.WithContext(ctx).
		Where("updated_at < ?", staleBefore).
		Where("(status = ? OR (status IN ? AND queueable = ?))",
			string(node_command.StatusInProgress),
			[]string{string(node_command.StatusRegistered), string(node_command.StatusSent)},
			false).
		Where("node_id IN (?)", awayNodes).
		Order("updated_at ASC").
		Limit(limit).
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]node_command.NodeCommand, len(models))
	for i := range models {
		out[i] = *models[i].ToDomain(r.encryptor)
	}
	return out, nil
}

// GetUnsettledCommandsByNodeID returns every non-terminal command of one node,
// oldest first, for settling them when the node is deleted.
func (r *NodeCommandRepository) GetUnsettledCommandsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_command.NodeCommand, error) {
	var models []NodeCommandModel
	if err := r.db.WithContext(ctx).
		Where("node_id = ? AND status IN ?", nodeID, []string{
			string(node_command.StatusRegistered),
			string(node_command.StatusSent),
			string(node_command.StatusInProgress),
		}).
		Order("created_at ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]node_command.NodeCommand, len(models))
	for i := range models {
		out[i] = *models[i].ToDomain(r.encryptor)
	}
	return out, nil
}
