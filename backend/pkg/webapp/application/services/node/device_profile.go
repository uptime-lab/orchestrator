package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// DeviceProfileResolver walks node -> OU -> root -> account for the effective
// equipage. A narrow local interface wired by a setter (the OU service
// implements it), the ExecutionAbandoner pattern.
type DeviceProfileResolver interface {
	ResolveEffectiveDeviceProfile(ctx context.Context, accountID, nodeID uuid.UUID) (device_profile.Equipage, organizational_unit.PolicySource, bool, error)
	// ResolveEffectiveLEDConfig is the LED twin of the equipage walk: the first
	// level declaring an LED configuration wins as ONE block.
	ResolveEffectiveLEDConfig(ctx context.Context, accountID, nodeID uuid.UUID) (node.LEDConfig, organizational_unit.PolicySource, bool, error)
	// ResolveEffectiveWatchdogConfig is the watchdog twin. It answers what is
	// DESIRED only — nothing arms a device without a human present, so the
	// per-node apply stays the sole path to hardware.
	ResolveEffectiveWatchdogConfig(ctx context.Context, accountID, nodeID uuid.UUID) (node.WatchdogConfig, organizational_unit.PolicySource, bool, error)
}

// SetDeviceProfileCatalog wires the loaded hardware registry.
func (s *Service) SetDeviceProfileCatalog(c *device_profile.Catalog) { s.deviceProfiles = c }

// DeviceProfileCatalog returns the loaded registry (nil when not wired).
func (s *Service) DeviceProfileCatalog() *device_profile.Catalog { return s.deviceProfiles }

// SetDeviceProfileResolver wires the inheritance walk.
func (s *Service) SetDeviceProfileResolver(r DeviceProfileResolver) { s.deviceProfileResolver = r }

// identifyDefaultDurationSec / identifyMaxDurationSec bound the locate blink.
// The agent clamps again on its side; the platform bound exists so the stored
// command shows what was really asked.
const (
	// identifyFallbackDurationSec applies when no account setting is readable.
	identifyFallbackDurationSec = 300
	identifyMaxDurationSec      = 3600
)

// IdentifyDurationSource answers the account's configured blink duration
// (accounts.identify_duration_sec; wired from main.go over the account repo).
type IdentifyDurationSource func(ctx context.Context, accountID uuid.UUID) int

// SetIdentifyDurationSource wires the account-setting read.
func (s *Service) SetIdentifyDurationSource(src IdentifyDurationSource) { s.identifyDuration = src }

// identifyDurationFor resolves the effective default duration, fail-soft.
func (s *Service) identifyDurationFor(ctx context.Context, accountID uuid.UUID) int {
	if s.identifyDuration != nil {
		if v := s.identifyDuration(ctx, accountID); v >= 10 && v <= identifyMaxDurationSec {
			return v
		}
	}
	return identifyFallbackDurationSec
}

// buttonDurationHistoryDepth bounds the history scan behind a button press.
// The device-profile apply is not a frequent command; if the last one has
// scrolled past this, the fallback below is the same answer the platform gave
// before it looked at all.
const buttonDurationHistoryDepth = 100

// appliedIdentifyDuration answers "how long will this device actually blink".
//
// The device blinks for the identify_duration_sec it was given at its last
// device-profile apply — that value is baked into what the agent persisted —
// while the account setting is what an operator may have changed since. The
// press event used the CURRENT setting, so after any change the browser lamp
// and the physical LED disagreed: an early fade, or a lamp still lit over a
// dark node.
//
// The applied value is read back from the dispatch PAYLOAD of the last
// successful apply_device_profile, which the platform already retains. No new
// column: a second copy of a value that is already stored is a second thing to
// keep in step, and the accessReapplyNeeded precedent reads history for exactly
// this kind of question.
//
// Falls back to the account setting for a node that has never had a profile
// applied, or whose last apply predates the field — for those the setting IS
// what the device will use, because the next apply is what will put it there.
func (s *Service) appliedIdentifyDuration(ctx context.Context, accountID, nodeID uuid.UUID) int {
	fallback := s.identifyDurationFor(ctx, accountID)
	if s.nodeCommandService == nil {
		return fallback
	}
	recent, err := s.nodeCommandService.GetNodeCommandsByNodeID(ctx, accountID, nodeID, buttonDurationHistoryDepth, 0)
	if err != nil {
		s.logger.WarnContext(ctx, "could not read command history for the applied identify duration; using the account setting",
			"node_id", nodeID, "error", err)
		return fallback
	}
	for i := range recent {
		cmd := &recent[i]
		if cmd.Slug != node_command.SlugApplyDeviceProfile || cmd.Status != node_command.StatusSuccess {
			continue
		}
		// jsonb round-trips a number as float64; an older payload has no key at
		// all, and 0 is not a duration — both fall through to the setting.
		if v, ok := cmd.Payload["identify_duration_sec"].(float64); ok && v > 0 {
			return int(v)
		}
		break // the newest successful apply is the one the device is running
	}
	return fallback
}

// NotifyButtonPressed fans a physical button press out to the account's
// connected browsers. No database write — the press is a transient locate
// signal, not a record — and OU-scoped like every other node event.
func (s *Service) NotifyButtonPressed(ctx context.Context, accountID, nodeID uuid.UUID, pressKind string) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	// Registry first, fan-out second: the press outcome must be findable by a
	// browser opened after the fact, not only by the ones watching now. The
	// duration rides the event so every lamp blinks exactly as long as the
	// device does — which is why it must be the duration THE DEVICE HAS, not
	// the one the account setting says today.
	durationSec := s.appliedIdentifyDuration(ctx, accountID, nodeID)
	blinkUntil, markedAt := s.recordButtonOutcome(accountID, nodeID, pressKind, durationSec)
	if s.websocketClient == nil {
		return nil
	}
	payload := map[string]string{
		"node_id":    nodeID.String(),
		"node_name":  n.Name,
		"press_kind": pressKind,
	}
	if blinkUntil != nil {
		payload["blink_until"] = blinkUntil.UTC().Format(time.RFC3339)
		// The duration travels as itself as well as as an end time. A
		// subtraction against the receiving browser's clock answers 299 for a
		// 300-second setting — network latency alone, before any clock skew —
		// and "blinking for 299 seconds" is how the same setting the identify
		// button calls five minutes reaches the operator as a different fact.
		payload["duration_sec"] = strconv.Itoa(durationSec)
	}
	if markedAt != nil {
		// The registry's own timestamp, so the browser stores THIS value as its
		// mark rather than its local clock. Hydration then reports the same
		// number, which is what lets a dismissal be keyed by value: the ID
		// button's off-click dispatches nothing when no blink is running, so
		// the registry keeps the mark for its TTL and would otherwise hydrate it
		// straight back on the next reconnect.
		payload["pressed_at"] = markedAt.UTC().Format(time.RFC3339)
	}
	return s.websocketClient.Send(websockets3.NewAccountMessageForOU(accountID, n.OUID, websockets3.MessageWrapper{
		Type:    "node_button_pressed",
		Payload: payload,
	}))
}

// agentTooOld builds the typed floor-refusal with the versions FILLED — the
// operator's next step is update_agent, and "an unknown version ... requires
// or newer" (both fields empty) points them nowhere.
func (s *Service) agentTooOld(ctx context.Context, accountID, nodeID uuid.UUID, slug string) *AgentTooOldError {
	e := &AgentTooOldError{NodeID: nodeID, Slug: slug}
	e.Require, _ = node_command.MinAgentVersion(slug)
	if n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err == nil {
		e.Have = n.AgentVersion
	}
	return e
}

func (s *Service) deviceProfileDeps() error {
	if s.deviceProfiles == nil || s.deviceProfileResolver == nil {
		return fmt.Errorf("device profile registry is not available")
	}
	return nil
}

// boardKindOf returns the node's detected board kind string, revision-first
// with the model string as fallback (the recovery bootstrap's forced
// device_tree= makes the model string lie for BCM2712 boards).
func boardKindOf(n *node.Node) string {
	return n.BoardLabel()
}

// GetDeviceProfileState resolves the node's effective equipage and its ladder.
func (s *Service) GetDeviceProfileState(ctx context.Context, accountID, nodeID uuid.UUID) (*node.DeviceProfileState, error) {
	if err := s.deviceProfileDeps(); err != nil {
		return nil, err
	}
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	eq, source, assigned, err := s.deviceProfileResolver.ResolveEffectiveDeviceProfile(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	state := &node.DeviceProfileState{
		Assigned:  assigned,
		Equipage:  eq,
		Source:    string(source),
		BoardKind: boardKindOf(n),
	}
	for _, b := range s.deviceProfiles.Boards {
		if b.FitsBoard(state.BoardKind) {
			state.CompatibleProfiles = append(state.CompatibleProfiles, b.ID)
		}
	}
	if assigned {
		rendered := device_profile.Render(s.deviceProfiles, eq)
		state.DesiredHash = rendered.Hash
		state.AppliedHash = n.DeviceProfileAppliedHash
		state.Scan = n.DeviceProfileScan
		state.Options = device_profile.ResolveOptions(s.deviceProfiles, eq, n.DeviceProfileAppliedHash, n.DeviceProfileScan)
		state.Capabilities = device_profile.Capabilities(s.deviceProfiles, eq)
		// Only meaningful while the applied config is the current one: with an
		// edit outstanding the answer is "apply first", and reporting a stale
		// apply's reboot as pending would send the operator to reboot into a
		// config that is about to be replaced.
		if state.AppliedHash == state.DesiredHash {
			state.RebootPending = n.DeviceProfileRebootPending()
		}
	}
	return state, nil
}

// SetDeviceProfile stores (or clears, for a nil/zero equipage) the node's OWN
// declared equipage. Inherited declarations live on the OU/account and are
// edited there; this column is the nearest level of the resolution.
func (s *Service) SetDeviceProfile(ctx context.Context, accountID, userID, nodeID uuid.UUID, eq *device_profile.Equipage) (*node.DeviceProfileState, error) {
	if err := s.deviceProfileDeps(); err != nil {
		return nil, err
	}
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}

	detail := "cleared"
	if eq == nil || eq.IsZero() {
		if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID,
			map[string]interface{}{"device_profile": nil}); err != nil {
			return nil, err
		}
		n.DeviceProfile = nil
	} else {
		if err := eq.Validate(s.deviceProfiles); err != nil {
			return nil, err
		}
		// The fits check runs at ASSIGNMENT, against the detected board. It is
		// advisory-strict: an unknown board (old agent, no revision reported)
		// passes — declaration-first means the operator may know better — but a
		// KNOWN board the profile excludes is refused, because that mismatch is
		// never intentional.
		board, _ := s.deviceProfiles.Board(eq.ProfileID)
		if kind := boardKindOf(n); !board.FitsBoard(kind) {
			return nil, fmt.Errorf("%w: %s does not accept a %s",
				device_profile.ErrProfileDoesNotFit, board.Name, kind)
		}
		raw, err := json.Marshal(eq)
		if err != nil {
			return nil, fmt.Errorf("failed to encode equipage: %w", err)
		}
		rawStr := string(raw)
		if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID,
			map[string]interface{}{"device_profile": &rawStr}); err != nil {
			return nil, err
		}
		copied := *eq
		n.DeviceProfile = &copied
		detail = fmt.Sprintf("profile=%s variant=%s modules=%d", eq.ProfileID, eq.VariantID, len(eq.Modules))
	}

	s.audit.RecordDetail(ctx, audit.UserActor(accountID, userID),
		audit_event.ActionNodeDeviceProfileAssign, audit_event.TargetNode, nodeID.String(), detail)
	s.notifyNodeUpdated(*n)
	return s.GetDeviceProfileState(ctx, accountID, nodeID)
}

// PreviewDeviceProfile renders what an apply would write — the confirm
// dialog's content, produced by the same code path the apply will use.
func (s *Service) PreviewDeviceProfile(ctx context.Context, accountID, nodeID uuid.UUID) (*node.DeviceProfilePreview, error) {
	if err := s.deviceProfileDeps(); err != nil {
		return nil, err
	}
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	eq, _, assigned, err := s.deviceProfileResolver.ResolveEffectiveDeviceProfile(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	if !assigned {
		// The comment-only block: what an apply after UN-assignment writes.
		eq = device_profile.Equipage{}
	}
	rendered := device_profile.Render(s.deviceProfiles, eq)
	kinds := make([]string, 0, len(rendered.Setup))
	for _, st := range rendered.Setup {
		kinds = append(kinds, st.Kind)
	}
	return &node.DeviceProfilePreview{
		Path:       device_profile.ConfigPath,
		Marker:     device_profile.Marker,
		Content:    rendered.Content,
		Hash:       rendered.Hash,
		SetupKinds: kinds,
		UpToDate:   n.DeviceProfileAppliedHash == rendered.Hash,
	}, nil
}

// ApplyDeviceProfile dispatches the apply as ONE queueable, managed-OS-only
// command. The rendered hash travels IN the payload: the settle hook records
// that value verbatim, so an equipage edited while the command sat queued can
// never be marked applied by the older dispatch.
// ApplyDeviceProfileToUnit runs the per-node apply over every node DIRECTLY
// assigned to the unit — the deliberate "apply to all nodes of this unit"
// button. Nothing here dispatches on an equipage EDIT: the OU access-config
// push is the cautionary precedent, and hardware setup steps are the last
// thing to run on a fleet as a side effect of a form save.
//
// Per-node failures do not abort the sweep: a mixed unit legitimately
// contains a board the profile refuses, a watcher node, or a node with
// nothing assigned and nothing applied — each lands in Skipped with the
// platform's own refusal wording, and the rest of the unit still gets its
// apply.
func (s *Service) ApplyDeviceProfileToUnit(ctx context.Context, accountID, userID, ouID uuid.UUID) (*services.UnitDeviceProfileApplyReport, error) {
	nodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	report := &services.UnitDeviceProfileApplyReport{Skipped: []services.UnitDeviceProfileSkip{}}
	for i := range nodes {
		n := &nodes[i]
		if n.OUID == nil || *n.OUID != ouID {
			continue
		}
		_, queued, err := s.ApplyDeviceProfile(ctx, accountID, userID, n.ID)
		switch {
		case err != nil:
			report.Skipped = append(report.Skipped, services.UnitDeviceProfileSkip{
				NodeID: n.ID.String(), Name: n.Name, Reason: err.Error(),
			})
		case queued:
			report.Queued++
		default:
			report.Dispatched++
		}
	}
	return report, nil
}

func (s *Service) ApplyDeviceProfile(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, bool, error) {
	if err := s.deviceProfileDeps(); err != nil {
		return uuid.Nil, false, err
	}
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return uuid.Nil, false, err
	}
	eq, _, assigned, err := s.deviceProfileResolver.ResolveEffectiveDeviceProfile(ctx, accountID, nodeID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if !assigned {
		// Applying "no profile" is meaningful exactly once: to empty a
		// previously applied block. Refuse when there is nothing applied
		// either, so the button cannot dispatch a no-op to the fleet.
		if n.DeviceProfileAppliedHash == "" {
			return uuid.Nil, false, app_errors.OperatorRefusal(fmt.Errorf("no device profile is assigned to this node"))
		}
		eq = device_profile.Equipage{}
	} else {
		// The fits refusal runs again HERE, against the resolved equipage,
		// because only node-level assignment could check it at save time — an
		// OU or account block legitimately spans mixed boards, so the mismatch
		// only becomes checkable per node, at the moment the overlays would be
		// written. Same semantics as assignment: an unknown board passes, a
		// KNOWN board the profile excludes is refused.
		if board, ok := s.deviceProfiles.Board(eq.ProfileID); ok {
			if kind := boardKindOf(n); !board.FitsBoard(kind) {
				return uuid.Nil, false, app_errors.OperatorRefusal(fmt.Errorf("%w: %s does not accept a %s",
					device_profile.ErrProfileDoesNotFit, board.Name, kind))
			}
		}
	}
	rendered := device_profile.Render(s.deviceProfiles, eq)
	kinds := make([]interface{}, 0, len(rendered.Setup))
	for _, st := range rendered.Setup {
		kinds = append(kinds, st.Kind)
	}
	// The identify-LED spec rides the apply so the agent can persist it next
	// to the button state: the button's local ack blink fires exactly when
	// the platform may be unreachable, so the agent cannot ask at press time.
	identify := device_profile.IdentifySpec{}
	if board, ok := s.deviceProfiles.Board(eq.ProfileID); ok {
		identify = board.Identify
	}
	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs: []uuid.UUID{nodeID},
		Command: node_command.SlugApplyDeviceProfile,
		Params: map[string]interface{}{
			"path":             device_profile.ConfigPath,
			"marker":           device_profile.Marker,
			"content":          rendered.Content,
			"content_hash":     rendered.Hash,
			"setup_kinds":      kinds,
			"button_gpio":      float64(rendered.ButtonGPIO),
			"identify_backend":      identify.Backend,
			"identify_gpio":         float64(identify.GPIO),
			"identify_color":        identify.Color,
			"identify_duration_sec": float64(s.identifyDurationFor(ctx, accountID)),
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	item := batch.Results[0]
	if !item.Success {
		if item.Skipped {
			return uuid.Nil, false, s.agentTooOld(ctx, accountID, nodeID, node_command.SlugApplyDeviceProfile)
		}
		// Dispatch refusal reasons are already client-visible in the batch API's
		// own `error` field, so the marker states a fact, not a new exposure.
		return uuid.Nil, false, app_errors.OperatorRefusal(item.FailureError())
	}
	if item.CommandID == nil {
		// Queued for an offline (or netbooted) node — delivered on the next
		// managed-OS connect. Legitimate for a declarative apply.
		return uuid.Nil, true, nil
	}
	return *item.CommandID, false, nil
}

// RecordDeviceProfileApplied stores the hash the dispatch payload carried,
// called by the gRPC settle hook on the command's terminal success.
func (s *Service) RecordDeviceProfileApplied(ctx context.Context, accountID, nodeID uuid.UUID, appliedHash string) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	// Named column, not a full-row save: a full-row save writes back every other
	// column from the read above and can undo a concurrent update. The
	// timestamp rides the SAME write as the hash: they answer one question
	// together ("what was applied, and does it still owe a reboot?"), and two
	// writes is how they drift apart.
	appliedAt := time.Now()
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"device_profile_applied_hash": appliedHash,
		"device_profile_applied_at":   appliedAt,
	}); err != nil {
		return err
	}
	n.DeviceProfileAppliedHash = appliedHash
	n.DeviceProfileAppliedAt = &appliedAt
	s.notifyNodeUpdated(*n)
	return nil
}

// ScanDeviceProfile dispatches the fixed hardware probe. Refused while the
// node runs the recovery bootstrap: the overlays were never in the boot config
// that booted it, so every probe would answer "absent" and poison the ladder
// with a false not-confirmed.
func (s *Service) ScanDeviceProfile(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return uuid.Nil, err
	}
	if n.IsRecoveryBootstrap() {
		return uuid.Nil, app_errors.OperatorRefusal(fmt.Errorf("the node is in netboot recovery, so a hardware scan would not describe the installed OS; boot it into its OS and try again"))
	}
	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs: []uuid.UUID{nodeID},
		Command: node_command.SlugRunScript,
		Params: map[string]interface{}{
			"script":          device_profile.ScanScript,
			"timeout_seconds": float64(15),
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return uuid.Nil, err
	}
	item := batch.Results[0]
	if !item.Success {
		return uuid.Nil, app_errors.OperatorRefusal(item.FailureError())
	}
	if item.CommandID == nil {
		return uuid.Nil, app_errors.OperatorRefusal(fmt.Errorf("the node is offline, so the hardware scan cannot run now; bring it online and try again"))
	}
	return *item.CommandID, nil
}

// GetDeviceProfileScanResult polls a dispatched scan. On completion the parsed
// result is PERSISTED into device_profile_scan — the poll is the single writer
// of that column — so the ladder's confirmed/not-confirmed survives the page.
func (s *Service) GetDeviceProfileScanResult(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (*node.DeviceProfileScanStatus, error) {
	if s.nodeCommandService == nil {
		return nil, fmt.Errorf("node command service not available")
	}
	cmd, err := s.nodeCommandService.GetNodeCommandByID(ctx, accountID, commandID)
	if err != nil {
		return nil, err
	}
	// Bind the poll to a command THIS endpoint dispatched: same node, and the
	// exact fixed scan script (the os-users scan precedent — without the script
	// equality an arbitrary prior run_script's stdout could be read here).
	if cmd.NodeID != nodeID || cmd.Slug != node_command.SlugRunScript ||
		scriptOf(cmd) != device_profile.ScanScript {
		return nil, app_errors.ErrNodeNotFound
	}
	switch cmd.Status {
	case node_command.StatusSuccess:
		// Timestamped from the command's terminal write, not the poll: terminal
		// commands are retained for 90 days, and stamping time.Now() would let a
		// re-poll of an OLD scan's command id overwrite a newer scan with stale
		// facts wearing a fresh date.
		parsed := device_profile.ParseScanOutput(cmd.Result["stdout"], cmd.UpdatedAt.UTC())
		persist := true
		if n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err == nil &&
			n.DeviceProfileScan != nil && n.DeviceProfileScan.At.After(parsed.At) {
			// A newer scan is already stored; this poll only reads.
			persist = false
		}
		if persist {
			raw, err := json.Marshal(&parsed)
			if err == nil {
				rawStr := string(raw)
				if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID,
					map[string]interface{}{"device_profile_scan": &rawStr}); err != nil {
					s.logger.WarnContext(ctx, "failed to persist device profile scan", "error", err, "node_id", nodeID)
				}
			}
		}
		return &node.DeviceProfileScanStatus{Status: "completed", Scan: &parsed}, nil
	case node_command.StatusFailed, node_command.StatusExpired, node_command.StatusSkipped, node_command.StatusSuperseded:
		detail := cmd.Details
		if detail == "" {
			detail = "the scan did not complete"
		}
		return &node.DeviceProfileScanStatus{Status: "failed", Detail: detail}, nil
	default:
		return &node.DeviceProfileScanStatus{Status: "running"}, nil
	}
}

// Identify blinks the node's locate LED — or, with stop, ends a running
// blink. Works on every node — the universal backend is the sysfs ACT LED —
// and renders prettier where the profile declares digital LEDs. Not
// queueable: a locate that fires hours after the operator left the rack is a
// surprise, so an offline node fails fast. A stop still carries a 5-second
// duration: an agent below the stop field's release ignores the flag and
// renders the legacy "shortest identify IS the stop" blink instead of a
// fresh 30-second one.
func (s *Service) Identify(ctx context.Context, accountID, userID, nodeID uuid.UUID, durationSec int, stop bool) (uuid.UUID, int, error) {
	if stop {
		durationSec = 5
	} else {
		if durationSec <= 0 {
			durationSec = s.identifyDurationFor(ctx, accountID)
		}
		if durationSec > identifyMaxDurationSec {
			durationSec = identifyMaxDurationSec
		}
	}
	spec := device_profile.IdentifySpec{Backend: "sys-led"}
	// The profile's identify spec is cosmetic routing, not a gate: a resolver
	// error or an unassigned node still identifies via the fallback.
	if s.deviceProfiles != nil && s.deviceProfileResolver != nil {
		if eq, _, ok, err := s.deviceProfileResolver.ResolveEffectiveDeviceProfile(ctx, accountID, nodeID); err == nil && ok {
			if board, found := s.deviceProfiles.Board(eq.ProfileID); found && board.Identify.Backend != "" {
				spec = board.Identify
			}
		}
	}
	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs: []uuid.UUID{nodeID},
		Command: node_command.SlugIdentify,
		Params: map[string]interface{}{
			"duration_sec": float64(durationSec),
			"backend":      spec.Backend,
			"gpio":         float64(spec.GPIO),
			"color":        spec.Color,
			"stop":         stop,
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return uuid.Nil, 0, err
	}
	item := batch.Results[0]
	if !item.Success {
		if item.Skipped {
			return uuid.Nil, 0, s.agentTooOld(ctx, accountID, nodeID, node_command.SlugIdentify)
		}
		return uuid.Nil, 0, app_errors.OperatorRefusal(item.FailureError())
	}
	if item.CommandID == nil {
		return uuid.Nil, 0, app_errors.OperatorRefusal(fmt.Errorf("the node is offline, so the identify blink cannot run"))
	}
	// The registry mirrors what was actually dispatched, so a browser opened
	// later can still see the blink (identify_state.go).
	if stop {
		s.recordIdentifyStop(accountID, nodeID)
	} else {
		s.recordIdentifyStart(accountID, nodeID, time.Duration(durationSec)*time.Second)
	}
	return *item.CommandID, durationSec, nil
}
