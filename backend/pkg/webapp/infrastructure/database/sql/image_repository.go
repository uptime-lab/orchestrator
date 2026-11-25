package sql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Verify interface implementation
var _ repository.ImageRepository = (*ImageRepository)(nil)

// ImageRepository is the implementation of the image repository using SQL
type ImageRepository struct {
	db *gorm.DB
}

// autoMigrate creates the necessary database tables
func (r *ImageRepository) autoMigrate() error {
	return r.db.AutoMigrate(&ImageModel{})
}

// Setup initializes the repository and creates tables if needed
func (r *ImageRepository) Setup() error {
	if err := r.autoMigrate(); err != nil {
		return err
	}
	// Backfill for pre-kind captures: they marked themselves by writing
	// "backup"/"golden" into the version field. Move that marker into kind
	// and clear the abused version so it can serve as a real release string.
	// Their source node was never recorded, so source_node_id stays NULL —
	// NeedsIdentityStripFor treats an unknown source as "strip" (fail safe).
	return r.db.Exec(
		`UPDATE images SET kind = version, version = '' WHERE version IN ('backup', 'golden') AND kind = 'os'`,
	).Error
}

// NewImageRepository creates a new image repository
func NewImageRepository(db *gorm.DB) *ImageRepository {
	repo := &ImageRepository{
		db: db,
	}
	return repo
}

// Create creates a new image
func (r *ImageRepository) Create(ctx context.Context, img *image.Image) error {
	model := &ImageModel{}
	model.FromDomain(img)

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// Update updates an existing image
func (r *ImageRepository) Update(ctx context.Context, img *image.Image) error {
	model := &ImageModel{}
	model.FromDomain(img)

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	result := updateAllColumns(r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", img.ID, img.AccountID), model)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrImageNotFound
	}

	return nil
}

// UpdateMetadata writes the four operator-editable fields and nothing else.
//
// The metadata PUT used to go through Update, which is `Select("*").Updates` —
// the only writer of `images` that is not claim-conditional. A title edit whose
// read landed just before ClaimUpload or ClaimCompression wrote
// compression_status="" back OVER the claim, after which a concurrent compress
// legitimately won and both jobs wrote the same deterministic object key: the
// exact disaster the exclusive claim exists to prevent. In the other order it
// wrote a stale file_storage_info/checksum over a finished compression, so with
// IMAGE_COMPRESS_REPLACE_ORIGINAL=true the record pointed at a deleted
// object and read in_progress forever.
//
// Naming the columns is the whole fix: a metadata edit cannot express an opinion
// about a claim it never read.
func (r *ImageRepository) UpdateMetadata(ctx context.Context, accountID, imageID uuid.UUID, title, version, description, imageURL string) error {
	result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ? AND account_id = ?", imageID, accountID).
		Updates(map[string]interface{}{
			"title":       title,
			"version":     version,
			"description": description,
			"image_url":   imageURL,
			"updated_at":  time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrImageNotFound
	}
	return nil
}

// ClaimUpload atomically marks the record as being uploaded into, but only
// while no other job holds it, reporting whether this caller won. It is the
// upload half of the mutual exclusion whose other half is ClaimCompression.
//
// Taken BEFORE the first byte is stored, which is the whole point. Checking the
// status at the top of the upload and writing the record at the end leaves the
// entire transfer — minutes, for a multi-GB image — as a window in which a
// compress can claim, work from the record as it was, and then delete what it
// believes is the stale object: the file the upload just wrote. A check that
// happens at the END cannot help either, because by then the object has already
// been overwritten; the deterministic key means a same-extension re-upload
// lands on exactly the key the compression is reading.
//
// Losing is not an error: the caller answers ErrImageCompressionInProgress
// without having touched storage at all.
func (r *ImageRepository) ClaimUpload(ctx context.Context, accountID, imageID uuid.UUID) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ? AND account_id = ? AND compression_status NOT IN (?)",
			imageID, accountID, image.BusyStatuses()).
		Updates(map[string]interface{}{
			"compression_status": image.UploadInProgress,
			"updated_at":         time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected > 0 {
		return true, nil
	}
	// Zero rows is either "held by a job" or "no such image"; answer honestly
	// rather than reporting a busy record for a row that is gone (same shape as
	// Delete above).
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ? AND account_id = ?", imageID, accountID).
		Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		return false, app_errors.ErrImageNotFound
	}
	return false, nil
}

// ReleaseUpload clears an upload claim, for the paths that do not go on to write
// the record themselves (a failed or refused upload). Clearing only OUR state:
// it is a no-op unless the record still reads as uploading, so a claim that has
// already been superseded is never disturbed.
//
// It restores the status the claim DISPLACED rather than writing "". ClaimUpload
// is allowed from any non-busy status, `capture_failed` included, and a blanket
// "" turned a capture record whose upload failed into a dead slot: the agent's
// retry goes through ClaimCaptureUpload, which accepts only `capturing` or
// `capture_failed`, so it was refused permanently — while quotaOccupyingScope
// still counted the row, costing an image slot under a small cap.
//
// The displaced status is DERIVED rather than remembered, which is what lets the
// startup sweep use the same rule with no memory of a process that is gone: a
// record with a source node and no stored file is a capture that never landed,
// and `capture_failed` is the only status that describes it. Anything else — an
// ordinary image record, or a capture that already has its bytes — is idle, and
// idle is "".
func releaseUploadStatus() interface{} {
	return gorm.Expr("CASE WHEN source_node_id IS NOT NULL AND file_storage_info IS NULL THEN ? ELSE ? END",
		image.CaptureFailed, "")
}

func (r *ImageRepository) ReleaseUpload(ctx context.Context, accountID, imageID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ? AND account_id = ? AND compression_status = ?",
			imageID, accountID, image.UploadInProgress).
		Updates(map[string]interface{}{
			"compression_status": releaseUploadStatus(),
			"updated_at":         time.Now(),
		}).Error
}

// ClaimCaptureUpload atomically marks a capture record as being streamed into,
// but only from the two states that legitimately await a capture. It is the
// capture counterpart of ClaimUpload, and it closes the same shape of window:
// the endpoint's HMAC capability is stateless and valid for hours, and the old
// read-then-act on compression_status let a replayed PUT run concurrently with
// the legitimate stream — both writing the same deterministic object key, the
// last finalize stamping its checksum over the other's bytes.
//
// Global (no account scope) deliberately: the caller is an agent presenting a
// signed URL and carries no account context (see GetByIDGlobal). The signature
// was verified before this is called; the claim adds exclusivity, not identity.
//
// Losing is not an error: the caller answers as if the record were not awaiting
// a capture, without having touched storage at all.
func (r *ImageRepository) ClaimCaptureUpload(ctx context.Context, imageID uuid.UUID) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ? AND compression_status IN (?)",
			imageID, []string{image.CaptureInProgress, image.CaptureFailed}).
		Updates(map[string]interface{}{
			"compression_status": image.CaptureUploadInProgress,
			"updated_at":         time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected > 0 {
		return true, nil
	}
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ?", imageID).
		Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		return false, app_errors.ErrImageNotFound
	}
	return false, nil
}

// FailAwaitingCapture moves a capture record that is still waiting for its
// stream (`capturing`) to `capture_failed`, for a capture command that ended
// without streaming. Conditional on that one state: a stream in flight owns
// its record (it settles it itself), and a finalized or already failed one is
// not this caller's to rewrite. Losing is not an error.
func (r *ImageRepository) FailAwaitingCapture(ctx context.Context, accountID, imageID uuid.UUID) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ? AND account_id = ? AND compression_status = ?",
			imageID, accountID, image.CaptureInProgress).
		Updates(map[string]interface{}{
			"compression_status": image.CaptureFailed,
			"updated_at":         time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// UpdateIfCaptureUploading writes the whole record only while the caller's
// capture claim still holds it. Both ends of a capture stream go through it —
// the finalize (status cleared, file info recorded) and the failure (status
// CaptureFailed) — so a claim that a restart already swept to CaptureFailed is
// never overwritten from a stale copy. Losing is not an error.
func (r *ImageRepository) UpdateIfCaptureUploading(ctx context.Context, img *image.Image) (bool, error) {
	model := &ImageModel{}
	model.FromDomain(img)
	model.UpdatedAt = time.Now()

	result := updateAllColumns(r.db.WithContext(ctx).
		Where("id = ? AND account_id = ? AND compression_status = ?",
			img.ID, img.AccountID, image.CaptureUploadInProgress),
		model)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// GetByID retrieves an image by ID within an account
func (r *ImageRepository) GetByID(ctx context.Context, accountID, imageID uuid.UUID) (*image.Image, error) {
	var model ImageModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", imageID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrImageNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(), nil
}

// GetByIDGlobal retrieves an image by ID without an account scope. Used only by
// the capability-authenticated capture upload endpoint, where the caller (an
// agent presenting an HMAC-signed URL) does not carry an account context; the
// signature over the image id is the authorization.
func (r *ImageRepository) GetByIDGlobal(ctx context.Context, imageID uuid.UUID) (*image.Image, error) {
	var model ImageModel
	result := r.db.WithContext(ctx).
		Where("id = ?", imageID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrImageNotFound
		}
		return nil, result.Error
	}
	return model.ToDomain(), nil
}

// ImageLockClass is the advisory-lock class for per-account image quota
// mutations ("ORCI"). It shares the lock space with MemberLockClass and
// TrackedFileLockClass, so the class word is what keeps the three from
// colliding.
const ImageLockClass = 0x4F524349

// quotaOccupyingScope builds the "which rows spend a slot of the per-account
// image COUNT cap" predicate. It is one function because the create path, the
// finalize backstop and the plain read all have to agree: two hand-written
// copies of this rule is how the count cap and the byte cap drifted apart in
// the first place.
//
// A finished image always counts. A FAILED capture never does — it holds no
// stored bytes and nothing is going to arrive, and counting it would let an
// abandoned capture consume the quota until someone cleans up by hand
// (permanently, under a cap of 1).
//
// An IN-PROGRESS capture counts only while it is younger than inFlight. That is
// what makes concurrent captures see each other — the whole point of the window
// — without reintroducing the brick: it holds its slot while it is plausibly
// still streaming and ages out afterwards. inFlight <= 0 counts no in-progress
// capture at all, which is the behaviour that shipped before this window
// existed.
func quotaOccupyingScope(q *gorm.DB, accountID uuid.UUID, inFlight time.Duration) *gorm.DB {
	// CaptureUploadInProgress rides the same window as CaptureInProgress: it is
	// the same capture, merely with its stream now in flight, so it must neither
	// gain nor lose a slot by the claim being taken.
	transient := []string{image.CaptureInProgress, image.CaptureUploadInProgress, image.CaptureFailed}
	inFlightStatuses := []string{image.CaptureInProgress, image.CaptureUploadInProgress}
	q = q.Where("account_id = ?", accountID)
	if inFlight <= 0 {
		return q.Where("compression_status NOT IN ?", transient)
	}
	return q.Where(
		"(compression_status NOT IN ? OR (compression_status IN ? AND created_at > ?))",
		transient,
		inFlightStatuses,
		time.Now().Add(-inFlight),
	)
}

// CountQuotaOccupying counts the account's images that spend a slot of the
// per-account count cap, optionally leaving one record out (a capture finalizing
// itself must not count itself).
func (r *ImageRepository) CountQuotaOccupying(
	ctx context.Context,
	accountID uuid.UUID,
	inFlight time.Duration,
	excludeImageID *uuid.UUID,
) (int64, error) {
	q := quotaOccupyingScope(r.db.WithContext(ctx).Model(&ImageModel{}), accountID, inFlight)
	if excludeImageID != nil {
		q = q.Where("id <> ?", *excludeImageID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CreateCaptureUnderCap creates a capture record only while the account is still
// under its image count cap, with the check and the insert in one transaction
// under the account's advisory lock.
//
// Read-then-create in the service could not hold the cap for captures at all:
// a backup playbook starts N captures at once, every one of them counts the same
// rows before any of them has inserted, and all N pass. The lock is what makes
// them serialize; the in-flight window (see quotaOccupyingScope) is what makes
// them visible to each other once they have. maxPerAccount <= 0 means unlimited,
// and then this is a plain insert.
func (r *ImageRepository) CreateCaptureUnderCap(
	ctx context.Context,
	img *image.Image,
	maxPerAccount int,
	inFlight time.Duration,
) error {
	if maxPerAccount <= 0 {
		return r.Create(ctx, img)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?, hashtext(?))",
			ImageLockClass, img.AccountID.String()).Error; err != nil {
			return err
		}
		var count int64
		if err := quotaOccupyingScope(tx.Model(&ImageModel{}), img.AccountID, inFlight).
			Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(maxPerAccount) {
			return fmt.Errorf("%w (max %d)", app_errors.ErrImageLimitReached, maxPerAccount)
		}
		model := &ImageModel{}
		model.FromDomain(img)
		return tx.Create(model).Error
	})
}

// CreateUnderCap creates a user-facing image record only while the account is
// under both its count cap and the byte ceilings, with the checks and the
// insert in one transaction under the account's advisory lock.
//
// Same reasoning as CreateCaptureUnderCap: the service's read-then-create could
// not hold either cap — N concurrent creates all count the same rows before any
// of them has inserted, and all N pass. The byte checks mirror the service's
// early refusal (the new record holds no bytes yet, so "full" means "no room
// for any bytes at all"); the count check mirrors quotaOccupyingScope. With no
// cap configured this is a plain insert.
func (r *ImageRepository) CreateUnderCap(
	ctx context.Context,
	img *image.Image,
	maxPerAccount int,
	inFlight time.Duration,
	maxBytesPerAccount int64,
	maxBytesInstance int64,
) error {
	if maxPerAccount <= 0 && maxBytesPerAccount <= 0 && maxBytesInstance <= 0 {
		return r.Create(ctx, img)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?, hashtext(?))",
			ImageLockClass, img.AccountID.String()).Error; err != nil {
			return err
		}
		if maxPerAccount > 0 {
			var count int64
			if err := quotaOccupyingScope(tx.Model(&ImageModel{}), img.AccountID, inFlight).
				Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(maxPerAccount) {
				return fmt.Errorf("%w (max %d)", app_errors.ErrImageLimitReached, maxPerAccount)
			}
		}
		if maxBytesPerAccount > 0 {
			var used int64
			if err := tx.Model(&ImageModel{}).
				Select(storedBytesExpr).
				Where("account_id = ?", img.AccountID).
				Scan(&used).Error; err != nil {
				return err
			}
			if used >= maxBytesPerAccount {
				return fmt.Errorf("%w: the stored image quota is full — delete an image to free space",
					app_errors.ErrImageLimitReached)
			}
		}
		if maxBytesInstance > 0 {
			var used int64
			if err := tx.Model(&ImageModel{}).
				Select(storedBytesExpr).
				Scan(&used).Error; err != nil {
				return err
			}
			if used >= maxBytesInstance {
				return fmt.Errorf("%w: the stored image quota is full — delete an image to free space",
					app_errors.ErrImageLimitReached)
			}
		}
		model := &ImageModel{}
		model.FromDomain(img)
		return tx.Create(model).Error
	})
}

// storedBytesExpr sums an image row's occupied bytes out of the
// file_storage_info jsonb: the current file plus a retained pre-compression
// original.
//
// The size lives in jsonb rather than a column, so this is an expression rather
// than a plain SUM. Both halves are COALESCEd because either key can be absent —
// a record created but never uploaded has no file_storage_info at all, and an
// image whose original was replaced (IMAGE_COMPRESS_REPLACE_ORIGINAL) has no
// source_size_bytes. A NULL from either would poison the whole
// sum and silently report an account as using nothing.
const storedBytesExpr = `COALESCE(SUM(
	COALESCE((file_storage_info->>'size_bytes')::bigint, 0) +
	COALESCE((file_storage_info->>'source_size_bytes')::bigint, 0)
), 0)`

// SumBytesByAccountID totals the bytes the account's images occupy.
//
// Unlike CountByAccountID this does NOT exclude in-progress or failed captures:
// a capture that has begun streaming has real bytes in the bucket, and the count
// cap excludes those records so an abandoned capture cannot brick image creation
// — a byte ceiling has the opposite concern, since bytes that exist are billed
// whatever the record says about them.
func (r *ImageRepository) SumBytesByAccountID(ctx context.Context, accountID uuid.UUID, excludeImageID *uuid.UUID) (int64, error) {
	var total int64
	q := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Select(storedBytesExpr).
		Where("account_id = ?", accountID)
	if excludeImageID != nil {
		q = q.Where("id <> ?", *excludeImageID)
	}
	if err := q.Scan(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// SumBytesAllAccounts totals stored image bytes across the instance.
func (r *ImageRepository) SumBytesAllAccounts(ctx context.Context) (int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Select(storedBytesExpr).
		Scan(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// GetAllByAccountID retrieves all images for a specific account
func (r *ImageRepository) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]image.Image, error) {
	var models []ImageModel
	result := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Limit(listRunawayCeiling).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	images := make([]image.Image, len(models))
	for i, model := range models {
		domainImage := model.ToDomain()
		images[i] = *domainImage
	}

	return images, nil
}

// Delete deletes an image by ID within an account
// Delete refuses while a compression job holds the row: the job's final
// StoreWithOptions would otherwise recreate the object AFTER the row (and its
// files) were deleted — an orphan invisible to the byte quota and to an account
// purge, which derives object keys from rows. The guard lives in the DELETE's
// WHERE rather than in a service-level status check so it serializes against
// ClaimCompression instead of racing it. Deliberately NOT extended
// to CaptureInProgress: an abandoned capture must stay deletable, or a stuck
// capture bricks cleanup — the same reasoning that keeps captures out of
// CountByAccountID.
func (r *ImageRepository) Delete(ctx context.Context, accountID, imageID uuid.UUID) error {
	// An in-flight UPLOAD holds the record too, for the same reason a compression
	// does and with a worse outcome: the delete removes the row and its objects,
	// the upload's store then RECREATES the object at the same deterministic key,
	// and its own cleanup skips it (the path equals the old one), leaving a
	// multi-GB object no row references — invisible to the byte quota and to the
	// account purge, both of which derive keys from rows.
	//
	// CaptureInProgress stays excluded, deliberately and as before: an abandoned
	// capture must remain deletable or a stuck one bricks cleanup. The upload
	// claim is safe to include because it is released even on a cancelled
	// request, so it cannot outlive a live process (see UploadFile's defer).
	// The capture-stream claim (CaptureUploadInProgress) is included for the
	// same reason as the upload claim — an agent is streaming into the object
	// key right now, so a delete here would orphan the object it is about to
	// recreate — and it is bounded the same way: released on a detached context
	// even when the agent vanishes, swept to CaptureFailed (deletable) at
	// startup if the process died holding it.
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ? AND compression_status NOT IN (?)", imageID, accountID,
			[]string{image.CompressionInProgress, image.UploadInProgress, image.CaptureUploadInProgress}).
		Delete(&ImageModel{})
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		// Zero rows is either "no such image" or "held by compression";
		// re-read to answer honestly rather than 404ing a live image.
		var count int64
		if err := r.db.WithContext(ctx).
			Model(&ImageModel{}).
			Where("id = ? AND account_id = ?", imageID, accountID).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return app_errors.ErrImageCompressionInProgress
		}
		return app_errors.ErrImageNotFound
	}

	return nil
}

// SetCompressionState updates only the compression status and progress.
// ClaimCompression atomically transitions the image INTO in-progress only if it
// is not already in-progress, reporting whether this caller won the claim. The
// previous flow read the status, checked it, then wrote it — a check-then-act, so
// two concurrent POST /compress both passed the check and both ran the job. Both
// write the SAME deterministic object key, and the loser's failure cleanup then
// deleted the winner's published object — with IMAGE_COMPRESS_REPLACE_ORIGINAL on
// the original was already gone too, so the image was LOST.
// The single atomic UPDATE lets exactly one caller in.
// It also refuses while an UPLOAD holds the record: the predicate
// used to name only in-progress compression, so a compress could claim in the
// middle of a multi-GB upload and go on to publish the pre-upload image under
// the post-upload record. A capture-stream claim (CaptureUploadInProgress)
// refuses it for the same reason; the two idle capture states (capturing,
// capture_failed) stay claimable as before — they have no file, so the compress
// fails its own no-file check without this predicate deciding anything.
func (r *ImageRepository) ClaimCompression(ctx context.Context, accountID, imageID uuid.UUID) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ? AND account_id = ? AND compression_status NOT IN (?)", imageID, accountID,
			[]string{image.CompressionInProgress, image.UploadInProgress, image.CaptureUploadInProgress}).
		Updates(map[string]interface{}{
			"compression_status":   image.CompressionInProgress,
			"compression_progress": 0,
			"updated_at":           time.Now(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *ImageRepository) SetCompressionState(ctx context.Context, accountID, imageID uuid.UUID, status string, progress int) error {
	result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("id = ? AND account_id = ?", imageID, accountID).
		Updates(map[string]interface{}{
			"compression_status":   status,
			"compression_progress": progress,
			"updated_at":           time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrImageNotFound
	}
	return nil
}

// ListStaleCompressions returns the records still marked as compressing, for
// the startup sweep to inspect BEFORE their status is reset. Compression is the
// only job that writes temporary objects, so this is the complete set of records
// that can own one.
func (r *ImageRepository) ListStaleCompressions(ctx context.Context) ([]image.Image, error) {
	var models []ImageModel
	if err := r.db.WithContext(ctx).
		Where("compression_status = ?", image.CompressionInProgress).
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]image.Image, 0, len(models))
	for i := range models {
		out = append(out, *models[i].ToDomain())
	}
	return out, nil
}

// ResetInProgressCompression marks stale in-progress work as failed at startup.
// A backend restart kills the background compression goroutines, and any capture
// left mid-upload will never resume (the agent streams once), so both an
// in-progress compression and an in-progress capture found at startup are stale.
func (r *ImageRepository) ResetInProgressCompression(ctx context.Context) error {
	if result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("compression_status = ?", image.CompressionInProgress).
		Updates(map[string]interface{}{
			"compression_status": image.CompressionFailed,
			"updated_at":         time.Now(),
		}); result.Error != nil {
		return result.Error
	}
	// A capture claim (CaptureUploadInProgress) is swept the same way as a
	// record still awaiting its capture: the agent streams once, so a stream a
	// restart interrupted never resumes, and failing it back to CaptureFailed is
	// what keeps the record retryable. Clearing it like a stale upload claim
	// would instead leave a record with no file and no way back into the capture
	// path.
	if result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("compression_status IN (?)",
			[]string{image.CaptureInProgress, image.CaptureUploadInProgress}).
		Updates(map[string]interface{}{
			"compression_status": image.CaptureFailed,
			"updated_at":         time.Now(),
		}); result.Error != nil {
		return result.Error
	}
	// An upload claim cannot survive the process that took it: the HTTP request
	// streaming into it is gone. Released rather than failed, because the record
	// still points at the file it had BEFORE that upload — nothing about it
	// failed, the replacement simply never arrived. Leaving these behind would
	// permanently refuse both re-upload and compression on the record.
	//
	// Through the SAME rule ReleaseUpload uses, not a blanket "": a claim taken
	// over a capture record that has no bytes yet must go back to capture_failed,
	// or the agent's retry — which only ClaimCaptureUpload can accept, and only
	// from capturing/capture_failed — is refused for good. The rule is derived
	// from the row precisely so this path, which remembers nothing about the
	// process that died, can apply it too.
	result := r.db.WithContext(ctx).
		Model(&ImageModel{}).
		Where("compression_status = ?", image.UploadInProgress).
		Updates(map[string]interface{}{
			"compression_status": releaseUploadStatus(),
			"updated_at":         time.Now(),
		})
	return result.Error
}
