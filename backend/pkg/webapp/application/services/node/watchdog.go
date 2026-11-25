package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// watchdogCommandScanLimit bounds the recent-command page GetWatchdog scans for
// the newest converge command. Watchdog converges are rare; a small page always
// contains the newest one when any is recent enough to matter.
const watchdogCommandScanLimit = 20

// GetWatchdog returns the node's watchdog state.
func (s *Service) GetWatchdog(ctx context.Context, accountID, nodeID uuid.UUID) (*node.WatchdogState, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	state := &node.WatchdogState{
		Desired:      n.WatchdogConfig,
		AppliedHash:  n.WatchdogAppliedHash,
		DisarmReason: n.WatchdogDisarmReason,
		LastRefusal:  n.WatchdogLastRefusal,
	}
	if n.WatchdogConfig != nil {
		state.DesiredHash = n.WatchdogConfig.Hash()
		state.PublicTargets = n.WatchdogConfig.PublicTargets()
		state.DesiredSource = string(organizational_unit.SourceNode)
	} else if s.deviceProfileResolver != nil {
		// No per-node config: the OU chain may declare one (the LED pattern).
		// Shown with its source; applying it stays an explicit per-node action
		// — nothing arms a device without a human present.
		if cfg, source, ok, err := s.deviceProfileResolver.ResolveEffectiveWatchdogConfig(ctx, accountID, nodeID); err == nil && ok {
			inherited := cfg
			state.Desired = &inherited
			state.DesiredHash = inherited.Hash()
			state.DesiredSource = string(source)
			state.PublicTargets = inherited.PublicTargets()
		}
	}
	state.InSync = state.DesiredHash != "" && state.DesiredHash == state.AppliedHash
	s.projectWatchdogCommand(ctx, accountID, nodeID, state)
	return state, nil
}

// projectWatchdogCommand fills LastCommand and Queued from the newest
// apply_watchdog_config command. Best-effort: the watchdog state is about the
// node, and a command-listing failure must not hide it.
func (s *Service) projectWatchdogCommand(ctx context.Context, accountID, nodeID uuid.UUID, state *node.WatchdogState) {
	if s.nodeCommandService == nil {
		return
	}
	cmds, err := s.nodeCommandService.GetNodeCommandsByNodeID(ctx, accountID, nodeID, watchdogCommandScanLimit, 0)
	if err != nil {
		s.logger.Warn("failed to project the watchdog converge command", "node_id", nodeID, "error", err)
		return
	}
	for i := range cmds { // newest first (created_at DESC)
		if cmds[i].Slug != node_command.SlugApplyWatchdogConfig {
			continue
		}
		state.LastCommand = &node.WatchdogCommandRef{
			ID:      cmds[i].ID,
			Status:  cmds[i].Status.String(),
			Details: cmds[i].Details,
		}
		// Registered = accepted but not yet delivered. "Sent" and beyond are on
		// the device; only a still-registered command is honestly "queued".
		state.Queued = cmds[i].Status == node_command.StatusRegistered
		return
	}
}

// SetWatchdog validates a desired configuration, stores it, and dispatches the
// converge command.
//
// The command is queueable: it is declarative and idempotent, so applying it on
// reconnect still means the same thing. Remember that a queued command counts as
// dispatched but has NOT been applied — the caller shows desired-vs-applied, not
// "done".
func (s *Service) SetWatchdog(
	ctx context.Context,
	accountID, userID, nodeID uuid.UUID,
	cfg node.WatchdogConfig,
) (*node.WatchdogState, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}

	n.WatchdogConfig = &cfg
	// The desired-config column only. This runs from an operator request, and a
	// full-row save would carry the rest of the row — status, connection state,
	// the agent's reported facts — back from a read taken moments ago, reverting
	// whatever the gRPC layer wrote in between.
	cfgJSON, err := json.Marshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode watchdog config: %w", err)
	}
	cfgRaw := string(cfgJSON)

	params := map[string]interface{}{
		"config_hash":          cfg.Hash(),
		"enabled":              cfg.Enabled,
		"os_hang":              cfg.OSHang,
		"watchdog_timeout_sec": float64(cfg.WatchdogTimeoutSec),
		"network":              cfg.Network,
		"ping_targets":         toInterfaceSlice(cfg.PingTargets),
		"ping_count":           float64(cfg.PingCount),
		"interface":            cfg.Interface,
		"agent_health":         cfg.AgentHealth,
		"agent_change_sec":     float64(cfg.AgentChangeSec),
		"max_load_1":           float64(cfg.MaxLoad1),
		"max_load_5":           float64(cfg.MaxLoad5),
		"max_load_15":          float64(cfg.MaxLoad15),
		"min_memory_pages":     float64(cfg.MinMemoryPages),
		"max_temperature_c":    float64(cfg.MaxTemperatureC),
		"interval_sec":         float64(cfg.IntervalSec),
		"retry_timeout_sec":    float64(cfg.RetryTimeoutSec),
		// The platform always asks for the on-device checks. They are the reason
		// a typo'd ping target produces an error instead of a device that
		// reboots every minute until someone drives to it. The proto builder
		// forces the flag regardless of this value; it is kept in the payload so
		// the recorded command shows what was actually sent.
		"verify_before_arm": true,
	}

	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs:     []uuid.UUID{nodeID},
		Command:     node_command.SlugApplyWatchdogConfig,
		Params:      params,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return nil, err
	}
	item := batch.Results[0]
	if !item.Success && !item.Queued {
		return nil, item.FailureError()
	}

	// PERSIST AFTER the dispatch is accepted, for the reason spelled out in
	// led.go: writing first meant a refusal changed the stored desired state
	// anyway, leaving the panel showing a configuration the fleet can never
	// converge to — and on a WATCHDOG that is a device the operator believes is
	// supervised and is not.
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID,
		map[string]interface{}{"watchdog_config": &cfgRaw}); err != nil {
		s.logger.ErrorContext(ctx, "the watchdog config was dispatched but could not be stored; the device may apply a configuration this platform has not recorded",
			"node_id", nodeID, "error", err)
		return nil, err
	}

	state := &node.WatchdogState{
		Desired:       &cfg,
		DesiredHash:   cfg.Hash(),
		AppliedHash:   n.WatchdogAppliedHash,
		DisarmReason:  n.WatchdogDisarmReason,
		LastRefusal:   n.WatchdogLastRefusal,
		PublicTargets: cfg.PublicTargets(),
		// Queued is the honest answer: an accepted-but-undelivered converge is
		// not an armed watchdog. item.Queued only knows about the connection,
		// but apply_watchdog_config is ALSO held for a node that is online in
		// the recovery bootstrap (RequiresManagedOS) — the dispatcher parks it
		// without marking it sent. Reporting "sent" there contradicted this
		// field's own contract and recorded a wrong queued=false in workflow
		// runs targeting netbooted nodes.
		Queued: item.Queued || (n.IsRecoveryBootstrap() && node_command.RequiresManagedOS(node_command.SlugApplyWatchdogConfig)),
	}
	state.InSync = state.DesiredHash == state.AppliedHash
	if item.CommandID != nil {
		status := node_command.StatusSent
		if item.Queued {
			status = node_command.StatusRegistered
		}
		state.LastCommand = &node.WatchdogCommandRef{ID: *item.CommandID, Status: status.String()}
	}
	return state, nil
}

// RecordWatchdogApplied stores the hash the device confirmed, so the UI can tell
// "asked for" from "running". A confirmed apply supersedes both a prior breaker
// disarm and a prior refusal, so it clears them.
func (s *Service) RecordWatchdogApplied(ctx context.Context, accountID, nodeID uuid.UUID, appliedHash string) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	// Named columns, not a full-row save. These three are the only ones this
	// report owns, and the row was read a moment ago — writing it whole would
	// carry back whatever the gRPC layer has changed since, including the
	// `restarting` pin.
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"watchdog_applied_hash":  appliedHash,
		"watchdog_disarm_reason": "",
		"watchdog_last_refusal":  "",
	}); err != nil {
		return err
	}
	// The in-memory copy is what the websocket event carries, so it has to be
	// updated too — the write above no longer does it as a side effect.
	n.WatchdogAppliedHash = appliedHash
	n.WatchdogDisarmReason = ""
	n.WatchdogLastRefusal = ""
	s.notifyNodeUpdated(*n)
	return nil
}

// RecordWatchdogDisarmed notes that the agent's local breaker took the watchdog
// down. Desired state is deliberately left alone: the operator has to see that
// what they asked for is not what is running.
func (s *Service) RecordWatchdogDisarmed(ctx context.Context, accountID, nodeID uuid.UUID, reason string) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"watchdog_disarm_reason": reason,
		"watchdog_applied_hash":  "",
	}); err != nil {
		return err
	}
	n.WatchdogDisarmReason = reason
	n.WatchdogAppliedHash = ""
	s.notifyNodeUpdated(*n)
	return nil
}

// RecordWatchdogRefused stores the agent's pre-arm verification refusal —
// "<code>: <details>", or just the code when details are empty. Desired state
// is kept: the operator must see that what they asked for was declined.
func (s *Service) RecordWatchdogRefused(ctx context.Context, accountID, nodeID uuid.UUID, refusal, details string) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	message := refusal
	if details != "" {
		message = fmt.Sprintf("%s: %s", refusal, details)
	}
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"watchdog_last_refusal": message,
	}); err != nil {
		return err
	}
	n.WatchdogLastRefusal = message
	s.notifyNodeUpdated(*n)
	return nil
}

// ScanWatchdogStatus dispatches the fixed live-status probe and returns the
// command id, polled via GetWatchdogStatusScanResult. It answers a question the
// stored state cannot: whether the daemon is running on the device right now.
// Online-only; run_script has no agent version floor, so this works on nodes
// whose agent is too old to arm a watchdog at all — which is exactly when
// "why is nothing supervising this node" gets asked.
func (s *Service) ScanWatchdogStatus(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, error) {
	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs: []uuid.UUID{nodeID},
		Command: node_command.SlugRunScript,
		Params: map[string]interface{}{
			"script":          node.WatchdogStatusScanScript,
			"timeout_seconds": float64(15),
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return uuid.Nil, err
	}
	item := batch.Results[0]
	if !item.Success {
		return uuid.Nil, item.FailureError()
	}
	if item.CommandID == nil {
		return uuid.Nil, fmt.Errorf("the node is offline, so the watchdog status probe cannot run now; bring it online and try again")
	}
	return *item.CommandID, nil
}

// GetWatchdogStatusScanResult reads a dispatched probe and parses it
// server-side. The command is verified to belong to the node and to be the exact
// fixed probe script, so an arbitrary command id cannot be used to read another
// run_script's stdout.
func (s *Service) GetWatchdogStatusScanResult(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (*node.WatchdogStatusScanResult, error) {
	if s.nodeCommandService == nil {
		return nil, fmt.Errorf("node command service not available")
	}
	cmd, err := s.nodeCommandService.GetNodeCommandByID(ctx, accountID, commandID)
	if err != nil {
		return nil, err
	}
	if cmd.NodeID != nodeID || cmd.Slug != node_command.SlugRunScript ||
		scriptOf(cmd) != node.WatchdogStatusScanScript {
		return nil, app_errors.ErrNodeNotFound
	}
	switch cmd.Status {
	case node_command.StatusSuccess:
		res := node.ParseWatchdogStatus(cmd.Result["stdout"])
		return &res, nil
	case node_command.StatusFailed, node_command.StatusExpired, node_command.StatusSkipped, node_command.StatusSuperseded:
		detail := cmd.Details
		if detail == "" {
			detail = "the probe did not complete"
		}
		return &node.WatchdogStatusScanResult{Status: "failed", Detail: detail}, nil
	default:
		return &node.WatchdogStatusScanResult{Status: "running"}, nil
	}
}

// buildApplyWatchdogPayload copies the watchdog fields sendCommandToNode may
// dispatch out of the caller's params. It is a named function rather than an
// inline switch arm because the arm's absence is invisible otherwise: while
// the version floor refuses every apply_watchdog_config before it can reach
// the switch, nothing — no test, no run — can observe that the case does not
// exist, and PUT /watchdog fails with "unknown command" the first time an
// agent is new enough to be sent one.
//
// Reached only through SetWatchdog, which has already validated the config
// against the domain bounds; the generic /command endpoint refuses this slug at
// the authz map. Numbers are matched as float64 only, which is the JSON shape
// SetWatchdog deliberately casts to — an int-typed param is silently dropped.
func buildApplyWatchdogPayload(params, payload map[string]interface{}) {
	if v, ok := params["config_hash"].(string); ok {
		payload["config_hash"] = v
	}
	for _, k := range []string{"enabled", "os_hang", "network", "agent_health"} {
		if v, ok := params[k].(bool); ok {
			payload[k] = v
		}
	}
	for _, k := range []string{"watchdog_timeout_sec", "ping_count", "agent_change_sec",
		"max_load_1", "max_load_5", "max_load_15", "min_memory_pages",
		"max_temperature_c", "interval_sec", "retry_timeout_sec"} {
		if v, ok := params[k].(float64); ok {
			payload[k] = v
		}
	}
	if v, ok := params["ping_targets"].([]interface{}); ok {
		payload["ping_targets"] = v
	}
	if v, ok := params["interface"].(string); ok && strings.TrimSpace(v) != "" {
		payload["interface"] = strings.TrimSpace(v)
	}
	// The on-device pre-arm checks are not optional, whatever the caller sent.
	payload["verify_before_arm"] = true
}

func toInterfaceSlice(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}
