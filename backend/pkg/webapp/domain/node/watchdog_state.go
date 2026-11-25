package node

import "github.com/google/uuid"

// WatchdogState is what the UI needs to show: what was asked for, what the
// device confirmed, whether the request is still waiting for the node, and
// whether the device refused or disarmed on its own.
type WatchdogState struct {
	Desired     *WatchdogConfig `json:"desired,omitempty"`
	DesiredHash string          `json:"desired_hash,omitempty"`
	// DesiredSource names the level the desired config came from
	// ("node"|"ou"|"root_ou"|"account") — the LED pattern. An inherited config
	// is shown with its source; applying it stays an explicit per-node action.
	DesiredSource string `json:"desired_source,omitempty"`
	AppliedHash   string `json:"applied_hash,omitempty"`
	InSync        bool   `json:"in_sync"`
	DisarmReason  string `json:"disarm_reason,omitempty"`
	// LastRefusal is the agent's pre-arm verification declining a config
	// ("<code>: <details>"). Cleared on the next confirmed apply.
	LastRefusal   string   `json:"last_refusal,omitempty"`
	PublicTargets []string `json:"public_ping_targets,omitempty"`
	// Queued means the converge command is accepted but NOT applied — the node
	// is offline (or netbooted) and will receive it on reconnect. A dispatched
	// command is not an armed watchdog.
	Queued bool `json:"queued"`
	// LastCommand is the newest apply_watchdog_config command, so the UI can
	// show in-flight/failed converges without polling the command list itself.
	LastCommand     *WatchdogCommandRef `json:"last_command,omitempty"`
	RecoveryLimited bool                `json:"recovery_limited,omitempty"`
}

// WatchdogCommandRef is the projection of the converge command the state
// carries: enough to poll or explain it, nothing more.
type WatchdogCommandRef struct {
	ID      uuid.UUID `json:"id"`
	Status  string    `json:"status"`
	Details string    `json:"details,omitempty"`
}

// WatchdogRecoveryLimited reports whether arming the watchdog on this node
// would reboot with nothing behind it. Recovery is only assured when the
// boot-loop failsafe is armed (a positive effective threshold — the per-node
// override when set, else the install's global default) AND the node netboots
// first, so a reboot loop lands back on the platform. This is a FLAG for the
// UI, never a refusal, because an install may legitimately run with the
// failsafe off and a hard refuse would remove the feature there entirely.
//
// (The threshold is an install setting, not a code default.
// An install that sets nothing runs with 0, which is what a copied example
// configuration gives; an install that wants the failsafe sets a small count.)
func WatchdogRecoveryLimited(enabled bool, effectiveThreshold int, networkFirst bool) bool {
	if !enabled {
		return false
	}
	return effectiveThreshold <= 0 || !networkFirst
}
