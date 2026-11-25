package sql

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// OrganizationalUnitModel is the GORM row for an organizational unit.
type OrganizationalUnitModel struct {
	ID              uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID       uuid.UUID  `gorm:"column:account_id;type:uuid;index;not null"`
	Name            string     `gorm:"column:name;size:255;not null"`
	ParentID        *uuid.UUID `gorm:"column:parent_id;type:uuid;index"`
	Position        int        `gorm:"column:position;not null;default:0"`
	NetworkFirst    *bool      `gorm:"column:network_first"`
	BootToOS        *bool      `gorm:"column:boot_to_os"`
	SSHPasswordAuth *bool      `gorm:"column:ssh_password_auth"`
	DefaultUsername *string    `gorm:"column:default_username;size:255"`
	DefaultPassword *string    `gorm:"column:default_password;type:text"`
	// DefaultPasswordEnc marks how DefaultPassword is stored: 0 = legacy
	// plaintext, 1 = AES-GCM. See MasterTokenModel for why this is a column and
	// not a prefix on the value.
	DefaultPasswordEnc int        `gorm:"column:default_password_enc;not null;default:0"`
	SSHKeyID           *uuid.UUID `gorm:"column:ssh_key_id;type:uuid"`
	OSImageID          *uuid.UUID `gorm:"column:os_image_id;type:uuid"`
	DeviceProfile      *string    `gorm:"column:device_profile;type:jsonb"`  // inheritable hardware equipage (NULL = inherit)
	LEDConfig          *string    `gorm:"column:led_config;type:jsonb"`      // inheritable LED behaviour (NULL = inherit)
	WatchdogConfig     *string    `gorm:"column:watchdog_config;type:jsonb"` // inheritable watchdog supervision (NULL = inherit)
	CreatedAt          time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (OrganizationalUnitModel) TableName() string {
	return "organizational_units"
}

func (m *OrganizationalUnitModel) ToDomain() *organizational_unit.OrganizationalUnit {
	return &organizational_unit.OrganizationalUnit{
		ID:              m.ID,
		AccountID:       m.AccountID,
		Name:            m.Name,
		ParentID:        m.ParentID,
		Position:        m.Position,
		NetworkFirst:    m.NetworkFirst,
		BootToOS:        m.BootToOS,
		SSHPasswordAuth: m.SSHPasswordAuth,
		DefaultUsername: m.DefaultUsername,
		DefaultPassword: m.DefaultPassword,
		SSHKeyID:        m.SSHKeyID,
		OSImageID:       m.OSImageID,
		DeviceProfile:   equipageFromJSON(m.DeviceProfile),
		LEDConfig:       ledConfigFromJSON(m.LEDConfig),
		WatchdogConfig:  watchdogConfigFromJSON(m.WatchdogConfig),
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func (m *OrganizationalUnitModel) FromDomain(d *organizational_unit.OrganizationalUnit) {
	m.ID = d.ID
	m.AccountID = d.AccountID
	m.Name = d.Name
	m.ParentID = d.ParentID
	m.Position = d.Position
	m.NetworkFirst = d.NetworkFirst
	m.BootToOS = d.BootToOS
	m.SSHPasswordAuth = d.SSHPasswordAuth
	m.DefaultUsername = d.DefaultUsername
	m.DefaultPassword = d.DefaultPassword
	m.SSHKeyID = d.SSHKeyID
	m.OSImageID = d.OSImageID
	m.DeviceProfile = equipageToJSON(d.DeviceProfile)
	m.LEDConfig = ledConfigToJSON(d.LEDConfig)
	m.WatchdogConfig = watchdogConfigToJSON(d.WatchdogConfig)
	m.CreatedAt = d.CreatedAt
	m.UpdatedAt = d.UpdatedAt
}

// equipageFromJSON/equipageToJSON are shared by the OU, account-policy and
// node models: an empty or unparseable stored value reads as nil (= inherit),
// and a zero equipage is stored as NULL rather than "{}" so "cleared" and
// "never set" are the same state.
func equipageFromJSON(raw *string) *device_profile.Equipage {
	if raw == nil || *raw == "" {
		return nil
	}
	var eq device_profile.Equipage
	if err := json.Unmarshal([]byte(*raw), &eq); err != nil || eq.IsZero() {
		return nil
	}
	return &eq
}

func equipageToJSON(eq *device_profile.Equipage) *string {
	if eq == nil || eq.IsZero() {
		return nil
	}
	data, _ := json.Marshal(eq)
	s := string(data)
	return &s
}

// ledConfigFromJSON/ledConfigToJSON follow the equipage helpers' contract:
// empty/unparseable/zero reads as nil (= inherit), and a zero config stores as
// NULL so "cleared" and "never set" are the same state.
func ledConfigFromJSON(raw *string) *node.LEDConfig {
	if raw == nil || *raw == "" {
		return nil
	}
	var cfg node.LEDConfig
	if err := json.Unmarshal([]byte(*raw), &cfg); err != nil || cfg.IsZero() {
		return nil
	}
	return &cfg
}

func ledConfigToJSON(cfg *node.LEDConfig) *string {
	if cfg == nil || cfg.IsZero() {
		return nil
	}
	data, _ := json.Marshal(cfg)
	s := string(data)
	return &s
}

// watchdogConfigFromJSON/watchdogConfigToJSON: empty or unparseable reads as
// nil (= inherit). Unlike the LED pair there is NO IsZero guard — a
// declared-but-disabled watchdog config is a meaningful declaration
// ("supervision off"), so only nil means inherit.
func watchdogConfigFromJSON(raw *string) *node.WatchdogConfig {
	if raw == nil || *raw == "" {
		return nil
	}
	var cfg node.WatchdogConfig
	if err := json.Unmarshal([]byte(*raw), &cfg); err != nil {
		return nil
	}
	return &cfg
}

func watchdogConfigToJSON(cfg *node.WatchdogConfig) *string {
	if cfg == nil {
		return nil
	}
	data, _ := json.Marshal(cfg)
	s := string(data)
	return &s
}
