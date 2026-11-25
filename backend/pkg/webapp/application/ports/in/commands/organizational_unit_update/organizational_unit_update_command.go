package organizational_unit_update

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// OrganizationalUnitUpdateCommand updates a unit. Nil fields are left unchanged;
// a non-nil policy flag (inherit|on|off) sets that policy.
type OrganizationalUnitUpdateCommand struct {
	ID   uuid.UUID `json:"-"`
	Name *string   `json:"name,omitempty"`
	// ParentID reparents the unit: nil = unchanged, "" = make it a root unit, a
	// UUID string = nest under that (root) unit. The two-level limit is enforced.
	ParentID *string `json:"parent_id,omitempty"`
	// Position sets the sibling sort order (nil = unchanged).
	Position        *int                            `json:"position,omitempty"`
	NetworkFirst    *organizational_unit.PolicyFlag `json:"network_first,omitempty"`
	BootToOS        *organizational_unit.PolicyFlag `json:"boot_to_os,omitempty"`
	SSHPasswordAuth *organizational_unit.PolicyFlag `json:"ssh_password_auth,omitempty"`
	// Provisioning defaults: nil = unchanged, "" = clear to inherit, value = set.
	// SSHKeyID/OSImageID are UUID strings.
	DefaultUsername *string `json:"default_username,omitempty"`
	DefaultPassword *string `json:"default_password,omitempty"`
	SSHKeyID        *string `json:"ssh_key_id,omitempty"`
	OSImageID       *string `json:"os_image_id,omitempty"`
	// DeviceProfile: nil = unchanged; a zero equipage (empty profile_id) =
	// clear back to inherit; a populated one = set for the unit's nodes.
	// Inherited as ONE block, and validated against the registry catalog in
	// the service before storing.
	DeviceProfile *device_profile.Equipage `json:"device_profile,omitempty"`
	// LEDConfig: nil = unchanged; a zero config (all defaults) = clear back to
	// inherit. One block, like the device profile.
	LEDConfig *node.LEDConfig `json:"led_config,omitempty"`
	// WatchdogConfig: nil = unchanged; a DISABLED config = clear back to
	// inherit. An OU declaration is always an armed supervision: with no
	// auto-apply there is nothing for an inheritable "off" to shield, and
	// disarm is the per-node action. One block, like the LED config, and the
	// save dispatches nothing.
	WatchdogConfig *node.WatchdogConfig `json:"watchdog_config,omitempty"`
	AuthContext    commands.AuthContext `json:"-"`
}
