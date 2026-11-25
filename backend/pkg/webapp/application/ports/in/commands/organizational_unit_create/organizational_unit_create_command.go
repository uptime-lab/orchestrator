package organizational_unit_create

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// OrganizationalUnitCreateCommand creates an organizational unit. A nil ParentID
// makes it a root unit; a non-nil ParentID nests it one level under a root.
type OrganizationalUnitCreateCommand struct {
	ID           uuid.UUID                      `json:"-"` // set internally
	Name         string                         `json:"name"`
	ParentID     *uuid.UUID                     `json:"parent_id,omitempty"`
	NetworkFirst organizational_unit.PolicyFlag `json:"network_first,omitempty"` // inherit|on|off
	BootToOS     organizational_unit.PolicyFlag `json:"boot_to_os,omitempty"`    // inherit|on|off
	// SSHPasswordAuth controls whether sshd accepts password logins. Named in the
	// positive so "off" unambiguously means passwords are refused.
	SSHPasswordAuth organizational_unit.PolicyFlag `json:"ssh_password_auth,omitempty"`
	// Provisioning defaults; empty string means inherit. SSHKeyID/OSImageID are
	// UUID strings ("" = inherit).
	DefaultUsername string `json:"default_username,omitempty"`
	DefaultPassword string `json:"default_password,omitempty"`
	SSHKeyID        string `json:"ssh_key_id,omitempty"`
	OSImageID       string `json:"os_image_id,omitempty"`
	// Position, DeviceProfile, LEDConfig and WatchdogConfig take the update
	// command's meaning and validation: every policy a PUT stores, a create
	// stores too, rather than answering 201 and dropping it. Nil = not set.
	Position       *int                     `json:"position,omitempty"`
	DeviceProfile  *device_profile.Equipage `json:"device_profile,omitempty"`
	LEDConfig      *node.LEDConfig          `json:"led_config,omitempty"`
	WatchdogConfig *node.WatchdogConfig     `json:"watchdog_config,omitempty"`
	AuthContext    commands.AuthContext     `json:"-"`
}
