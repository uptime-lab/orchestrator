package results

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"time"
)

// OrganizationalUnitResult is the API DTO for an organizational unit.
type OrganizationalUnitResult struct {
	ID              uuid.UUID                      `json:"id"`
	AccountID       uuid.UUID                      `json:"account_id"`
	Name            string                         `json:"name"`
	ParentID        *uuid.UUID                     `json:"parent_id,omitempty"`
	IsRoot          bool                           `json:"is_root"`
	Position        int                            `json:"position"`
	NetworkFirst    organizational_unit.PolicyFlag `json:"network_first"`
	BootToOS        organizational_unit.PolicyFlag `json:"boot_to_os"`
	SSHPasswordAuth organizational_unit.PolicyFlag `json:"ssh_password_auth"`
	// Provisioning defaults (nil/absent = inherit). Password value is never
	// returned; HasDefaultPassword reports whether one is set.
	DefaultUsername    *string    `json:"default_username,omitempty"`
	HasDefaultPassword bool       `json:"has_default_password"`
	SSHKeyID           *uuid.UUID `json:"ssh_key_id,omitempty"`
	OSImageID          *uuid.UUID `json:"os_image_id,omitempty"`
	// DeviceProfile is the unit's inheritable hardware equipage (absent =
	// inherit). One block, like the domain field.
	DeviceProfile *device_profile.Equipage `json:"device_profile,omitempty"`
	// LEDConfig is the unit's inheritable LED behaviour (absent = inherit).
	LEDConfig *node.LEDConfig `json:"led_config,omitempty"`
	// WatchdogConfig is the unit's inheritable watchdog supervision (absent =
	// inherit). Declared state only; a device receives it through its
	// per-node apply.
	WatchdogConfig *node.WatchdogConfig `json:"watchdog_config,omitempty"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

// NewOrganizationalUnitResult maps a domain unit to its DTO.
func NewOrganizationalUnitResult(ou *organizational_unit.OrganizationalUnit) OrganizationalUnitResult {
	return OrganizationalUnitResult{
		ID:                 ou.ID,
		AccountID:          ou.AccountID,
		Name:               ou.Name,
		ParentID:           ou.ParentID,
		IsRoot:             ou.IsRoot(),
		Position:           ou.Position,
		NetworkFirst:       organizational_unit.PolicyFlagFromBool(ou.NetworkFirst),
		BootToOS:           organizational_unit.PolicyFlagFromBool(ou.BootToOS),
		SSHPasswordAuth:    organizational_unit.PolicyFlagFromBool(ou.SSHPasswordAuth),
		DefaultUsername:    ou.DefaultUsername,
		HasDefaultPassword: ou.DefaultPassword != nil && *ou.DefaultPassword != "",
		SSHKeyID:           ou.SSHKeyID,
		OSImageID:          ou.OSImageID,
		DeviceProfile:      ou.DeviceProfile,
		LEDConfig:          ou.LEDConfig,
		WatchdogConfig:     ou.WatchdogConfig,
		CreatedAt:          ou.CreatedAt,
		UpdatedAt:          ou.UpdatedAt,
	}
}

// OrganizationalUnitListResult is the list DTO.
type OrganizationalUnitListResult struct {
	Units []OrganizationalUnitResult `json:"units"`
	Count int                        `json:"count"`
}

// NewOrganizationalUnitListResult maps domain units to the list DTO.
func NewOrganizationalUnitListResult(ous []organizational_unit.OrganizationalUnit) OrganizationalUnitListResult {
	units := make([]OrganizationalUnitResult, len(ous))
	for i := range ous {
		units[i] = NewOrganizationalUnitResult(&ous[i])
	}
	return OrganizationalUnitListResult{Units: units, Count: len(units)}
}
