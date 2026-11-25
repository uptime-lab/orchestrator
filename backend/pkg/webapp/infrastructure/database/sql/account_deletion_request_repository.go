package sql

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_deletion_request"
	"gorm.io/gorm"
)

var _ repository.AccountDeletionRequestRepository = (*AccountDeletionRequestRepository)(nil)

// AccountDeletionRequestModel is a tenant's open request to be erased.
//
// account_id carries a UNIQUE index, not merely an index: at most one request
// may be open per account, and enforcing that in the database rather than in the
// service is what makes two simultaneous clicks produce one row instead of a
// race the read-then-write in the service would lose.
type AccountDeletionRequestModel struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID   uuid.UUID `gorm:"column:account_id;type:uuid;uniqueIndex;not null"`
	RequestedBy uuid.UUID `gorm:"column:requested_by;type:uuid;not null"`
	RequestedAt time.Time `gorm:"column:requested_at;not null;default:now()"`
	Reason      string    `gorm:"column:reason;type:text"`
}

func (AccountDeletionRequestModel) TableName() string {
	return "account_deletion_requests"
}

func (m *AccountDeletionRequestModel) ToDomain() *account_deletion_request.Request {
	return &account_deletion_request.Request{
		ID:          m.ID,
		AccountID:   m.AccountID,
		RequestedBy: m.RequestedBy,
		RequestedAt: m.RequestedAt,
		Reason:      m.Reason,
	}
}

func FromDomainAccountDeletionRequest(r *account_deletion_request.Request) *AccountDeletionRequestModel {
	return &AccountDeletionRequestModel{
		ID:          r.ID,
		AccountID:   r.AccountID,
		RequestedBy: r.RequestedBy,
		RequestedAt: r.RequestedAt,
		Reason:      r.Reason,
	}
}

type AccountDeletionRequestRepository struct {
	db *gorm.DB
}

func NewAccountDeletionRequestRepository(db *gorm.DB) *AccountDeletionRequestRepository {
	return &AccountDeletionRequestRepository{db: db}
}

func (r *AccountDeletionRequestRepository) Setup() error {
	return r.db.AutoMigrate(&AccountDeletionRequestModel{})
}

// Create inserts the request, translating the unique-index violation into the
// domain's "already open" error.
//
// GORM's TranslateError is relied on here, which is safe because this is a real
// Create and not a Raw/Exec — the distinction that has caught this codebase
// before, where ErrDuplicatedKey never fires on raw SQL.
func (r *AccountDeletionRequestRepository) Create(ctx context.Context, req *account_deletion_request.Request) error {
	err := r.db.WithContext(ctx).Create(FromDomainAccountDeletionRequest(req)).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return account_deletion_request.ErrAlreadyRequested
	}
	return err
}

func (r *AccountDeletionRequestRepository) GetByAccountID(ctx context.Context, accountID uuid.UUID) (*account_deletion_request.Request, error) {
	var m AccountDeletionRequestModel
	err := r.db.WithContext(ctx).Where("account_id = ?", accountID).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, app_errors.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return m.ToDomain(), nil
}

// DeleteByAccountID cancels the request. It reports whether a row was actually
// removed, so the caller can answer 404 rather than pretending it cancelled
// something that was never there.
func (r *AccountDeletionRequestRepository) DeleteByAccountID(ctx context.Context, accountID uuid.UUID) (bool, error) {
	res := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Delete(&AccountDeletionRequestModel{})
	return res.RowsAffected > 0, res.Error
}
