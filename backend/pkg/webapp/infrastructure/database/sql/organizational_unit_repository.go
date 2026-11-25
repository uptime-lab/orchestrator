package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ repository.OrganizationalUnitRepository = (*OrganizationalUnitRepository)(nil)

type OrganizationalUnitRepository struct {
	db *gorm.DB
	// enc protects organizational_units.default_password at rest.
	enc Encryptor
}

func NewOrganizationalUnitRepository(db *gorm.DB, enc Encryptor) *OrganizationalUnitRepository {
	if enc == nil {
		panic("OrganizationalUnitRepository requires an encryptor")
	}
	return &OrganizationalUnitRepository{db: db, enc: enc}
}

func (r *OrganizationalUnitRepository) Setup() error {
	return r.db.AutoMigrate(&OrganizationalUnitModel{})
}

// BackfillDefaultPasswordEncryption encrypts the group passwords stored before
// the marker column existed. Non-fatal by contract.
func (r *OrganizationalUnitRepository) BackfillDefaultPasswordEncryption() (int, error) {
	return backfillProvisioningPasswords(r.db, r.enc, "organizational_units", "id")
}

// toDomain converts a row and decrypts its group password. Every read path goes
// through it.
func (r *OrganizationalUnitRepository) toDomain(m *OrganizationalUnitModel) *organizational_unit.OrganizationalUnit {
	ou := m.ToDomain()
	stored := ""
	if m.DefaultPassword != nil {
		stored = *m.DefaultPassword
	}
	plain, unreadable := decryptProvisioningPassword(r.enc, stored, m.DefaultPasswordEnc, "organizational_units."+m.ID.String())
	ou.DefaultPasswordUnreadable = unreadable
	switch {
	case unreadable:
		ou.DefaultPassword = nil
	case m.DefaultPassword == nil:
		ou.DefaultPassword = nil
	default:
		ou.DefaultPassword = &plain
	}
	return ou
}

// fromDomain fills a row and encrypts its group password, reporting whether the
// password columns must be dropped from the write (an undecryptable value the
// caller did not replace is left alone rather than blanked).
func (r *OrganizationalUnitRepository) fromDomain(m *OrganizationalUnitModel, ou *organizational_unit.OrganizationalUnit) (bool, error) {
	m.FromDomain(ou)
	if ou.DefaultPasswordUnreadable && ou.DefaultPassword == nil {
		return true, nil
	}
	if ou.DefaultPassword == nil {
		m.DefaultPassword = nil
		m.DefaultPasswordEnc = 0
		return false, nil
	}
	stored, marker, err := encryptProvisioningPassword(r.enc, *ou.DefaultPassword)
	if err != nil {
		return false, err
	}
	m.DefaultPassword = &stored
	m.DefaultPasswordEnc = marker
	return false, nil
}

func (r *OrganizationalUnitRepository) Create(ctx context.Context, ou *organizational_unit.OrganizationalUnit) error {
	model := &OrganizationalUnitModel{}
	if _, err := r.fromDomain(model, ou); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Create(model).Error
}

// ouUpdateColumns are the columns Update writes, the password pair aside.
// Every column of OrganizationalUnitModel is either here or named in the
// repository test's list of columns Update deliberately leaves alone, so a
// new policy column cannot be added to the model and silently skipped here:
// the request would answer success, echo the value, and store nothing.
var ouUpdateColumns = []string{
	"name", "parent_id", "position", "network_first", "boot_to_os", "ssh_password_auth",
	"default_username", "ssh_key_id", "os_image_id", "device_profile", "led_config",
	"watchdog_config", "updated_at",
}

// ouPasswordColumns are written only when the stored password is replaced or
// cleared; an unreadable one the caller did not touch keeps its ciphertext.
var ouPasswordColumns = []string{"default_password", "default_password_enc"}

func (r *OrganizationalUnitRepository) Update(ctx context.Context, ou *organizational_unit.OrganizationalUnit) error {
	model := &OrganizationalUnitModel{}
	keepPassword, err := r.fromDomain(model, ou)
	if err != nil {
		return err
	}
	model.UpdatedAt = time.Now()

	// Select the mutable columns explicitly so nil policy pointers are written
	// as NULL ("inherit"), which struct-based Updates would otherwise skip.
	cols := append([]string(nil), ouUpdateColumns...)
	if !keepPassword {
		cols = append(cols, ouPasswordColumns...)
	}
	result := r.db.WithContext(ctx).
		Model(&OrganizationalUnitModel{}).
		Where("id = ? AND account_id = ?", ou.ID, ou.AccountID).
		Select(cols).
		Updates(model)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrOrganizationalUnitNotFound
	}
	return nil
}

func (r *OrganizationalUnitRepository) GetByID(ctx context.Context, accountID, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	var model OrganizationalUnitModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		First(&model)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, app_errors.ErrOrganizationalUnitNotFound
		}
		return nil, result.Error
	}
	return r.toDomain(&model), nil
}

func (r *OrganizationalUnitRepository) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	var models []OrganizationalUnitModel
	result := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("position ASC, created_at ASC").
		Limit(listRunawayCeiling).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}
	ous := make([]organizational_unit.OrganizationalUnit, len(models))
	for i := range models {
		ous[i] = *r.toDomain(&models[i])
	}
	return ous, nil
}

func (r *OrganizationalUnitRepository) Delete(ctx context.Context, accountID, id uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", id, accountID).
		Delete(&OrganizationalUnitModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrOrganizationalUnitNotFound
	}
	return nil
}

func (r *OrganizationalUnitRepository) CountChildren(ctx context.Context, accountID, parentID uuid.UUID) (int64, error) {
	var count int64
	result := r.db.WithContext(ctx).
		Model(&OrganizationalUnitModel{}).
		Where("account_id = ? AND parent_id = ?", accountID, parentID).
		Count(&count)
	return count, result.Error
}
