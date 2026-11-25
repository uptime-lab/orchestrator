package results

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"time"
)

// AccountPolicyResult is the API DTO for the account-wide policy fallback.
// The password value is never returned; HasDefaultPassword reports whether
// one is set.
type AccountPolicyResult struct {
	AccountID          uuid.UUID                      `json:"account_id"`
	NetworkFirst       organizational_unit.PolicyFlag `json:"network_first"`
	BootToOS           organizational_unit.PolicyFlag `json:"boot_to_os"`
	SSHPasswordAuth    organizational_unit.PolicyFlag `json:"ssh_password_auth"`
	DefaultUsername    *string                        `json:"default_username,omitempty"`
	HasDefaultPassword bool                           `json:"has_default_password"`
	SSHKeyID           *uuid.UUID                     `json:"ssh_key_id,omitempty"`
	OSImageID          *uuid.UUID                     `json:"os_image_id,omitempty"`
	// DeviceProfile mirrors the unit DTO: the PUT has always accepted it, and a
	// GET that hid it let an editor clear a stored profile without ever having
	// shown it (absent = inherit/none).
	DeviceProfile *device_profile.Equipage `json:"device_profile,omitempty"`
	// LEDConfig mirrors the unit DTO (absent = none).
	LEDConfig *node.LEDConfig `json:"led_config,omitempty"`
	// WatchdogConfig mirrors the unit DTO (absent = none).
	WatchdogConfig *node.WatchdogConfig `json:"watchdog_config,omitempty"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

// NewAccountPolicyResult maps the domain account policy to its DTO.
func NewAccountPolicyResult(p *organizational_unit.AccountPolicy) AccountPolicyResult {
	return AccountPolicyResult{
		AccountID:          p.AccountID,
		NetworkFirst:       organizational_unit.PolicyFlagFromBool(p.NetworkFirst),
		BootToOS:           organizational_unit.PolicyFlagFromBool(p.BootToOS),
		SSHPasswordAuth:    organizational_unit.PolicyFlagFromBool(p.SSHPasswordAuth),
		DefaultUsername:    p.DefaultUsername,
		HasDefaultPassword: p.DefaultPassword != nil && *p.DefaultPassword != "",
		SSHKeyID:           p.SSHKeyID,
		OSImageID:          p.OSImageID,
		DeviceProfile:      p.DeviceProfile,
		LEDConfig:          p.LEDConfig,
		WatchdogConfig:     p.WatchdogConfig,
		UpdatedAt:          p.UpdatedAt,
	}
}
