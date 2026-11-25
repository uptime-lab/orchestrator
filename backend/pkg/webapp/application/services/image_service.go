package services

import (
	"context"
	"io"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_compress"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/google/uuid"
)

// ImageService defines the interface for image operations
type ImageService interface {
	Create(ctx context.Context, cmd image_create.ImageCreateCommand) (*image.Image, error)
	Update(ctx context.Context, cmd image_update.ImageUpdateCommand) (*image.Image, error)
	GetByID(ctx context.Context, query image_get.ImageGetQuery) (*image.Image, error)
	GetAllByAccountID(ctx context.Context, query image_list.ImageListQuery) ([]image.Image, error)
	Delete(ctx context.Context, accountID, imageID uuid.UUID) error
	// BeginUpload admits a multipart upload BEFORE the handler reads a byte of
	// its body: it takes an upload slot (server-wide and per account, typed
	// 429 when none is free), reserves declaredBytes of in-flight staging
	// against the byte quotas, checks the record belongs to the account
	// (ErrImageNotFound otherwise) and claims it for upload
	// (ErrImageCompressionInProgress when a job holds it). Every refusal
	// returns with nothing held. The lease must be released on every exit
	// path of the request; UploadFile against a leased record does not claim
	// again. declaredBytes is the request's Content-Length when it has one,
	// else the per-request cap, else 0.
	BeginUpload(ctx context.Context, accountID, imageID uuid.UUID, declaredBytes int64) (UploadLease, error)
	UploadFile(ctx context.Context, cmd image_upload.ImageUploadCommand, fileReader io.Reader) (*image.Image, error)
	StartCompression(ctx context.Context, cmd image_compress.ImageCompressCommand) (*image.Image, error)
	GetSignedURL(ctx context.Context, accountID, imageID uuid.UUID, expiration time.Duration) (string, error)
	// CreateCaptureImage creates a pending image record for an agent-side disk
	// capture (respecting the per-account cap).
	CreateCaptureImage(ctx context.Context, accountID uuid.UUID, title string, generalize bool, sourceNodeID uuid.UUID, sourceNodeName string) (*image.Image, error)
	// FailAwaitingCapture settles the record of a capture whose command ended
	// without streaming — refused by the agent, failed, or never delivered —
	// so it reads capture_failed (deletable, retryable) instead of capturing
	// for good. A no-op for a record in any other state.
	FailAwaitingCapture(ctx context.Context, accountID, imageID uuid.UUID) error
	// CaptureUploadURL mints the signed PUT URL the agent streams the capture to.
	CaptureUploadURL(ctx context.Context, accountID, imageID uuid.UUID, expiration time.Duration) (string, error)
	// VerifyCaptureUploadCapability checks the ?exp=&sig= capability of a
	// capture upload without touching storage, the database or the body —
	// the same check FinalizeCapture opens with. It exists so the handler can
	// establish WHO is uploading before it lifts the request-body idle
	// deadline for the stream (middleware.ReleaseBodyReadDeadline): the
	// deadline must outlast an anonymous caller and not a device with a
	// valid capability. Returns cdnsign.ErrInvalidSignature on any failure.
	VerifyCaptureUploadCapability(imageID uuid.UUID, expStr, sig string) error
	// FinalizeCapture verifies the capability signature and stores the agent's
	// streamed capture, finalizing the image record.
	FinalizeCapture(ctx context.Context, imageID uuid.UUID, expStr, sig string, body io.Reader) (*image.Image, error)
	// RemainingUploadBytes reports how many more bytes of image this account may
	// store — the tighter of its own ceiling and the instance ceiling — or
	// NoByteCeiling when neither is configured. excludeImageID leaves one record
	// out, because an upload into an existing record replaces its file.
	RemainingUploadBytes(ctx context.Context, accountID uuid.UUID, excludeImageID *uuid.UUID) (int64, error)
}

// UploadLease is what BeginUpload hands out: the admission slot, the byte
// reservation and the record's upload claim, held together for the life of
// one request.
type UploadLease interface {
	// Release returns the slot and the reservation and — unless UploadFile
	// completed against this record, whose final write already cleared it —
	// releases the upload claim on a detached context. Idempotent; call it
	// from a defer so a parse error, a cancelled request, a quota refusal and
	// a storage failure all return what they held.
	Release()
}

// NoByteCeiling is what RemainingUploadBytes returns when no image byte ceiling
// is configured. A sentinel rather than 0, because 0 legitimately means "the
// quota is full" and reading one as the other turns an unlimited on-prem install
// into one that refuses every upload.
const NoByteCeiling = int64(-1)
