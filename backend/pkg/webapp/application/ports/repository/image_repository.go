package repository

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/google/uuid"
)

// ImageRepository defines the interface for image-related database operations
type ImageRepository interface {
	// Create creates a new image
	Create(ctx context.Context, img *image.Image) error

	// Update updates an existing image
	Update(ctx context.Context, img *image.Image) error

	// UpdateMetadata writes only the four operator-editable fields. The metadata
	// PUT must use it rather than Update: Update rewrites every column from a
	// copy that may predate a concurrent ClaimUpload/ClaimCompression, and
	// writing compression_status back over a claim is how two jobs end up
	// writing the same deterministic object key.
	UpdateMetadata(ctx context.Context, accountID, imageID uuid.UUID, title, version, description, imageURL string) error


	// GetByID retrieves an image by ID within an account
	GetByID(ctx context.Context, accountID, imageID uuid.UUID) (*image.Image, error)

	// GetByIDGlobal retrieves an image by ID without an account scope, for the
	// capability-authenticated capture upload endpoint (no account context).
	GetByIDGlobal(ctx context.Context, imageID uuid.UUID) (*image.Image, error)

	// CountQuotaOccupying counts the account's images that spend a slot of the
	// per-account COUNT cap: everything except transient capture records, plus
	// in-progress captures younger than inFlight.
	//
	// The window is the whole point. Excluding in-progress captures outright
	// (inFlight = 0, the original rule) means concurrent captures
	// cannot see each other and N of them all pass a cap of 3; counting them
	// forever means one abandoned capture holds a slot until an operator
	// intervenes. Counting them while they are plausibly still streaming is
	// what avoids both.
	//
	// excludeImageID leaves one record out — a capture re-checking the cap as it
	// finalizes must not count itself.
	CountQuotaOccupying(ctx context.Context, accountID uuid.UUID, inFlight time.Duration, excludeImageID *uuid.UUID) (int64, error)

	// CreateCaptureUnderCap creates a capture record only while the account is
	// still under maxPerAccount, with the count and the insert atomic under the
	// account's advisory lock. maxPerAccount <= 0 means unlimited.
	CreateCaptureUnderCap(ctx context.Context, img *image.Image, maxPerAccount int, inFlight time.Duration) error

	// CreateUnderCap creates a user-facing image record only while the account
	// is under BOTH its count cap and the byte ceilings, with every check and
	// the insert atomic under the account's advisory lock — the same treatment
	// CreateCaptureUnderCap gives captures, because a read-then-create in the
	// service lets N concurrent creates all pass the same check. The record
	// being created holds no bytes yet, so the byte check is "is there any room
	// at all"; the upload path enforces the real ceiling. Caps <= 0 mean
	// unlimited.
	CreateUnderCap(ctx context.Context, img *image.Image, maxPerAccount int, inFlight time.Duration, maxBytesPerAccount, maxBytesInstance int64) error

	// SumBytesByAccountID totals the stored bytes the account's images occupy —
	// the current file plus any retained pre-compression original, because both
	// sit in the bucket and both are billed.
	//
	// excludeImageID, when set, leaves one record out. Re-uploading into an
	// existing record REPLACES its file, so counting the bytes it is about to
	// lose would refuse a swap that frees as much as it consumes.
	SumBytesByAccountID(ctx context.Context, accountID uuid.UUID, excludeImageID *uuid.UUID) (int64, error)

	// SumBytesAllAccounts totals stored image bytes across every account. The
	// per-account ceiling does not bound the instance when signup is
	// self-service.
	SumBytesAllAccounts(ctx context.Context) (int64, error)

	// GetAllByAccountID retrieves all images for a specific account
	GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]image.Image, error)

	// Delete deletes an image by ID within an account
	Delete(ctx context.Context, accountID, imageID uuid.UUID) error

	// ClaimCompression atomically marks the image in-progress only if it is not
	// already, reporting whether this caller won — the guard that stops two
	// concurrent compress jobs from racing and deleting each other's output.
	ClaimCompression(ctx context.Context, accountID, imageID uuid.UUID) (bool, error)

	// ClaimUpload atomically marks the record as being uploaded into, only while
	// no other job holds it, reporting whether it won. The upload half of the
	// mutual exclusion whose other half is ClaimCompression: an upload streams
	// for minutes between checking the status and writing, and a compress
	// claimed inside that gap republished the previous image's bytes under the
	// same record. Taken before the first byte is stored. Losing
	// is not an error.
	ClaimUpload(ctx context.Context, accountID, imageID uuid.UUID) (bool, error)

	// ReleaseUpload clears an upload claim for the paths that do not go on to
	// write the record themselves. A no-op unless the record still reads as
	// uploading.
	ReleaseUpload(ctx context.Context, accountID, imageID uuid.UUID) error

	// ClaimCaptureUpload atomically marks a capture record as being streamed
	// into, but only from the two states that legitimately await a capture
	// (CaptureInProgress, CaptureFailed), reporting whether this caller won.
	// The capture endpoint's capability is a stateless HMAC valid for hours, so
	// this claim — not the signature — is what makes a replayed PUT lose while
	// the legitimate stream is in flight. Global (no account scope) for the same
	// reason GetByIDGlobal is: the caller is an agent presenting a signed URL,
	// not a tenant request.
	ClaimCaptureUpload(ctx context.Context, imageID uuid.UUID) (bool, error)

	// FailAwaitingCapture moves a capture record still awaiting its stream
	// (CaptureInProgress) to CaptureFailed, reporting whether it did. Any
	// other state is left alone: a stream in flight settles its own record.
	FailAwaitingCapture(ctx context.Context, accountID, imageID uuid.UUID) (bool, error)

	// UpdateIfCaptureUploading writes the whole record only while this caller's
	// capture claim still holds it, reporting whether it won. Both the finalize
	// (status cleared) and the failure (status CaptureFailed) go through it, so
	// a claim that has been swept by a restart is never overwritten blind.
	UpdateIfCaptureUploading(ctx context.Context, img *image.Image) (bool, error)

	// SetCompressionState updates only the compression status and progress,
	// leaving the rest of the record untouched so it cannot clobber
	// concurrent metadata edits.
	SetCompressionState(ctx context.Context, accountID, imageID uuid.UUID, status string, progress int) error

	// ResetInProgressCompression marks all in-progress compressions as failed.
	// Called on startup: a restart kills the background compression goroutines,
	// so any in-progress status left in the database is stale.
	ResetInProgressCompression(ctx context.Context) error

	// ListStaleCompressions returns the records still marked as compressing, for
	// the startup sweep to inspect BEFORE their status is reset. Compression is
	// the only job that writes temporary objects, so this is the complete set of
	// records that can own one.
	ListStaleCompressions(ctx context.Context) ([]image.Image, error)
}
