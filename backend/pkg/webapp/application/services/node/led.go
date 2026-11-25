package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// ledCommandScanLimit bounds the recent-command page GetLED scans for the
// newest converge command; LED converges are rare, a small page suffices.
const ledCommandScanLimit = 20

// ledCapabilitiesFor resolves what the node's effective device profile makes
// possible. A node with no profile (or a profile without the wiring) simply
// has no stealth circuit and no digital chain — the sysfs knobs still work.
// A resolver ERROR is returned, not swallowed: empty capabilities on a
// transient read failure render as "this board has none", which reads as a
// product answer, and SetLED would gate — and build the
// dispatch params — against facts it never had.
func (s *Service) ledCapabilitiesFor(ctx context.Context, accountID, nodeID uuid.UUID) (node.LEDCapabilities, device_profile.IdentifySpec, error) {
	caps := node.LEDCapabilities{}
	spec := device_profile.IdentifySpec{}
	if s.deviceProfileResolver == nil || s.deviceProfiles == nil {
		return caps, spec, nil
	}
	eq, _, assigned, err := s.deviceProfileResolver.ResolveEffectiveDeviceProfile(ctx, accountID, nodeID)
	if err != nil {
		return caps, spec, fmt.Errorf("could not resolve the node's device profile: %w", err)
	}
	if !assigned {
		return caps, spec, nil
	}
	board, ok := s.deviceProfiles.Board(eq.ProfileID)
	if !ok {
		return caps, spec, nil
	}
	spec = board.Identify
	caps.StealthAvailable = spec.StealthGPIO > 0
	caps.DigitalLEDs = spec.DigitalLEDs
	return caps, spec, nil
}

// GetLED returns the node's LED state.
func (s *Service) GetLED(ctx context.Context, accountID, nodeID uuid.UUID) (*node.LEDState, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	state := &node.LEDState{
		Desired:     n.LEDConfig,
		AppliedHash: n.LEDAppliedHash,
	}
	if n.LEDConfig != nil {
		state.DesiredHash = n.LEDConfig.Hash()
		state.DesiredSource = string(organizational_unit.SourceNode)
	} else if s.deviceProfileResolver != nil {
		// No per-node config: the OU chain may declare one (Organization page).
		// Shown with its source; applying it stays an explicit per-node action.
		if cfg, source, ok, err := s.deviceProfileResolver.ResolveEffectiveLEDConfig(ctx, accountID, nodeID); err == nil && ok {
			inherited := cfg
			state.Desired = &inherited
			state.DesiredHash = inherited.Hash()
			state.DesiredSource = string(source)
		}
	}
	state.InSync = state.DesiredHash != "" && state.DesiredHash == state.AppliedHash
	caps, _, capErr := s.ledCapabilitiesFor(ctx, accountID, nodeID)
	caps.SupportedSources = n.SupportedLEDSources()
	state.Capabilities = caps
	if capErr != nil {
		// Read path: still answer, but say the capabilities are UNKNOWN so the
		// panel renders "could not resolve" instead of "board has none".
		state.CapabilitiesUnknown = true
		s.logger.WarnContext(ctx, "led capabilities unresolved", "node_id", nodeID, "error", capErr)
	}
	s.projectLEDCommand(ctx, accountID, nodeID, state)
	return state, nil
}

// projectLEDCommand fills LastCommand and Queued from the newest
// apply_led_config command. Best-effort, like the watchdog projection.
func (s *Service) projectLEDCommand(ctx context.Context, accountID, nodeID uuid.UUID, state *node.LEDState) {
	if s.nodeCommandService == nil {
		return
	}
	cmds, err := s.nodeCommandService.GetNodeCommandsByNodeID(ctx, accountID, nodeID, ledCommandScanLimit, 0)
	if err != nil {
		s.logger.Warn("failed to project the led converge command", "node_id", nodeID, "error", err)
		return
	}
	for i := range cmds { // newest first
		if cmds[i].Slug != node_command.SlugApplyLEDConfig {
			continue
		}
		state.LastCommand = &node.LEDCommandRef{
			ID:      cmds[i].ID,
			Status:  cmds[i].Status.String(),
			Details: cmds[i].Details,
		}
		state.Queued = cmds[i].Status == node_command.StatusRegistered
		return
	}
}

// SetLED validates a desired LED configuration against the node's board,
// stores it, and dispatches the converge command.
func (s *Service) SetLED(
	ctx context.Context,
	accountID, userID, nodeID uuid.UUID,
	cfg node.LEDConfig,
) (*node.LEDState, error) {
	return s.convergeLED(ctx, accountID, userID, nodeID, cfg, true)
}

// ResetLED removes the node's own LED configuration, so the node follows its
// unit chain (or the defaults) again, and dispatches the converge to whatever
// that now resolves to.
//
// The override is cleared FIRST, unlike SetLED's persist-after-dispatch: the
// inherited configuration can only be resolved once the node's own is gone,
// and if the dispatch is then refused the stored state is already the one the
// operator asked for — the panel shows the device out of sync, which a
// re-apply fixes. Storing an empty configuration instead would pin an
// all-off override that hides the unit's policy for good.
func (s *Service) ResetLED(ctx context.Context, accountID, userID, nodeID uuid.UUID) (*node.LEDState, error) {
	if _, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err != nil {
		return nil, err
	}
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID,
		map[string]interface{}{"led_config": nil}); err != nil {
		return nil, err
	}
	cfg := node.LEDConfig{}
	if s.deviceProfileResolver != nil {
		inherited, _, ok, err := s.deviceProfileResolver.ResolveEffectiveLEDConfig(ctx, accountID, nodeID)
		if err != nil {
			return nil, err
		}
		if ok {
			cfg = inherited
		}
	}
	if _, err := s.convergeLED(ctx, accountID, userID, nodeID, cfg, false); err != nil {
		return nil, err
	}
	return s.GetLED(ctx, accountID, nodeID)
}

// convergeLED gates a configuration against the node's board and agent and
// dispatches it; with persist, it then stores it as the node's own.
func (s *Service) convergeLED(
	ctx context.Context,
	accountID, userID, nodeID uuid.UUID,
	cfg node.LEDConfig,
	persist bool,
) (*node.LEDState, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}

	caps, spec, err := s.ledCapabilitiesFor(ctx, accountID, nodeID)
	if err != nil {
		// Write path: refuse. Gating against empty capabilities would answer
		// the misleading "this board cannot" for a transient read failure, and
		// the dispatch params below would carry GPIO 0 in place of the wiring.
		return nil, err
	}
	caps.SupportedSources = n.SupportedLEDSources()
	// Agent gate (LED v2): a source the RUNNING agent has not advertised would
	// render dark forever while the panel reads configured — refuse with the
	// remedy instead. Agents predating the advertisement count as the v1 pair.
	supported := map[string]bool{node.LEDSourceOff: true}
	for _, src := range caps.SupportedSources {
		supported[src] = true
	}
	for _, src := range cfg.EffectiveSources() {
		if src != "" && !supported[src] {
			return nil, fmt.Errorf("%w: this node's agent cannot render %q (it advertises %s) — update the agent", node.ErrLEDNotSupported, src, strings.Join(caps.SupportedSources, ", "))
		}
	}
	// Board gates: the wiring facts come from the device profile, so a config
	// the board cannot honor is refused at save — a dispatched command that
	// the agent must fail helps nobody.
	if cfg.StealthMode && !caps.StealthAvailable {
		return nil, fmt.Errorf("%w: stealth mode needs a board with a stealth circuit (assign the Compute Blade device profile first)", node.ErrLEDNotSupported)
	}
	if len(cfg.EffectiveSources()) > 0 {
		if spec.Backend != "neopixel" || spec.GPIO <= 0 {
			return nil, fmt.Errorf("%w: digital LED indication needs a board with digital LEDs (assign the Compute Blade device profile first)", node.ErrLEDNotSupported)
		}
		if caps.DigitalLEDs > 0 && len(cfg.EffectiveSources()) > caps.DigitalLEDs {
			return nil, fmt.Errorf("%w: the configuration assigns more digital LEDs than this board has (%d)", node.ErrLEDNotSupported, caps.DigitalLEDs)
		}
	}

	n.LEDConfig = &cfg
	cfgJSON, err := json.Marshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode led config: %w", err)
	}
	cfgRaw := string(cfgJSON)

	params := map[string]interface{}{
		"config_hash": cfg.Hash(),
		"stealth_mode": cfg.StealthMode,
		"act_led_off":  cfg.ActLEDOff,
		"pwr_led_off":  cfg.PwrLEDOff,
		// The v1 projection ALWAYS rides, whichever form the config carries:
		// an older agent ignores digital_leds and renders these sources with
		// its built-in defaults — degraded thresholds, never dark LEDs.
		"digital_sources": toInterfaceSlice(cfg.EffectiveSources()),
		"digital_gpio":    float64(spec.GPIO),
		"stealth_gpio":    float64(spec.StealthGPIO),
	}
	if len(cfg.DigitalLEDs) > 0 {
		specs := make([]interface{}, 0, len(cfg.DigitalLEDs))
		for _, d := range cfg.DigitalLEDs {
			specs = append(specs, map[string]interface{}{
				"source":     d.Source,
				"low":        d.Low,
				"high":       d.High,
				"brightness": float64(d.Brightness),
			})
		}
		params["digital_leds"] = specs
	}

	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs:     []uuid.UUID{nodeID},
		Command:     node_command.SlugApplyLEDConfig,
		Params:      params,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return nil, err
	}
	item := batch.Results[0]
	if !item.Success && !item.Queued {
		if item.Skipped {
			return nil, s.agentTooOld(ctx, accountID, nodeID, node_command.SlugApplyLEDConfig)
		}
		return nil, item.FailureError()
	}

	// PERSIST AFTER the dispatch is accepted, never before.
	//
	// The write used to happen first, so a refusal — an agent below the slug's
	// floor is the common one — answered the caller an error while the stored
	// desired config had already changed. The panel then showed a configuration
	// the fleet can never converge to, and nothing would ever dispatch it again
	// because the desired state already matched what the operator asked for.
	//
	// The opposite failure is the better one to have: if this write fails the
	// device has a config the platform did not record, the settle reports an
	// applied hash matching no desired one, and the panel shows drift — visible,
	// and fixed by re-applying. So the error is returned, and the log says the
	// device may already have it.
	//
	// Named column only: a full-row save would carry back everything else from a
	// read taken moments ago, undoing any concurrent update.
	if !persist {
		return nil, nil
	}
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID,
		map[string]interface{}{"led_config": &cfgRaw}); err != nil {
		s.logger.ErrorContext(ctx, "the LED config was dispatched but could not be stored; the device may apply a configuration this platform has not recorded",
			"node_id", nodeID, "error", err)
		return nil, err
	}

	state := &node.LEDState{
		Desired:      &cfg,
		DesiredHash:  cfg.Hash(),
		AppliedHash:  n.LEDAppliedHash,
		Capabilities: caps,
		// Held-for-managed-OS counts as queued, same as the watchdog: an
		// accepted-but-undelivered converge is not an applied one.
		Queued: item.Queued || (n.IsRecoveryBootstrap() && node_command.RequiresManagedOS(node_command.SlugApplyLEDConfig)),
	}
	state.InSync = state.DesiredHash == state.AppliedHash
	if item.CommandID != nil {
		status := node_command.StatusSent
		if item.Queued {
			status = node_command.StatusRegistered
		}
		state.LastCommand = &node.LEDCommandRef{ID: *item.CommandID, Status: status.String()}
	}
	return state, nil
}

// RecordLEDApplied stores the hash the settle confirmed, so the UI can tell
// "asked for" from "running".
func (s *Service) RecordLEDApplied(ctx context.Context, accountID, nodeID uuid.UUID, appliedHash string) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"led_applied_hash": appliedHash,
	}); err != nil {
		return err
	}
	n.LEDAppliedHash = appliedHash
	s.notifyNodeUpdated(*n)
	return nil
}

// buildApplyLEDPayload copies the LED params into the dispatch payload. A
// separate function (the watchdog pattern) so a pin test can assert every key
// SetLED sends survives the copy — this switch copies only the keys it knows,
// and a key added at the dispatch site alone is silently dropped.
func buildApplyLEDPayload(params, payload map[string]interface{}) error {
	configHash, _ := params["config_hash"].(string)
	if configHash == "" {
		return fmt.Errorf("apply_led_config requires config_hash")
	}
	payload["config_hash"] = configHash
	payload["stealth_mode"], _ = params["stealth_mode"].(bool)
	payload["act_led_off"], _ = params["act_led_off"].(bool)
	payload["pwr_led_off"], _ = params["pwr_led_off"].(bool)
	if sources, ok := params["digital_sources"].([]interface{}); ok {
		payload["digital_sources"] = sources
	}
	if v, ok := params["digital_gpio"].(float64); ok {
		payload["digital_gpio"] = v
	}
	if v, ok := params["stealth_gpio"].(float64); ok {
		payload["stealth_gpio"] = v
	}
	if specs, ok := params["digital_leds"].([]interface{}); ok {
		payload["digital_leds"] = specs
	}
	return nil
}
