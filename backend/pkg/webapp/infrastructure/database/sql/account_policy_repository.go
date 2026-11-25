package sql

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.AccountPolicyRepository = (*AccountPolicyRepository)(nil)

// AccountPolicyModel stores the account-wide policy fallback (one row per
// account); field semantics mirror OrganizationalUnitModel (NULL = unset).
type AccountPolicyModel struct {
	AccountID       uuid.UUID `gorm:"column:account_id;type:uuid;primaryKey;not null"`
	NetworkFirst    *bool     `gorm:"column:network_first"`
	BootToOS        *bool     `gorm:"column:boot_to_os"`
	SSHPasswordAuth *bool     `gorm:"column:ssh_password_auth"`
	DefaultUsername *string   `gorm:"column:default_username;size:255"`
	DefaultPassword *string   `gorm:"column:default_password;type:text"`
	// DefaultPasswordEnc marks how DefaultPassword is stored: 0 = legacy
	// plaintext, 1 = AES-GCM. See MasterTokenModel for why this is a column and
	// not a prefix on the value.
	DefaultPasswordEnc int        `gorm:"column:default_password_enc;not null;default:0"`
	SSHKeyID           *uuid.UUID `gorm:"column:ssh_key_id;type:uuid"`
	OSImageID          *uuid.UUID `gorm:"column:os_image_id;type:uuid"`
	DeviceProfile      *string    `gorm:"column:device_profile;type:jsonb"`  // account-wide equipage fallback (NULL = none)
	LEDConfig          *string    `gorm:"column:led_config;type:jsonb"`      // account-wide LED fallback (NULL = none)
	WatchdogConfig     *string    `gorm:"column:watchdog_config;type:jsonb"` // account-wide watchdog fallback (NULL = none)
	CreatedAt          time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (AccountPolicyModel) TableName() string {
	return "account_policies"
}

func (m *AccountPolicyModel) ToDomain() *organizational_unit.AccountPolicy {
	return &organizational_unit.AccountPolicy{
		AccountID:       m.AccountID,
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

func (m *AccountPolicyModel) FromDomain(p *organizational_unit.AccountPolicy) {
	m.AccountID = p.AccountID
	m.NetworkFirst = p.NetworkFirst
	m.BootToOS = p.BootToOS
	m.SSHPasswordAuth = p.SSHPasswordAuth
	m.DefaultUsername = p.DefaultUsername
	m.DefaultPassword = p.DefaultPassword
	m.SSHKeyID = p.SSHKeyID
	m.OSImageID = p.OSImageID
	m.DeviceProfile = equipageToJSON(p.DeviceProfile)
	m.LEDConfig = ledConfigToJSON(p.LEDConfig)
	m.WatchdogConfig = watchdogConfigToJSON(p.WatchdogConfig)
	m.CreatedAt = p.CreatedAt
	m.UpdatedAt = p.UpdatedAt
}

type AccountPolicyRepository struct {
	db *gorm.DB
	// enc protects account_policies.default_password at rest.
	enc Encryptor
}

func NewAccountPolicyRepository(db *gorm.DB, enc Encryptor) *AccountPolicyRepository {
	if enc == nil {
		panic("AccountPolicyRepository requires an encryptor")
	}
	return &AccountPolicyRepository{db: db, enc: enc}
}

func (r *AccountPolicyRepository) Setup() error {
	return r.db.AutoMigrate(&AccountPolicyModel{})
}

// BackfillDefaultPasswordEncryption encrypts the account-level passwords stored
// before the marker column existed. Non-fatal by contract.
func (r *AccountPolicyRepository) BackfillDefaultPasswordEncryption() (int, error) {
	return backfillProvisioningPasswords(r.db, r.enc, "account_policies", "account_id")
}

// toDomain converts a row and decrypts its password. Every read path goes
// through it.
func (r *AccountPolicyRepository) toDomain(m *AccountPolicyModel) *organizational_unit.AccountPolicy {
	p := m.ToDomain()
	stored := ""
	if m.DefaultPassword != nil {
		stored = *m.DefaultPassword
	}
	plain, unreadable := decryptProvisioningPassword(r.enc, stored, m.DefaultPasswordEnc, "account_policies."+m.AccountID.String())
	p.DefaultPasswordUnreadable = unreadable
	if unreadable || m.DefaultPassword == nil {
		p.DefaultPassword = nil
	} else {
		p.DefaultPassword = &plain
	}
	return p
}

// Get returns the account policy, or nil when the account never set one.
func (r *AccountPolicyRepository) Get(ctx context.Context, accountID uuid.UUID) (*organizational_unit.AccountPolicy, error) {
	var model AccountPolicyModel
	result := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, result.Error
	}
	return r.toDomain(&model), nil
}

// accountPolicyUpsertColumns are the columns the conflict arm of Upsert
// rewrites, the password pair aside (accountPolicyPasswordColumns). The
// repository test holds every model column to this list or to its named
// exceptions.
var accountPolicyUpsertColumns = []string{
	"network_first", "boot_to_os", "ssh_password_auth", "default_username",
	"ssh_key_id", "os_image_id", "device_profile", "led_config", "watchdog_config", "updated_at",
}

var accountPolicyPasswordColumns = []string{"default_password", "default_password_enc"}

// Upsert creates or replaces the account policy. All policy columns are
// written explicitly so nil pointers reset a field back to NULL ("unset") —
// a column missing from this list silently stops being persisted.
func (r *AccountPolicyRepository) Upsert(ctx context.Context, policy *organizational_unit.AccountPolicy) error {
	model := &AccountPolicyModel{}
	model.FromDomain(policy)
	model.UpdatedAt = time.Now()
	if model.CreatedAt.IsZero() {
		model.CreatedAt = model.UpdatedAt
	}

	cols := append([]string(nil), accountPolicyUpsertColumns...)
	switch {
	// An undecryptable stored password the caller did not replace keeps its
	// bytes: leaving the columns out of the conflict update preserves the row's
	// ciphertext for a rotation that restores the right key.
	case policy.DefaultPasswordUnreadable && policy.DefaultPassword == nil:
		model.DefaultPassword = nil
		model.DefaultPasswordEnc = 0
	case policy.DefaultPassword == nil:
		model.DefaultPassword = nil
		model.DefaultPasswordEnc = 0
		cols = append(cols, accountPolicyPasswordColumns...)
	default:
		stored, marker, err := encryptProvisioningPassword(r.enc, *policy.DefaultPassword)
		if err != nil {
			return err
		}
		model.DefaultPassword = &stored
		model.DefaultPasswordEnc = marker
		cols = append(cols, accountPolicyPasswordColumns...)
	}

	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_id"}},
			DoUpdates: clause.AssignmentColumns(cols),
		}).
		Create(model).Error; err != nil {
		return err
	}
	// The caller answers with the record it passed in, so it carries the
	// timestamps just written; a first save would otherwise report the zero
	// time. created_at is not in the conflict update, so a stored row keeps
	// its own and only a record that had none takes the insert's.
	policy.UpdatedAt = model.UpdatedAt
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = model.CreatedAt
	}
	return nil
}
