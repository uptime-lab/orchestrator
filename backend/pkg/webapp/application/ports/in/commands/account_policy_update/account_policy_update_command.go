package account_policy_update

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// AccountPolicyUpdateCommand updates the account-wide policy fallback. Field
// semantics mirror the OU update command: nil = unchanged, "" (or the
// "inherit" flag) = clear back to unset, value = set. SSHKeyID/OSImageID are
// UUID strings.
type AccountPolicyUpdateCommand struct {
	NetworkFirst    *organizational_unit.PolicyFlag `json:"network_first,omitempty"`
	BootToOS        *organizational_unit.PolicyFlag `json:"boot_to_os,omitempty"`
	SSHPasswordAuth *organizational_unit.PolicyFlag `json:"ssh_password_auth,omitempty"`
	DefaultUsername *string                         `json:"default_username,omitempty"`
	DefaultPassword *string                         `json:"default_password,omitempty"`
	SSHKeyID        *string                         `json:"ssh_key_id,omitempty"`
	OSImageID       *string                         `json:"os_image_id,omitempty"`
	// DeviceProfile: nil = unchanged; a zero equipage (empty profile_id) =
	// clear back to unset; a populated one = set as the account-wide fallback.
	// Validated against the registry catalog in the service before storing.
	DeviceProfile *device_profile.Equipage `json:"device_profile,omitempty"`
	// LEDConfig: nil = unchanged; a zero config = clear back to none.
	LEDConfig *node.LEDConfig `json:"led_config,omitempty"`
	// WatchdogConfig: nil = unchanged; a DISABLED config = clear back to none
	// (an account declaration is always an armed supervision — disarm is the
	// per-node action). The save dispatches nothing.
	WatchdogConfig *node.WatchdogConfig `json:"watchdog_config,omitempty"`
	AuthContext    commands.AuthContext `json:"-"`
}
