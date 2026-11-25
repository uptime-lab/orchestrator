package sql

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.NodeTombstoneRepository = (*NodeTombstoneRepository)(nil)

// NodeTombstoneModel is the identity of a deleted node.
//
// The primary key is the DELETED NODE'S id, which is also the reason a restore
// works at all: the device authenticates as that id. unique_token has no unique
// index here even though `nodes.unique_token` does — the token of a restored node
// legitimately exists in both tables at once, and a unique index would make the
// restore fail at the moment it mattered.
type NodeTombstoneModel struct {
	ID             uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID      uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
	HardwareSerial string    `gorm:"column:hardware_serial;size:64;index"`
	UniqueToken    string    `gorm:"column:unique_token;size:255;not null"` // ENCRYPTED at rest (see UniqueTokenEnc)
	// UniqueTokenEnc marks how UniqueToken is stored: 0 = legacy plaintext,
	// 1 = AES-GCM — the nodes.unique_token_enc rule.
	UniqueTokenEnc int        `gorm:"column:unique_token_enc;not null;default:0"`
	Name           string     `gorm:"column:name;size:255"`
	Hostname       string     `gorm:"column:hostname;size:255"`
	OUID           *uuid.UUID `gorm:"column:ou_id;type:uuid"`
	AdoptionMethod string     `gorm:"column:adoption_method;size:16;not null;default:'unknown'"`
	// EEPROMBakeConfirmed / BootOrderNetbootsFirst: see the domain fields.
	EEPROMBakeConfirmed    bool  `gorm:"column:eeprom_bake_confirmed;not null;default:false"`
	BootOrderNetbootsFirst *bool `gorm:"column:boot_order_netboots_first"`
	BootToOS               *bool `gorm:"column:boot_to_os"`
	// WasAdmitted / ApprovalStatus / Status: the node's admission state at
	// deletion (see the domain fields). was_admitted is NULLABLE on purpose:
	// NULL is a row written before it was recorded, which the restore treats
	// as not admitted, and a NOT NULL default would have to guess.
	WasAdmitted    *bool  `gorm:"column:was_admitted"`
	ApprovalStatus string `gorm:"column:approval_status;size:32"`
	Status         string `gorm:"column:status;size:32"`
	// IdentityReleaseRequested: the platform asked the agent to wipe its own
	// adoption file. See the domain field's comment for why "requested".
	IdentityReleaseRequested bool       `gorm:"column:identity_release_requested;not null;default:false"`
	IdentityReleasedAt       *time.Time `gorm:"column:identity_released_at"`
	LastRefusedAt            *time.Time `gorm:"column:last_refused_at"`
	DeletedAt                time.Time  `gorm:"column:deleted_at;not null;default:now()"`
	DeletedBy                uuid.UUID  `gorm:"column:deleted_by;type:uuid"`
	RestoredAt               *time.Time `gorm:"column:restored_at"`
}

func (NodeTombstoneModel) TableName() string {
	return "node_tombstones"
}

func (m *NodeTombstoneModel) ToDomain() *node_tombstone.Tombstone {
	return &node_tombstone.Tombstone{
		ID:                       m.ID,
		AccountID:                m.AccountID,
		HardwareSerial:           m.HardwareSerial,
		UniqueToken:              m.UniqueToken,
		Name:                     m.Name,
		Hostname:                 m.Hostname,
		OUID:                     m.OUID,
		AdoptionMethod:           m.AdoptionMethod,
		EEPROMBakeConfirmed:      m.EEPROMBakeConfirmed,
		BootOrderNetbootsFirst:   m.BootOrderNetbootsFirst,
		BootToOS:                 m.BootToOS,
		WasAdmitted:              m.WasAdmitted,
		ApprovalStatus:           node.ApprovalStatus(m.ApprovalStatus),
		Status:                   node.NodeStatus(m.Status),
		IdentityReleaseRequested: m.IdentityReleaseRequested,
		IdentityReleasedAt:       m.IdentityReleasedAt,
		LastRefusedAt:            m.LastRefusedAt,
		DeletedAt:                m.DeletedAt,
		DeletedBy:                m.DeletedBy,
		RestoredAt:               m.RestoredAt,
	}
}

func (m *NodeTombstoneModel) FromDomain(t *node_tombstone.Tombstone) {
	m.ID = t.ID
	m.AccountID = t.AccountID
	m.HardwareSerial = t.HardwareSerial
	m.UniqueToken = t.UniqueToken
	m.Name = t.Name
	m.Hostname = t.Hostname
	m.OUID = t.OUID
	m.AdoptionMethod = t.AdoptionMethod
	m.EEPROMBakeConfirmed = t.EEPROMBakeConfirmed
	m.BootOrderNetbootsFirst = t.BootOrderNetbootsFirst
	m.BootToOS = t.BootToOS
	m.WasAdmitted = t.WasAdmitted
	m.ApprovalStatus = string(t.ApprovalStatus)
	m.Status = string(t.Status)
	m.IdentityReleaseRequested = t.IdentityReleaseRequested
	m.IdentityReleasedAt = t.IdentityReleasedAt
	m.LastRefusedAt = t.LastRefusedAt
	m.DeletedAt = t.DeletedAt
	m.DeletedBy = t.DeletedBy
	m.RestoredAt = t.RestoredAt
}

type NodeTombstoneRepository struct {
	db *gorm.DB
	// enc protects node_tombstones.unique_token at rest.
	enc Encryptor
}

func NewNodeTombstoneRepository(db *gorm.DB, enc Encryptor) *NodeTombstoneRepository {
	if enc == nil {
		panic("NodeTombstoneRepository requires an encryptor")
	}
	return &NodeTombstoneRepository{db: db, enc: enc}
}

// toDomain converts a row and decrypts its token — every read path goes
// through it, the node repository's rule.
func (r *NodeTombstoneRepository) toDomain(m *NodeTombstoneModel) *node_tombstone.Tombstone {
	t := m.ToDomain()
	t.UniqueToken, t.UniqueTokenUnreadable = decryptDeviceToken(
		r.enc, m.UniqueToken, m.UniqueTokenEnc, "node_tombstones."+m.ID.String())
	return t
}

func (r *NodeTombstoneRepository) Setup() error {
	return r.db.AutoMigrate(&NodeTombstoneModel{})
}

func (r *NodeTombstoneRepository) Create(ctx context.Context, tombstone *node_tombstone.Tombstone) error {
	model := &NodeTombstoneModel{}
	model.FromDomain(tombstone)
	// The tombstone write is the ONE part of delete that is not best-effort;
	// an encryption failure here must fail the delete rather than store the
	// credential in the clear.
	stored, marker, err := encryptDeviceToken(r.enc, tombstone.UniqueToken)
	if err != nil {
		return err
	}
	model.UniqueToken = stored
	model.UniqueTokenEnc = marker
	// Overwrite on conflict: a node restored and then deleted again must leave a
	// fresh record, not fail on the stale one and silently strand the device a
	// second time.
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).
		Create(model).Error
}

func (r *NodeTombstoneRepository) ListByAccountID(ctx context.Context, accountID uuid.UUID) ([]node_tombstone.Tombstone, error) {
	var models []NodeTombstoneModel
	if err := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("deleted_at DESC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]node_tombstone.Tombstone, len(models))
	for i := range models {
		out[i] = *r.toDomain(&models[i])
	}
	return out, nil
}

func (r *NodeTombstoneRepository) GetByID(ctx context.Context, accountID, nodeID uuid.UUID) (*node_tombstone.Tombstone, error) {
	var model NodeTombstoneModel
	err := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", nodeID, accountID).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNotFound
		}
		return nil, err
	}
	return r.toDomain(&model), nil
}

func (r *NodeTombstoneRepository) FindByHardwareSerial(ctx context.Context, serial string) (*node_tombstone.Tombstone, error) {
	if serial == "" {
		// An empty serial would otherwise match every tombstone of a node that
		// never reported a fingerprint.
		return nil, app_errors.ErrNotFound
	}
	var model NodeTombstoneModel
	err := r.db.WithContext(ctx).
		// A RESTORED tombstone is history, not a live record of a deleted
		// device. Matching one made the fail-closed HTTP-boot handler log
		// "withholding for a DELETED node … restore it" about a node that had
		// already been restored — which sends the operator to do again the thing
		// that did not work, instead of at the missing serial binding.
		Where("hardware_serial = ? AND restored_at IS NULL", serial).
		Order("deleted_at DESC").
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrNotFound
		}
		return nil, err
	}
	return r.toDomain(&model), nil
}

func (r *NodeTombstoneRepository) MarkRestored(ctx context.Context, accountID, nodeID uuid.UUID) error {
	now := time.Now()
	res := r.db.WithContext(ctx).
		Model(&NodeTombstoneModel{}).
		Where("id = ? AND account_id = ?", nodeID, accountID).
		Update("restored_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return app_errors.ErrNotFound
	}
	return nil
}

func (r *NodeTombstoneRepository) MarkIdentityReleased(ctx context.Context, accountID, nodeID uuid.UUID, at time.Time) error {
	res := r.db.WithContext(ctx).
		Model(&NodeTombstoneModel{}).
		// First report wins: a replayed terminal frame must not move the time.
		Where("id = ? AND account_id = ? AND identity_released_at IS NULL", nodeID, accountID).
		Update("identity_released_at", at)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Either no tombstone or already stamped; the caller cannot tell them
		// apart and does not need to — both mean "nothing to do".
		return app_errors.ErrNotFound
	}
	return nil
}

func (r *NodeTombstoneRepository) MarkRefused(ctx context.Context, accountID, nodeID uuid.UUID, at time.Time, minInterval time.Duration) error {
	return r.db.WithContext(ctx).
		Model(&NodeTombstoneModel{}).
		Where("id = ? AND account_id = ? AND restored_at IS NULL AND (last_refused_at IS NULL OR last_refused_at < ?)",
			nodeID, accountID, at.Add(-minInterval)).
		Update("last_refused_at", at).Error
}

func (r *NodeTombstoneRepository) Delete(ctx context.Context, accountID, nodeID uuid.UUID) error {
	res := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", nodeID, accountID).
		Delete(&NodeTombstoneModel{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return app_errors.ErrNotFound
	}
	return nil
}
