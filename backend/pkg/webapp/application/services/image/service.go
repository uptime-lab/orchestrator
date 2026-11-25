package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/compression"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_compress"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

// The compression job bound lives in config.ImageConfig.MaxConcurrentCompressions
// (env IMAGE_MAX_CONCURRENT_COMPRESSIONS, default 2): jobs wait for a slot
// (in_progress, progress 0) rather than being refused, and each job is a
// two-process xz pipeline whose memory is bounded per PROCESS — see the field's
// comment for the arithmetic that sizes it against the container's memory.
// Slots are granted fairly across accounts (compressionQueue), a running job
// has a deadline (IMAGE_COMPRESSION_TIMEOUT_MINUTES), and deleting the image
// stops its job, queued or running.

// errCompressSourceReplaced ends a compression whose record stopped describing
// the file being compressed. It exists as a distinct error because the cleanup
// differs from every other failure: the source must NOT be deleted (it belongs
// to whoever replaced it) and the record must NOT be marked failed (it is
// someone else's successful upload). Internal — never returned to a caller,
// since the job is detached from the request that started it.
var errCompressSourceReplaced = errors.New("compression source was replaced")

// releaseClaimTimeout bounds the detached UPDATE that releases an upload claim.
// Short: it is one indexed write, and the request it belongs to has usually
// already ended.
const releaseClaimTimeout = 15 * time.Second

// Service handles image-related business logic
type Service struct {
	imageRepo        repository.ImageRepository
	storageClient    storage.StorageClient
	compressor       compression.Compressor
	decompressors    map[string]compression.Decompressor // keyed by extension without dot
	cfg              *config.Config
	websocketClient  websockets.WebsocketClient
	logger           *slog.Logger
	compressionSlots *compressionQueue
	// jobs holds this process's queued and running compression jobs by image
	// id, so Delete can stop one instead of refusing until it ends.
	jobsMu sync.Mutex
	jobs   map[uuid.UUID]*compressionJob
	// memory is the container-wide compression memory budget the slots do
	// not express; processWeightMB is what one xz process may
	// take and therefore what one job charges per process.
	memory          *memoryBudget
	processWeightMB int
	// quotaMu is the ONE admission/commit boundary for every upload writer in
	// this process: "read what is left — stored
	// bytes plus every in-flight reservation of both kinds — then spend it"
	// is a single step under it at all three sites: the capture reservation
	// (admitCapture), the multipart reservation (admitUpload, from
	// BeginUpload) and the conversion of an uploaded body into stored bytes
	// (commitUploadedBytes, from UploadFile). The admission tracker's own
	// mutex guards each map operation, not the read-then-spend: with only
	// captures serialized against each other, a capture and a multipart
	// upload each admitted against a state that did not include the other,
	// and a multipart commit could land after a capture had been sized
	// against the bytes it was about to store.
	// Process-local by design, like the pending-login tickets: a single
	// replica today, a database lease before a second one.
	quotaMu sync.Mutex
	// admission bounds concurrent multipart staging; see
	// upload_admission.go.
	admission *uploadAdmission
	// beforeQuotaSpend, when set, runs at each quotaSite right after a writer
	// has read what is left and right before it acts on the answer — keeps
	// its reservation, reserves a capture's cap, writes the record. Tests
	// only: it is how a regression holds two writers between their read and
	// their spend, the interval the quota boundary exists for. Production
	// never sets it.
	beforeQuotaSpend func(site quotaSite)
	// compressionTimeout, when set, replaces the configured per-job deadline,
	// which is counted in minutes. Tests only.
	compressionTimeout time.Duration
}

// quotaSite names a point where an upload writer moves from reading the
// remaining byte quota to spending it — reserving a capture's cap, reserving
// a multipart body, or converting an uploaded body into stored bytes.
type quotaSite string

const (
	quotaSiteCaptureReserve quotaSite = "capture_reserve"
	quotaSiteUploadReserve  quotaSite = "upload_reserve"
	quotaSiteUploadCommit   quotaSite = "upload_commit"
)

// pauseAt runs the test hook for site, if one is installed.
func (s *Service) pauseAt(site quotaSite) {
	if s.beforeQuotaSpend != nil {
		s.beforeQuotaSpend(site)
	}
}

// SetCompressor wires the compressor (and a logger for the background jobs)
// after construction, following the composition-root setter pattern.
func (s *Service) SetCompressor(compressor compression.Compressor, logger *slog.Logger) {
	s.compressor = compressor
	s.logger = logger.With("service", "image")
}

// SetDecompressors wires the source decompressors used to transcode
// already-compressed uploads into the deploy format, keyed by the source
// extension each one handles.
func (s *Service) SetDecompressors(decompressors ...compression.Decompressor) {
	s.decompressors = make(map[string]compression.Decompressor, len(decompressors))
	for _, d := range decompressors {
		s.decompressors[d.Extension()] = d
	}
}

// decompressorFor returns the decompressor handling the given file extension
// (with or without the leading dot), or nil.
func (s *Service) decompressorFor(ext string) compression.Decompressor {
	return s.decompressors[strings.TrimPrefix(strings.ToLower(ext), ".")]
}

// NewService creates a new image service
func NewService(imageRepo repository.ImageRepository, storageClient storage.StorageClient, cfg *config.Config) *Service {
	return &Service{
		imageRepo:        imageRepo,
		storageClient:    storageClient,
		cfg:              cfg,
		compressionSlots: newCompressionQueue(cfg.Image.EffectiveMaxConcurrentCompressions()),
		memory:           newMemoryBudget(cfg.Image.EffectiveCompressionMemoryBudgetMB()),
		processWeightMB:  cfg.Image.EffectiveXZMemLimitMB(),
		admission:        newUploadAdmission(cfg.Image.EffectiveMaxConcurrentUploads(), cfg.Image.EffectiveMaxConcurrentUploadsPerAccount(), cfg.Image.EffectiveMaxStagingBytes()),
	}
}

// NewServiceWithWebSocket creates a new image service with websocket client
func NewServiceWithWebSocket(imageRepo repository.ImageRepository, storageClient storage.StorageClient, cfg *config.Config, websocketClient websockets.WebsocketClient) *Service {
	return &Service{
		imageRepo:        imageRepo,
		storageClient:    storageClient,
		cfg:              cfg,
		websocketClient:  websocketClient,
		compressionSlots: newCompressionQueue(cfg.Image.EffectiveMaxConcurrentCompressions()),
		memory:           newMemoryBudget(cfg.Image.EffectiveCompressionMemoryBudgetMB()),
		processWeightMB:  cfg.Image.EffectiveXZMemLimitMB(),
		admission:        newUploadAdmission(cfg.Image.EffectiveMaxConcurrentUploads(), cfg.Image.EffectiveMaxConcurrentUploadsPerAccount(), cfg.Image.EffectiveMaxStagingBytes()),
	}
}

// RemainingUploadBytes returns how many bytes this account may still store —
// the tighter of what its own ceiling leaves and what the instance ceiling
// leaves — or a negative sentinel meaning "no byte ceiling applies".
//
// excludeImageID leaves one record out of the account total: an upload into an
// existing record REPLACES its file, so charging the account for bytes it is
// about to drop would refuse a same-size swap.
//
// It is deliberately NOT folded into the count cap. The count is what an
// operator picks for convenience; the bytes are what gets billed, and with three
// images of 5 GiB allowed the count says 3 while the bill says 15 GiB. Both
// exist because neither implies the other.
//
// This is a READ. Anything that acts on its answer — reserving, committing —
// does so under quotaMu, in the same step as the read (admitCapture,
// admitUpload, commitUploadedBytes); a caller outside the boundary gets an
// estimate that another writer may have spent by the time it is used.
func (s *Service) RemainingUploadBytes(ctx context.Context, accountID uuid.UUID, excludeImageID *uuid.UUID) (int64, error) {
	perAccount := s.cfg.Image.MaxBytesPerAccount
	instance := s.cfg.Image.MaxBytesInstance
	if perAccount <= 0 && instance <= 0 {
		return services.NoByteCeiling, nil
	}
	// Bytes other uploads have been ADMITTED for but not yet written count as
	// used: the stored sums see a file only once its record is written at the
	// very end, so without this N concurrent quota-sized uploads all read the
	// same remaining budget and all pass. The excluded record's
	// own reservation is left out, as its stored bytes are.
	reservedAccount, reservedGlobal := s.admission.reservedBytes(accountID, excludeImageID)
	remaining := int64(math.MaxInt64)
	if perAccount > 0 {
		used, err := s.imageRepo.SumBytesByAccountID(ctx, accountID, excludeImageID)
		if err != nil {
			return 0, fmt.Errorf("failed to total the account's stored image bytes: %w", err)
		}
		remaining = perAccount - used - reservedAccount
	}
	if instance > 0 {
		used, err := s.imageRepo.SumBytesAllAccounts(ctx)
		if err != nil {
			return 0, fmt.Errorf("failed to total the instance's stored image bytes: %w", err)
		}
		// The instance total includes the excluded record, so subtract it here
		// too — otherwise a re-upload is charged for bytes it replaces, which is
		// the same mistake the account side avoids.
		if excludeImageID != nil {
			if img, err := s.imageRepo.GetByIDGlobal(ctx, *excludeImageID); err == nil && img != nil {
				used -= storedBytesOf(img)
			}
		}
		if left := instance - used - reservedGlobal; left < remaining {
			remaining = left
		}
	}
	if remaining < 0 {
		remaining = 0
	}
	return remaining, nil
}

// storedBytesOf mirrors the SQL sum in the repository: current file plus a
// retained pre-compression original.
func storedBytesOf(img *image.Image) int64 {
	if img == nil || img.FileStorageInfo == nil {
		return 0
	}
	return img.FileStorageInfo.SizeBytes + img.FileStorageInfo.SourceSizeBytes
}

// assertByteQuota refuses when the account (or the instance) has no room left.
func (s *Service) assertByteQuota(ctx context.Context, accountID uuid.UUID, excludeImageID *uuid.UUID) error {
	remaining, err := s.RemainingUploadBytes(ctx, accountID, excludeImageID)
	if err != nil {
		return err
	}
	if remaining == services.NoByteCeiling {
		return nil
	}
	if remaining <= 0 {
		return fmt.Errorf("%w: the stored image quota is full — delete an image to free space",
			app_errors.ErrImageLimitReached)
	}
	return nil
}

// Create creates a new image for the authenticated account
func (s *Service) Create(ctx context.Context, cmd image_create.ImageCreateCommand) (*image.Image, error) {
	if strings.TrimSpace(cmd.Title) == "" {
		return nil, app_errors.InvalidRequest("invalid_image_title", errors.New("title is required"))
	}
	if err := validateImageURL(cmd.ImageURL); err != nil {
		return nil, err
	}
	newImage := &image.Image{
		ID:          cmd.ID,
		AccountID:   cmd.AuthContext.AccountID,
		Title:       cmd.Title,
		Version:     cmd.Version,
		Description: cmd.Description,
		ImageURL:    cmd.ImageURL,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Per-account count cap AND byte ceilings (disk quota), checked
	// atomically with the insert under the account's advisory lock — the same
	// treatment the capture path got, because a read-then-insert here let N
	// concurrent creates all pass the same check. The upload endpoint requires
	// an existing record, so this gates uploads too; the byte check is an early
	// "no room for any bytes at all" refusal (the record holds none yet) and the
	// upload path enforces the real ceiling.
	err := s.imageRepo.CreateUnderCap(
		ctx, newImage,
		s.cfg.Image.MaxPerAccount, s.cfg.Image.CaptureInFlightWindow(),
		s.cfg.Image.MaxBytesPerAccount, s.cfg.Image.MaxBytesInstance,
	)
	if err != nil {
		return nil, err
	}

	// Emit websocket event
	s.notifyImageCreated(*newImage)

	return newImage, nil
}

// validateImageURL accepts an empty value (no link) or an absolute http(s)
// URL with a host. The field is a link an operator follows to the image's
// source; any other scheme (javascript:, data:, file:) is never one.
func validateImageURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return app_errors.InvalidRequest("invalid_image_url", errors.New("image_url must be an absolute http or https URL"))
	}
	return nil
}

// Update updates an existing image if it belongs to the account
// Note: FileStorageInfo is NOT updated by this method - use UploadFile instead
func (s *Service) Update(ctx context.Context, cmd image_update.ImageUpdateCommand) (*image.Image, error) {
	existingImage, err := s.imageRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}

	// Blanking a title is refused; a record whose title was already empty
	// (stored before the rule) can still have its other fields edited.
	if strings.TrimSpace(cmd.Title) == "" && strings.TrimSpace(existingImage.Title) != "" {
		return nil, app_errors.InvalidRequest("invalid_image_title", errors.New("title is required"))
	}
	if cmd.ImageURL != existingImage.ImageURL {
		if err := validateImageURL(cmd.ImageURL); err != nil {
			return nil, err
		}
	}
	existingImage.Title = cmd.Title
	existingImage.Version = cmd.Version
	existingImage.Description = cmd.Description
	existingImage.ImageURL = cmd.ImageURL
	existingImage.UpdatedAt = time.Now()

	// Four columns, not the whole row. This used to go through Update
	// (`Select("*").Updates`), the only writer of `images` that is not
	// claim-conditional: a title edit whose read landed just before ClaimUpload
	// or ClaimCompression wrote compression_status="" back OVER the claim, after
	// which a concurrent compress legitimately won and both jobs wrote the same
	// deterministic object key. In the other order it wrote a stale
	// file_storage_info/checksum over a finished compression, so with
	// IMAGE_COMPRESS_REPLACE_ORIGINAL=true the record pointed at a deleted
	// object and read in_progress forever. The UI serialises update-then-upload,
	// so the UI never triggered it; a second tab, a second operator or any API
	// client does.
	err = s.imageRepo.UpdateMetadata(ctx, cmd.AuthContext.AccountID, cmd.ID,
		existingImage.Title, existingImage.Version, existingImage.Description, existingImage.ImageURL)
	if err != nil {
		return nil, err
	}

	// Emit websocket event
	s.notifyImageUpdated(*existingImage)

	return existingImage, nil
}

// GetByID retrieves an image by ID if it belongs to the account
func (s *Service) GetByID(ctx context.Context, query image_get.ImageGetQuery) (*image.Image, error) {
	img, err := s.imageRepo.GetByID(ctx, query.AuthContext.AccountID, query.ID)
	if err != nil {
		return nil, err
	}

	return img, nil
}

// GetAllByAccountID lists all images for the account
func (s *Service) GetAllByAccountID(ctx context.Context, query image_list.ImageListQuery) ([]image.Image, error) {
	images, err := s.imageRepo.GetAllByAccountID(ctx, query.AuthContext.AccountID)
	if err != nil {
		return nil, err
	}

	return images, nil
}

// Delete deletes an image if it belongs to the account, removing its stored
// files (both the current one and a retained pre-compression original).
func (s *Service) Delete(ctx context.Context, accountID, imageID uuid.UUID) error {
	// Get image before deletion for websocket event
	imageToDelete, err := s.imageRepo.GetByID(ctx, accountID, imageID)
	if err != nil {
		return err
	}

	// A compression holds the record, and the repository refuses to delete it
	// while it does (the job's store would recreate the object after the row
	// is gone). Deleting is the way to stop one, so stop it — queued or
	// running — and wait until it has settled and released the record; then
	// re-read, because a job that finished in the meantime moved the files.
	if imageToDelete.CompressionStatus == image.CompressionInProgress &&
		s.stopCompression(ctx, accountID, imageID) {
		if imageToDelete, err = s.imageRepo.GetByID(ctx, accountID, imageID); err != nil {
			return err
		}
	}

	err = s.imageRepo.Delete(ctx, accountID, imageID)
	if err != nil {
		return err
	}

	// DETACHED, and for the same reason the claim releases in this file are: the
	// row is already gone, so on the request context a client that navigates
	// away — or any transient bucket error — would leave up to two multi-GB
	// objects behind with no row pointing at them, invisible to the byte quota,
	// to SumBytesAllAccounts and to an out-of-band account purge that works from
	// rows. The compression artifacts have a startup sweep for this orphan
	// class; the ordinary delete path, the one operators use, has no sweep
	// behind it, so it must be detached itself.
	if s.storageClient != nil && imageToDelete.FileStorageInfo != nil {
		deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseClaimTimeout)
		defer cancel()
		s.deleteStoredFiles(deleteCtx, imageToDelete.FileStorageInfo.FilePath, imageToDelete.FileStorageInfo.SourceFilePath)
	}

	// Emit websocket event
	s.notifyImageDeleted(*imageToDelete)

	return nil
}

// deleteStoredFiles removes the given storage paths, best effort.
func (s *Service) deleteStoredFiles(ctx context.Context, paths ...string) {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := s.storageClient.Delete(ctx, path); err != nil {
			s.jobLogger().Warn("failed to delete stored image file", "file_path", path, "error", err)
		}
	}
}

// countingReader counts the bytes read through it so the stored size can be
// verified against the size declared in the multipart form.
type countingReader struct {
	reader io.Reader
	count  int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.count += int64(n)
	return n, err
}

// UploadFile uploads a file for an image and updates the FileStorageInfo field.
// The new file is stored before the old one is removed, so a failed upload
// never leaves the image without its previously uploaded file.
func (s *Service) UploadFile(ctx context.Context, cmd image_upload.ImageUploadCommand, fileReader io.Reader) (*image.Image, error) {
	if s.storageClient == nil {
		return nil, fmt.Errorf("storage client is not configured")
	}

	if err := cmd.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", app_errors.ErrInvalidImageFile, err)
	}

	existingImage, err := s.imageRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}

	// Claim the record BEFORE storing a single byte, and hold it for the whole
	// transfer. This replaces a plain `if status == in_progress` check that stood
	// here: it answers the same question and the same error, but atomically and
	// without a window. That check was necessary and nowhere near sufficient — a
	// multi-GB upload streams for minutes afterwards, and a compress claimed
	// inside that window worked from the record as it was BEFORE this upload —
	// it then deleted what it believed was the stale object, which is the file
	// this upload had just written, and republished the previous image's bytes
	// under this record.
	//
	// Checking again at the END cannot save it: the object key is deterministic,
	// so a re-upload of the same extension lands on exactly the key the
	// compression is reading, and by then the bytes are already replaced. The
	// only place the check does any good is before the store.
	//
	// The HTTP path takes the claim EARLIER still, through BeginUpload,
	// before the multipart body is parsed; a
	// record the admission tracker shows as leased is therefore already
	// claimed, and claiming it again here would refuse our own upload. The
	// lease then owns the release; this function only reports completion to
	// it. A direct caller with no lease (tests, a future non-HTTP path) still
	// claims and releases here, so the invariant — claimed before the first
	// byte, released on every failure — holds for both.
	lease := s.admission.lookup(cmd.AuthContext.AccountID, cmd.ID)
	uploadDone := false
	if lease == nil {
		claimed, err := s.imageRepo.ClaimUpload(ctx, cmd.AuthContext.AccountID, cmd.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to claim the image for upload: %w", err)
		}
		if !claimed {
			return nil, app_errors.ErrImageCompressionInProgress
		}
		// Released on every path that does not itself write the record (which clears
		// the status as part of the write).
		//
		// The release runs on a DETACHED context, and that is the whole point rather
		// than a precaution. The most likely way a multi-GB upload ends is the client
		// going away — a closed tab, a slept laptop, a proxy timeout — which cancels
		// the request context; the store then fails, this defer fires, and
		// `database/sql` refuses to execute an UPDATE on a cancelled context. The
		// release would silently not happen and the record would stay `uploading`
		// until the next backend restart: every retry refused, compression refused,
		// and a perpetual "uploading" badge on an image nothing is uploading. The
		// cancelled context is exactly when the release matters most.
		//
		// A claim stranded by a hard CRASH is still cleared at startup, which is
		// sound because a crash is followed by a start.
		defer func() {
			if uploadDone {
				return
			}
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseClaimTimeout)
			defer cancel()
			if relErr := s.imageRepo.ReleaseUpload(releaseCtx, cmd.AuthContext.AccountID, cmd.ID); relErr != nil && s.logger != nil {
				s.logger.Warn("failed to release the upload claim", "image_id", cmd.ID, "error", relErr)
			}
		}()
	} else {
		defer func() {
			if uploadDone {
				s.admission.markDone(lease)
			}
		}()
	}

	ext := strings.TrimPrefix(filepath.Ext(cmd.FileName), ".")
	filePath := fmt.Sprintf("%s/%s.%s", cmd.AuthContext.AccountID.String(), cmd.ID.String(), ext)

	var oldFilePath, oldSourcePath string
	if existingImage.FileStorageInfo != nil {
		oldFilePath = existingImage.FileStorageInfo.FilePath
		oldSourcePath = existingImage.FileStorageInfo.SourceFilePath
	}

	// Calculate SHA256 checksum and byte count while streaming the file
	hash := sha256.New()
	counter := &countingReader{reader: io.TeeReader(fileReader, hash)}

	// The stored type is pinned, never the client's multipart part type
	// (cmd.ContentType is kept for logging only). An S3 HeadObject hands the
	// stored type back to whatever serves the object, and a CDN handler that
	// reflected it would let a `text/html` upload named `x.img` run in the
	// application's origin. The CDN forces an inert
	// type regardless, so this is defence in depth for objects that reach a
	// browser by any other route (a provider-signed URL, a bucket listing).
	storageOptions := &storage.StorageOptions{
		ContentType: "application/octet-stream",
		Metadata: map[string]string{
			"account_id": cmd.AuthContext.AccountID.String(),
			"image_id":   cmd.ID.String(),
			"file_name":  cmd.FileName,
		},
	}

	_, err = s.storageClient.StoreWithOptions(ctx, filePath, counter, storageOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to upload file to storage: %w", err)
	}

	cleanupNewFile := func() {
		if filePath == oldFilePath {
			return
		}
		if err := s.storageClient.Delete(ctx, filePath); err != nil && s.logger != nil {
			s.logger.Warn("failed to clean up stored file after failed upload", "file_path", filePath, "error", err)
		}
	}

	if counter.count != cmd.SizeBytes {
		cleanupNewFile()
		return nil, fmt.Errorf("stored file size %d does not match declared size %d (upload truncated?)", counter.count, cmd.SizeBytes)
	}

	existingImage.SetFileStorageInfo(s.storageClient.GetStorageType(), filePath, cmd.FileName, counter.count)
	existingImage.Checksum = hex.EncodeToString(hash.Sum(nil))
	// A fresh upload supersedes any earlier compression outcome (progress 100
	// marks "already in deploy format" when the original is not retained).
	existingImage.CompressionStatus = ""
	existingImage.CompressionProgress = 0

	// Convert the body into stored bytes: re-check the byte ceiling now that
	// the real size is known and write the record as ONE step under quotaMu,
	// deleting what was just stored if it no longer fits. This write also
	// clears the claim taken above: the record carries the new file, so the
	// upload is over.
	//
	// The handler's pre-check is a plain SUM, and an upload's bytes enter that
	// sum only when the record is written here — so N concurrent uploads into
	// N records of one account all read the same remaining budget and all
	// pass it: three 5 GiB files against a 10 GiB account cap, and the same
	// across accounts for IMAGE_MAX_BYTES_INSTANCE, the ceiling that bounds
	// the operator's bucket bill. CreateUnderCap holds count and bytes under
	// the account advisory lock, but an upload targets an EXISTING record and
	// never creates.
	//
	// An unlocked re-check here bounds the overshoot to what is in flight
	// but does not remove it: a capture admitted between this read and this
	// write is sized against bytes the write is about to spend, and an
	// upload whose reservation is smaller than its body (a chunked request
	// with no per-request cap reserves 0; a direct caller reserves nothing)
	// is invisible to it until the write. Under the boundary the read and the write
	// are the same step for every writer.
	if err := s.commitUploadedBytes(ctx, existingImage, counter.count); err != nil {
		cleanupNewFile()
		return nil, err
	}
	uploadDone = true

	// The image record now points at the new file; drop the previous file
	// (and a retained pre-compression original) at other paths. Best effort.
	if oldFilePath != filePath {
		s.deleteStoredFiles(ctx, oldFilePath)
	}
	if oldSourcePath != filePath {
		s.deleteStoredFiles(ctx, oldSourcePath)
	}

	// Emit websocket event
	s.notifyImageUpdated(*existingImage)

	return existingImage, nil
}

// StartCompression validates the request, marks the image as compressing and
// launches the background raw-to-xz compression job. Returns the image with
// CompressionStatus already set to in_progress.
func (s *Service) StartCompression(ctx context.Context, cmd image_compress.ImageCompressCommand) (*image.Image, error) {
	if s.storageClient == nil {
		return nil, fmt.Errorf("storage client is not configured")
	}
	if s.compressor == nil {
		return nil, fmt.Errorf("%w: compression is not available on this server", app_errors.ErrInvalidImageFile)
	}
	if err := cmd.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", app_errors.ErrInvalidImageFile, err)
	}

	img, err := s.imageRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}
	if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath == "" {
		return nil, fmt.Errorf("%w: no file uploaded", app_errors.ErrInvalidImageFile)
	}
	ext := strings.ToLower(filepath.Ext(img.FileStorageInfo.FilePath))
	if isCompressedExt(ext) {
		// A file we already produced is in its final form; anything else
		// compressed is transcodable — that includes same-extension repacks
		// (single-block official .xz → multi-block .xz that devices decode in
		// parallel). Our output is recognized by the retained source OR, when
		// replace_original_on_compress dropped the original, by the completed
		// progress marker (a fresh upload resets it).
		if img.FileStorageInfo.SourceFilePath != "" ||
			(img.CompressionStatus == "" && img.CompressionProgress == 100) {
			return nil, fmt.Errorf("%w: file is already in the deploy format (%s)", app_errors.ErrInvalidImageFile, ext)
		}
		if s.decompressorFor(ext) == nil {
			return nil, fmt.Errorf("%w: transcoding %s is not available on this server", app_errors.ErrInvalidImageFile, ext)
		}
	}
	// Atomic claim, NOT a read-then-write: two concurrent compress requests both
	// passing a status check and both running the job is what let the loser's
	// cleanup delete the winner's published object. Exactly one caller
	// wins the UPDATE; the rest get ErrImageCompressionInProgress and never spawn a
	// job, so nothing else can reach the shared object key.
	won, err := s.imageRepo.ClaimCompression(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to claim compression: %w", err)
	}
	if !won {
		return nil, app_errors.ErrImageCompressionInProgress
	}

	// Re-read AFTER winning the claim, and work from that. Everything above was
	// read before the claim existed, so an upload that landed in between left
	// this copy naming a file that is no longer the record's — and compressing
	// it would publish the old image under the new record. From
	// here on the claim keeps the record still: ClaimUpload refuses an
	// upload while the status is in-progress.
	claimed, err := s.imageRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		// The claim is recorded but no job will run, so release it rather than
		// leaving the image stuck in-progress until the next restart.
		s.setCompressionState(ctx, cmd.AuthContext.AccountID, cmd.ID, image.CompressionFailed, 0)
		return nil, fmt.Errorf("failed to re-read image after claiming compression: %w", err)
	}
	if claimed.FileStorageInfo == nil || claimed.FileStorageInfo.FilePath == "" {
		s.setCompressionState(ctx, cmd.AuthContext.AccountID, cmd.ID, image.CompressionFailed, 0)
		return nil, fmt.Errorf("%w: no file uploaded", app_errors.ErrInvalidImageFile)
	}
	img = claimed
	img.CompressionStatus = image.CompressionInProgress
	img.CompressionProgress = 0
	s.notifyImageUpdated(*img)

	// Compressing a multi-GB image takes minutes, so it runs detached from
	// the HTTP request. Statuses stranded by a restart are reset to failed
	// on startup (ResetInProgressCompression).
	//
	// The checksum travels with it as the identity of what is being compressed:
	// the swap refuses to publish over a record that no longer describes it.
	go s.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, img.FileStorageInfo.FilePath, img.FileStorageInfo.FileName, img.FileStorageInfo.SizeBytes, img.Checksum)

	return img, nil
}

// admitCapture computes the stream cap from what the account has left and
// reserves it, under quotaMu so no other writer — a capture, a multipart
// admission, a multipart commit — can read the same remainder in between.
// nil reservation means no byte ceiling applies.
func (s *Service) admitCapture(ctx context.Context, img *image.Image, maxBytes int64) (*uploadReservation, int64, error) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	remaining, err := s.RemainingUploadBytes(ctx, img.AccountID, &img.ID)
	if err != nil {
		return nil, 0, err
	}
	if remaining == services.NoByteCeiling {
		return nil, maxBytes, nil
	}
	if remaining <= 0 {
		return nil, 0, fmt.Errorf("%w: the stored image quota is full — delete an image to free space",
			app_errors.ErrImageLimitReached)
	}
	if maxBytes <= 0 || remaining < maxBytes {
		maxBytes = remaining
	}
	s.pauseAt(quotaSiteCaptureReserve)
	return s.admission.reserveCapture(img.AccountID, img.ID, maxBytes), maxBytes, nil
}

// commitUploadedBytes writes an uploaded image record — the moment its bytes
// become STORED usage — after checking that they still fit, both under
// quotaMu. The record is excluded from its own total: its previous file is
// being replaced, and its own in-flight reservation (when the HTTP path took
// one) describes the body being committed. Holding the boundary across the
// single-row write is the point: a writer that read between the check and
// the write would otherwise see neither the reservation (which may be
// smaller than the body, or absent) nor the stored bytes.
func (s *Service) commitUploadedBytes(ctx context.Context, img *image.Image, count int64) error {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	remaining, err := s.RemainingUploadBytes(ctx, img.AccountID, &img.ID)
	if err != nil {
		return err
	}
	if remaining != services.NoByteCeiling && count > remaining {
		return &ImageQuotaExceededError{Needed: count, Remaining: remaining}
	}
	s.pauseAt(quotaSiteUploadCommit)
	if err := s.imageRepo.Update(ctx, img); err != nil {
		return fmt.Errorf("failed to update image with file storage info: %w", err)
	}
	return nil
}

// admitCompressionOutput reserves what a compression's output may store: what
// the account and the instance have left once the record's own bytes are set
// aside and the retained original is charged, no more than bound. It returns
// the reservation and the cap to enforce on the output stream; nil and 0 mean
// no byte ceiling applies.
func (s *Service) admitCompressionOutput(ctx context.Context, accountID, imageID uuid.UUID, retained, bound int64) (*uploadReservation, int64, error) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	remaining, err := s.RemainingUploadBytes(ctx, accountID, &imageID)
	if err != nil {
		return nil, 0, err
	}
	if remaining == services.NoByteCeiling {
		return nil, 0, nil
	}
	available := remaining - retained
	if available <= 0 {
		return nil, 0, &ImageQuotaExceededError{Remaining: max(remaining, 0)}
	}
	if bound > 0 && bound < available {
		available = bound
	}
	// The capture map is the right shelf: it holds bytes a stream may come to
	// store without an upload slot or staging bytes, keyed by a record whose
	// claim makes it the only writer.
	return s.admission.reserveCapture(accountID, imageID, available), available, nil
}

// commitCompressedBytes writes the swapped record — the moment the output
// becomes stored usage — after checking under quotaMu that the record's new
// total still fits. The record is excluded from the sum it is checked
// against, and so is its own reservation.
func (s *Service) commitCompressedBytes(ctx context.Context, img *image.Image, total int64) error {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	remaining, err := s.RemainingUploadBytes(ctx, img.AccountID, &img.ID)
	if err != nil {
		return err
	}
	if remaining != services.NoByteCeiling && total > remaining {
		return &ImageQuotaExceededError{Needed: total, Remaining: remaining}
	}
	if err := s.imageRepo.Update(ctx, img); err != nil {
		return fmt.Errorf("failed to update image record: %w", err)
	}
	return nil
}

// compressionWeightMB is what one job charges the memory budget: one process
// limit for a raw source, two for a compressed one that is transcoded.
func (s *Service) compressionWeightMB(rawPath string) int {
	if isCompressedExt(filepath.Ext(rawPath)) {
		return 2 * s.processWeightMB
	}
	return s.processWeightMB
}

// runCompression executes one compression job and records the outcome.
func (s *Service) runCompression(ctx context.Context, accountID, imageID uuid.UUID, rawPath, rawFileName string, rawSize int64, rawChecksum string) {
	log := s.jobLogger().With("image_id", imageID, "raw_path", rawPath)

	// jobCtx is what Delete cancels; ctx stays live for the cleanup and the
	// final status write, which must happen however the job ends. The job is
	// unregistered only after that write, so a Delete waiting on it reads the
	// settled record.
	jobCtx, cancelJob := context.WithCancel(ctx)
	job := s.registerCompressionJob(accountID, imageID, cancelJob)
	defer s.finishCompressionJob(imageID, job)
	stopped := func(err error) {
		log.Warn("image compression stopped before it started", "reason", err)
		s.setCompressionState(ctx, accountID, imageID, image.CompressionFailed, 0)
	}

	// Bound how many all-core compression pipelines run at once; the claim is
	// already recorded, so a queued job waits here as in_progress/0% rather
	// than being refused. Released on every exit path below.
	releaseSlot, err := s.compressionSlots.acquire(jobCtx, accountID)
	if err != nil {
		stopped(err)
		return
	}
	defer releaseSlot()
	// Then the memory budget, for the job's WHOLE weight: two process limits
	// for a transcode (decoder + encoder run together, piped), one for a
	// plain compression. Every codec is charged the xz limit — zstd's use is
	// smaller, and the point is a ceiling the container can survive, not an
	// accounting of actual bytes.
	weight := s.compressionWeightMB(rawPath)
	if err := s.memory.acquire(jobCtx, weight); err != nil {
		stopped(err)
		return
	}
	defer s.memory.release(weight)

	// The deadline starts once the job holds its slot and its memory: waiting
	// behind other jobs is not this job's cost.
	timeout := s.cfg.Image.CompressionTimeout()
	if s.compressionTimeout > 0 {
		timeout = s.compressionTimeout
	}
	workCtx, cancelWork := context.WithTimeout(jobCtx, timeout)
	defer cancelWork()

	compressedPath := fmt.Sprintf("%s/%s.%s", accountID.String(), imageID.String(), s.compressor.Extension())
	fileName := rawFileName
	if fileName == "" {
		fileName = filepath.Base(rawPath)
	}
	// Transcoded sources swap their compression extension ("x.img.xz" →
	// "x.img.zst"); raw sources gain one ("x.img" → "x.img.zst").
	if isCompressedExt(filepath.Ext(fileName)) {
		fileName = strings.TrimSuffix(fileName, filepath.Ext(fileName))
	}
	fileName += "." + s.compressor.Extension()

	// Files uploaded before size tracking existed have no recorded size;
	// ask the storage for it so progress can still be computed.
	if rawSize <= 0 {
		if info, err := s.storageClient.GetFileInfo(ctx, rawPath); err == nil && info != nil {
			rawSize = info.Size
		}
	}

	if err := s.compressAndSwap(workCtx, accountID, imageID, rawPath, compressedPath, fileName, rawSize, rawChecksum); err != nil {
		// Name the cause a cancelled context hides behind a codec or storage
		// error, so the log says why the image is marked failed.
		// A stop the operator asked for (deleting the image) is the job doing
		// what it was told, not a failure: it is logged at WARN so ERROR keeps
		// meaning the compression itself broke.
		switch {
		case errors.Is(workCtx.Err(), context.DeadlineExceeded) && jobCtx.Err() == nil:
			err = fmt.Errorf("compression ran past its %s deadline (IMAGE_COMPRESSION_TIMEOUT_MINUTES): %w", timeout, err)
			log.Error("image compression failed", "error", err)
		case jobCtx.Err() != nil:
			err = fmt.Errorf("compression stopped because the image is being deleted: %w", err)
			log.Warn("image compression stopped", "error", err)
		default:
			log.Error("image compression failed", "error", err)
		}
		// For same-path repacks the atomic store never replaced the original,
		// so deleting the "partial output" would destroy it. The exception is
		// the row vanishing mid-job (an out-of-band account purge — the API delete
		// is refused while we hold the claim): the store already recreated the object
		// AFTER the purge removed it, there is no record left to reach it, and
		// the "original" the guard protects belongs to nobody — so the output
		// must go even on a same-path repack.
		rowGone := errors.Is(err, app_errors.ErrImageNotFound)
		if errors.Is(err, errCompressSourceReplaced) {
			// The record now describes somebody else's successful upload. Take
			// away only our own output, and only where it cannot be the file
			// that record points at — deleting that would destroy the upload
			// this branch exists to protect. Leave the status alone: the upload
			// already set it, and writing "failed" here would report a failure
			// of a compression of an image that is no longer there.
			s.cleanupReplacedCompressionOutput(ctx, log, accountID, imageID, compressedPath)
			log.Warn("image changed while compression was running; output discarded and record left untouched",
				"compressed_path", compressedPath)
			return
		}
		if compressedPath != rawPath || rowGone {
			if delErr := s.storageClient.Delete(ctx, compressedPath); delErr != nil {
				log.Warn("failed to clean up partial compressed file", "error", delErr)
			}
		}
		if rowGone {
			log.Warn("image row deleted while compression was running; recreated object removed", "compressed_path", compressedPath)
			return
		}
		s.setCompressionState(ctx, accountID, imageID, image.CompressionFailed, 0)
		return
	}
	log.Info("image compression finished", "compressed_path", compressedPath)
}

// repackOriginalPath is the name a same-extension repack stages the untouched
// original under, so it can be read while the output overwrites the source key.
// Derived, never stored — which is exactly why the startup sweep can find one
// left behind by a restart.
func repackOriginalPath(filePath string) string {
	ext := filepath.Ext(filePath)
	return strings.TrimSuffix(filePath, ext) + ".orig" + ext
}

// SweepStaleCompressionArtifacts removes the temporary objects a compression
// leaves behind when the process dies mid-job, and must run BEFORE the statuses
// are reset — the in-progress status is the only thing that identifies which
// records could own one.
//
// The orphan is the size of a disk image and nothing will ever name it again:
// both temporary names are derived at runtime and appear on no row, so neither
// the per-account byte quota nor an out-of-band account purge that works from
// rows can see it. A customer can delete their image and
// still be paying for a full copy of it.
//
// This DELETES objects at startup, so it is deliberately narrow. It considers
// only records the database says were mid-compression; it only ever names the
// two paths a compression itself derives; and it refuses to touch a path the
// record actually references. A missed orphan costs disk. A wrong delete costs
// the customer their image, so every skip is logged rather than assumed.
func (s *Service) SweepStaleCompressionArtifacts(ctx context.Context) error {
	if s.storageClient == nil || s.compressor == nil {
		// Without a compressor this build never produced these objects, and
		// without storage there is nothing to delete from.
		return nil
	}
	stale, err := s.imageRepo.ListStaleCompressions(ctx)
	if err != nil {
		return fmt.Errorf("failed to list stale compressions: %w", err)
	}
	log := s.jobLogger()
	for i := range stale {
		img := stale[i]
		if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath == "" {
			continue
		}
		referenced := map[string]bool{
			img.FileStorageInfo.FilePath:       true,
			img.FileStorageInfo.SourceFilePath: true,
		}
		candidates := []string{
			// The half-written output of the job that died.
			fmt.Sprintf("%s/%s.%s", img.AccountID, img.ID, s.compressor.Extension()),
			// The staged original of a same-extension repack — the copy whose
			// deferred cleanup the restart skipped.
			repackOriginalPath(img.FileStorageInfo.FilePath),
		}
		for _, path := range candidates {
			if path == "" || referenced[path] {
				log.Info("keeping a compression artifact the record still references",
					"image_id", img.ID, "path", path)
				continue
			}
			if err := s.storageClient.Delete(ctx, path); err != nil {
				// Almost always "not there", which is the normal case: only a
				// same-extension repack stages an original at all. Logged at
				// debug so a real failure is still findable without making every
				// boot noisy.
				log.Debug("no stale compression artifact to remove",
					"image_id", img.ID, "path", path, "error", err)
				continue
			}
			log.Warn("removed a compression artifact orphaned by a restart",
				"image_id", img.ID, "account_id", img.AccountID, "path", path)
		}
	}
	return nil
}

// cleanupReplacedCompressionOutput removes a compressed object produced for a
// record that has since moved on, but only once it is certain the object is not
// what that record now serves.
//
// The deterministic object key is the whole hazard: the compressed output and a
// re-upload of the same extension land on the SAME key, so "delete what I wrote"
// and "delete the customer's current image" can be the same call.
func (s *Service) cleanupReplacedCompressionOutput(ctx context.Context, log *slog.Logger, accountID, imageID uuid.UUID, compressedPath string) {
	current, err := s.imageRepo.GetByID(ctx, accountID, imageID)
	if err != nil {
		// Cannot prove the object is unreferenced, so keep it. An orphan costs
		// disk; deleting a live image costs the image.
		log.Warn("could not re-read image to clean up discarded compression output; leaving it in place",
			"compressed_path", compressedPath, "error", err)
		return
	}
	if current.FileStorageInfo != nil &&
		(current.FileStorageInfo.FilePath == compressedPath ||
			current.FileStorageInfo.SourceFilePath == compressedPath) {
		log.Warn("discarded compression output is the record's current file; leaving it in place",
			"compressed_path", compressedPath)
		return
	}
	if delErr := s.storageClient.Delete(ctx, compressedPath); delErr != nil {
		log.Warn("failed to clean up discarded compressed file", "error", delErr)
	}
}

// cappedReader fails the read once more than `limit` bytes have come through.
//
// Deliberately not io.LimitReader: that returns EOF at the cap, which the
// compressor would read as a clean end of stream and happily publish a silently
// truncated image. A decompression bomb has to be an error, not a short read.
//
// A stream of exactly `limit` bytes passes: at the cap it probes for one more
// byte and fails only if there is one.
type cappedReader struct {
	r         io.Reader
	remaining int64
	limit     int64
	// exceeded is the error past the cap; nil means the decompression cap's.
	exceeded error
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		var probe [1]byte
		if n, err := c.r.Read(probe[:]); n == 0 {
			return 0, err
		}
		if c.exceeded != nil {
			return 0, c.exceeded
		}
		return 0, fmt.Errorf("decompressed image exceeds the %d byte limit; refusing to continue (set IMAGE_MAX_DECOMPRESSED_SIZE if this image is genuinely this large)", c.limit)
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}

// isCompressedExt reports whether the extension is a known compressed format.
func isCompressedExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".xz", ".zst":
		return true
	}
	return false
}

// compressAndSwap streams the source file through the compressor (via the
// decompressor first for already-compressed sources) into storage, then
// atomically repoints the image record. The source file is kept in storage
// as a safety copy and recorded in FileStorageInfo.
func (s *Service) compressAndSwap(ctx context.Context, accountID, imageID uuid.UUID, rawPath, compressedPath, fileName string, rawSize int64, rawChecksum string) (retErr error) {
	// Same-extension repack (e.g. single-block official .xz → multi-block
	// .xz): the output replaces the source path, so preserve the original
	// under a distinct name first and read from that copy.
	sourcePath := rawPath
	swapped := false
	// stored marks the moment a same-path repack's output has replaced the
	// original at its key. From then until the swap the staged copy is the
	// only copy of the bytes the record's checksum and size describe.
	stored := false
	if compressedPath == rawPath {
		// Derived in ONE place: the startup sweep has to name this object to
		// clean it up after a restart, and a second copy of the
		// rule is how a sweep comes to look for a file nobody writes.
		origPath := repackOriginalPath(rawPath)
		if err := s.copyStoredFile(ctx, rawPath, origPath); err != nil {
			return fmt.Errorf("failed to preserve original before repack: %w", err)
		}
		sourcePath = origPath
		defer func() {
			if swapped {
				return
			}
			// Detached: a job stopped by its deadline or by a delete still
			// owes this cleanup, and the staged copy is named on no row.
			detached := context.WithoutCancel(ctx)
			// A failure after the output replaced the original — the reload,
			// the commit-time quota re-check, the deadline — leaves the record
			// describing bytes no longer at its key. Put the original back
			// before dropping the staged copy, or every install of the image
			// fails its digest check. Not when the record itself moved on or
			// vanished: then the original describes nothing anyone serves.
			if stored && !errors.Is(retErr, errCompressSourceReplaced) && !errors.Is(retErr, app_errors.ErrImageNotFound) {
				if err := s.copyStoredFile(detached, origPath, rawPath); err != nil {
					// Keep the staged copy: it is the only one left, and an
					// orphan costs disk where a delete would cost the image.
					s.jobLogger().Error("failed to restore the original after a repack failed; the original is kept at its staged name",
						"image_id", imageID, "path", rawPath, "staged_path", origPath, "error", err)
					return
				}
			}
			s.deleteStoredFiles(detached, origPath)
		}()
	}

	// The output is stored bytes like any upload's, so it is admitted like
	// one: what the account and the instance have left is reserved under
	// quotaMu before the first byte, the stream is cut at that reservation,
	// and the swap re-checks under quotaMu before the record takes the bytes.
	// A small archive that expands into more than the quota allows fails
	// here instead of landing in storage. A retained original stays charged,
	// so it is subtracted from what the output may take.
	replaceOriginal := s.cfg.Image.ReplaceOriginalOnCompress
	retained := rawSize
	if replaceOriginal {
		retained = 0
	}
	transcode := isCompressedExt(filepath.Ext(rawPath))
	outputBound := rawSize
	if transcode {
		outputBound = s.cfg.Image.DecompressionLimit()
	}
	// Room for codec framing: neither xz nor zstd grows incompressible input
	// by more than a fraction of a percent. A source of unknown size gives no
	// bound; the quota still does.
	if outputBound > 0 {
		outputBound += outputBound/64 + 1<<20
	}
	reservation, outputCap, err := s.admitCompressionOutput(ctx, accountID, imageID, retained, outputBound)
	if err != nil {
		return err
	}
	if reservation != nil {
		defer s.admission.releaseCapture(reservation)
	}

	src, err := s.storageClient.Retrieve(ctx, sourcePath)
	if err != nil {
		return fmt.Errorf("failed to open source image: %w", err)
	}
	defer src.Close()

	// The compressed output size is unknown upfront, the input size is not;
	// see compressionProgress for how the two sides become a percentage and
	// how often it is written and pushed.
	progress := newCompressionProgress(rawSize, transcode, func(percent int) {
		s.setCompressionState(ctx, accountID, imageID, image.CompressionInProgress, percent)
	})
	progressSrc := &progressCounter{reader: src, add: progress.addSource}

	// A compressed source is transcoded: decompress into the pipeline
	// before recompressing to the deploy format.
	var compressInput io.Reader = progressSrc
	var decompressDone chan error
	var decompressReader *io.PipeReader
	if srcExt := filepath.Ext(rawPath); transcode {
		decompressor := s.decompressorFor(srcExt)
		if decompressor == nil {
			return fmt.Errorf("no decompressor configured for %s", srcExt)
		}
		var decompressWriter *io.PipeWriter
		decompressReader, decompressWriter = io.Pipe()
		decompressDone = make(chan error, 1)
		go func() {
			decompressErr := decompressor.Decompress(ctx, decompressWriter, progressSrc)
			decompressWriter.CloseWithError(decompressErr)
			decompressDone <- decompressErr
		}()
		compressInput = decompressReader

		// Bound the expansion. IMAGE_MAX_UPLOAD_SIZE limits the artifact, not
		// how far it unpacks, so a crafted archive could otherwise fill the
		// disk from a tiny upload. This must ERROR rather than stop short: an
		// io.LimitReader would quietly truncate and we would publish a corrupt
		// image that looks like a successful compress.
		if limit := s.cfg.Image.DecompressionLimit(); limit > 0 {
			compressInput = &cappedReader{r: compressInput, remaining: limit, limit: limit}
		}
		compressInput = &progressCounter{reader: compressInput, add: progress.addExpanded}
	}

	// The compressor writes, storage reads: connect them with a pipe and
	// hash/count the compressed stream on the way through.
	pipeReader, pipeWriter := io.Pipe()
	compressDone := make(chan error, 1)
	go func() {
		compressErr := s.compressor.Compress(ctx, pipeWriter, compressInput)
		pipeWriter.CloseWithError(compressErr)
		compressDone <- compressErr
	}()

	var output io.Reader = pipeReader
	if outputCap > 0 {
		output = &cappedReader{r: pipeReader, remaining: outputCap, limit: outputCap,
			exceeded: fmt.Errorf("%w: the compressed image would exceed the %d bytes the stored image quota has left — delete an image to free space",
				app_errors.ErrImageLimitReached, outputCap)}
	}
	hash := sha256.New()
	counter := &countingReader{reader: io.TeeReader(output, hash)}
	_, err = s.storageClient.StoreWithOptions(ctx, compressedPath, counter, &storage.StorageOptions{
		ContentType: s.compressor.ContentType(),
		Metadata: map[string]string{
			"account_id": accountID.String(),
			"image_id":   imageID.String(),
			"file_name":  fileName,
		},
	})
	if err != nil {
		// The store stopped reading mid-stream (e.g. disk full). Unblock the
		// compressor (and the decompressor upstream) — they are parked on a
		// write to a pipe nobody is draining, so without closing the read ends
		// the goroutines leak and the drains below hang forever.
		pipeReader.CloseWithError(err)
		<-compressDone
		if decompressReader != nil {
			decompressReader.CloseWithError(err)
			<-decompressDone
		}
		return fmt.Errorf("failed to store compressed image: %w", err)
	}
	stored = true
	if compressErr := <-compressDone; compressErr != nil {
		return fmt.Errorf("compression failed: %w", compressErr)
	}
	if decompressDone != nil {
		if decompressErr := <-decompressDone; decompressErr != nil {
			return fmt.Errorf("source decompression failed: %w", decompressErr)
		}
	}

	img, err := s.imageRepo.GetByID(ctx, accountID, imageID)
	if err != nil {
		return fmt.Errorf("failed to reload image: %w", err)
	}
	// Refuse to publish over a record that no longer describes what was
	// compressed. With ClaimUpload holding uploads off while the claim is
	// ours this should be unreachable, which is the right relationship between a
	// guard and an invariant: the cost of being wrong here is the customer's
	// image replaced by a different one, advertised under this record's checksum
	// and size. The checksum is the discriminator, not the path —
	// a re-upload of the same extension writes to the SAME key, so the path
	// alone cannot tell one image from another.
	if img.FileStorageInfo == nil ||
		img.FileStorageInfo.FilePath != rawPath ||
		(rawChecksum != "" && img.Checksum != rawChecksum) {
		return fmt.Errorf("%w: the image file changed while it was being compressed", errCompressSourceReplaced)
	}
	// By default the source file stays in storage as a safety copy and the
	// record remembers where it lives. With replace_original_on_compress the
	// record carries no source (progress 100 marks the deploy format) and the
	// original is deleted after the record swap — an install pays for one
	// copy per image, at the cost of not being able to re-compress it.
	recordedSource, recordedSourceSize := sourcePath, rawSize
	if replaceOriginal {
		recordedSource, recordedSourceSize = "", 0
	}
	img.SetCompressedFileStorageInfo(s.storageClient.GetStorageType(), compressedPath, fileName, counter.count, recordedSource, recordedSourceSize)
	img.Checksum = hex.EncodeToString(hash.Sum(nil))
	img.CompressionStatus = ""
	img.CompressionProgress = 100
	if err := s.commitCompressedBytes(ctx, img, counter.count+recordedSourceSize); err != nil {
		return err
	}
	swapped = true

	if replaceOriginal && sourcePath != compressedPath {
		// Record already points at the compressed file; a failed delete only
		// leaves an orphaned file behind (logged), never a broken record.
		s.deleteStoredFiles(context.WithoutCancel(ctx), sourcePath)
	}

	s.notifyImageUpdated(*img)
	return nil
}

// copyStoredFile duplicates a stored object via streaming.
func (s *Service) copyStoredFile(ctx context.Context, from, to string) error {
	src, err := s.storageClient.Retrieve(ctx, from)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = s.storageClient.StoreWithOptions(ctx, to, src, nil)
	return err
}

// setCompressionState persists the compression status/progress and pushes the
// refreshed image over the websocket, best effort.
func (s *Service) setCompressionState(ctx context.Context, accountID, imageID uuid.UUID, status string, progress int) {
	if err := s.imageRepo.SetCompressionState(ctx, accountID, imageID, status, progress); err != nil {
		s.jobLogger().Warn("failed to persist compression state", "image_id", imageID, "error", err)
		return
	}
	img, err := s.imageRepo.GetByID(ctx, accountID, imageID)
	if err != nil {
		s.jobLogger().Warn("failed to reload image after state update", "image_id", imageID, "error", err)
		return
	}
	s.notifyImageUpdated(*img)
}

// jobLogger returns the configured logger or a safe default for background jobs.
func (s *Service) jobLogger() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// GetSignedURL generates a signed URL for downloading an image file
// For local storage (internal CDN), returns a public CDN URL without signature
// For other storage types, returns a signed URL
func (s *Service) GetSignedURL(ctx context.Context, accountID, imageID uuid.UUID, expiration time.Duration) (string, error) {
	if s.storageClient == nil {
		return "", fmt.Errorf("storage client is not configured")
	}

	img, err := s.imageRepo.GetByID(ctx, accountID, imageID)
	if err != nil {
		return "", err
	}

	if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath == "" {
		return "", fmt.Errorf("image file not uploaded")
	}

	storageType := s.storageClient.GetStorageType()

	// Backends the platform's own CDN proxies get a signed CDN URL. S3 is one of
	// them deliberately: a presigned bucket URL would work, but it moves the
	// transfer onto metered bucket egress and changes the URL every client sees.
	if storage.ServedThroughCDN(storageType) {
		// Use server advertisement URL if configured
		baseURL := s.cfg.Server.AdvertisementURL
		if baseURL == "" {
			// Fallback: construct from host and port (use http by default)
			// Note: In production, advertisement_url should be configured with proper scheme
			if s.cfg.Server.Host == "0.0.0.0" || s.cfg.Server.Host == "127.0.0.1" {
				baseURL = fmt.Sprintf("http://localhost:%d", s.cfg.Server.Port)
			} else {
				baseURL = fmt.Sprintf("http://%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
			}
		} else {
			// Ensure advertisement URL has a scheme (http:// or https://)
			if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
				// If no scheme, default to http
				baseURL = fmt.Sprintf("http://%s", baseURL)
			}
		}

		// Remove leading slash from file path if present
		cleanPath := strings.TrimPrefix(img.FileStorageInfo.FilePath, "/")
		// Construct CDN URL: {baseURL}/cdn/{filePath}
		cdnURL := fmt.Sprintf("%s/cdn/%s", strings.TrimSuffix(baseURL, "/"), cleanPath)
		// Sign the URL with an expiry so the CDN path is a time-bounded
		// capability rather than a permanent public link. The CDN handler
		// verifies this signature before serving the file.
		signingKey := cdnsign.DeriveKey(s.cfg.CDNSigningSecret())
		signedURL, err := cdnsign.AppendSignature(cdnURL, cleanPath, signingKey, expiration)
		if err != nil {
			return "", fmt.Errorf("failed to sign CDN URL: %w", err)
		}
		return signedURL, nil
	}

	// For other storage types (GCP, S3, etc.), use signed URL
	signedURL, err := s.storageClient.GetSignedURL(ctx, img.FileStorageInfo.FilePath, expiration)
	if err != nil {
		return "", fmt.Errorf("failed to generate signed URL: %w", err)
	}

	return signedURL, nil
}

// --- Agent-side disk capture (the reverse of install_os) ---

// captureUploadName is the cdnsign subject bound to a capture upload URL.
func captureUploadName(imageID uuid.UUID) string {
	return "capture/" + imageID.String()
}

// captureStoragePath is where a captured image lands (always the zstd deploy
// format the agent streams).
func captureStoragePath(accountID, imageID uuid.UUID) string {
	return fmt.Sprintf("%s/%s.zst", accountID.String(), imageID.String())
}

// publicBaseURL returns the externally reachable base URL the agent uses to
// reach the platform, mirroring GetSignedURL's derivation.
func (s *Service) publicBaseURL() string {
	baseURL := s.cfg.Server.AdvertisementURL
	if baseURL == "" {
		if s.cfg.Server.Host == "0.0.0.0" || s.cfg.Server.Host == "127.0.0.1" {
			baseURL = fmt.Sprintf("http://localhost:%d", s.cfg.Server.Port)
		} else {
			baseURL = fmt.Sprintf("http://%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
		}
	} else if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = fmt.Sprintf("http://%s", baseURL)
	}
	return strings.TrimSuffix(baseURL, "/")
}

// CreateCaptureImage creates a pending image record for an agent-side capture.
// It enforces the per-account image cap, so a capture cannot exceed the quota
// an upload would. The source node is recorded on the image so restores can
// detect cross-node deployments of backup images.
func (s *Service) CreateCaptureImage(ctx context.Context, accountID uuid.UUID, title string, generalize bool, sourceNodeID uuid.UUID, sourceNodeName string) (*image.Image, error) {
	if err := s.assertByteQuota(ctx, accountID, nil); err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		title = "captured-image"
	}
	img := image.NewCaptureImage(uuid.New(), accountID, title, generalize, sourceNodeID, sourceNodeName)
	// The count cap is enforced in the repository, not here: the check and the
	// insert have to be one atomic step under the account's lock or a playbook
	// that captures N nodes at once has every record pass the same check before
	// any of them exists. The byte quota above stays a plain read —
	// it is a pre-flight courtesy, and FinalizeCapture is where the byte ceiling
	// is actually enforced, against a stream with no Content-Length to pre-check.
	if err := s.imageRepo.CreateCaptureUnderCap(
		ctx, img, s.cfg.Image.MaxPerAccount, s.cfg.Image.CaptureInFlightWindow()); err != nil {
		return nil, err
	}
	s.notifyImageCreated(*img)
	return img, nil
}

// FailAwaitingCapture settles a capture record whose command ended before any
// byte was streamed. Before it, only the stream's own failure and the startup
// sweep moved a record out of `capturing`, so a capture the agent refused (or
// a command that failed or was never delivered) left a record reading
// "capturing" until the next backend restart. On a detached context: the
// settle usually runs from the command's terminal path, whose context belongs
// to a stream or a request that may already be gone.
func (s *Service) FailAwaitingCapture(ctx context.Context, accountID, imageID uuid.UUID) error {
	detached := context.WithoutCancel(ctx)
	failed, err := s.imageRepo.FailAwaitingCapture(detached, accountID, imageID)
	if err != nil {
		return fmt.Errorf("failed to settle the capture record: %w", err)
	}
	if !failed {
		return nil
	}
	if img, gerr := s.imageRepo.GetByID(detached, accountID, imageID); gerr == nil && img != nil {
		s.notifyImageUpdated(*img)
	}
	return nil
}

// CaptureUploadURL mints the HMAC-signed, expiring PUT URL the agent streams a
// captured image to. The signature over the image id is the capability; no
// node/account token is required (nodes never hold one on HTTP).
func (s *Service) CaptureUploadURL(ctx context.Context, accountID, imageID uuid.UUID, expiration time.Duration) (string, error) {
	if s.storageClient == nil {
		return "", fmt.Errorf("storage client is not configured")
	}
	// Confirm the record exists and belongs to the account before minting.
	if _, err := s.imageRepo.GetByID(ctx, accountID, imageID); err != nil {
		return "", err
	}
	name := captureUploadName(imageID)
	rawURL := fmt.Sprintf("%s/%s", s.publicBaseURL(), name)
	signingKey := cdnsign.DeriveKey(s.cfg.CDNSigningSecret())
	signedURL, err := cdnsign.AppendSignature(rawURL, name, signingKey, expiration)
	if err != nil {
		return "", fmt.Errorf("failed to sign capture URL: %w", err)
	}
	return signedURL, nil
}

// VerifyCaptureUploadCapability is the stateless half of FinalizeCapture's
// authentication: the HMAC over the capture name, bound to its expiry. No
// storage, no database, no body — so the handler can run it BEFORE it decides
// how long it is willing to wait for the body.
func (s *Service) VerifyCaptureUploadCapability(imageID uuid.UUID, expStr, sig string) error {
	signingKey := cdnsign.DeriveKey(s.cfg.CDNSigningSecret())
	return cdnsign.Verify(captureUploadName(imageID), expStr, sig, signingKey)
}

// FinalizeCapture verifies the capability signature, streams the agent's
// uploaded bytes into image storage under a byte cap, and finalizes the image
// record (checksum, size, deploy-format marker). It runs without an account
// context — the signature over the image id is the authorization.
//
// It re-verifies the capability even though the handler already did through
// VerifyCaptureUploadCapability: this is the service's own gate, and a second
// caller must not be able to skip it by not being the handler.
func (s *Service) FinalizeCapture(ctx context.Context, imageID uuid.UUID, expStr, sig string, body io.Reader) (*image.Image, error) {
	if s.storageClient == nil {
		return nil, fmt.Errorf("storage client is not configured")
	}
	if err := s.VerifyCaptureUploadCapability(imageID, expStr, sig); err != nil {
		return nil, err
	}

	img, err := s.imageRepo.GetByIDGlobal(ctx, imageID)
	if err != nil {
		return nil, err
	}

	// Single-use, enforced as an atomic claim rather than a status check: only a
	// record still awaiting its capture (or one whose prior attempt failed and
	// is being retried) may be claimed, and only ONE caller wins it. The
	// capability is a stateless HMAC valid for hours, so without the claim a
	// replayed PUT raced the legitimate stream through a check-then-act — both
	// writing the same object key, the last finalize stamping its checksum over
	// the other's bytes. A finalized image (empty status) is unclaimable, so a
	// leaked URL replayed within its window still cannot swap the stored bytes
	// of an already-captured image.
	won, err := s.imageRepo.ClaimCaptureUpload(ctx, img.ID)
	if err != nil {
		return nil, err
	}
	if !won {
		return nil, fmt.Errorf("%w: image is not awaiting a capture upload", app_errors.ErrInvalidImageFile)
	}
	// From here the claim is ours and every exit must settle it: failCapture on
	// failure (back to CaptureFailed, retryable), the conditional finalize below
	// on success.

	filePath := captureStoragePath(img.AccountID, img.ID)
	fileName := fmt.Sprintf("%s.zst", img.ID.String())

	// Count backstop. The create-time check under the account's lock is the
	// enforcement; this catches the one case it cannot, because no
	// lock can be held across a multi-GB stream: a capture that outlived the
	// in-flight window while other images were created and finalized in the
	// meantime. Refusing here rather than after the transfer costs the operator
	// nothing they were going to keep — the record cannot become an image
	// either way — and it excludes ITSELF from the count, since this row is the
	// one asking.
	if s.cfg.Image.MaxPerAccount > 0 {
		count, cerr := s.imageRepo.CountQuotaOccupying(
			ctx, img.AccountID, s.cfg.Image.CaptureInFlightWindow(), &img.ID)
		if cerr != nil {
			s.failCapture(ctx, img, filePath)
			return nil, fmt.Errorf("failed to count account images: %w", cerr)
		}
		if count >= int64(s.cfg.Image.MaxPerAccount) {
			s.failCapture(ctx, img, filePath)
			return nil, fmt.Errorf("%w (max %d)", app_errors.ErrImageLimitReached, s.cfg.Image.MaxPerAccount)
		}
	}

	// Cap the streamed bytes: MaxUploadSize wins, else the storage cap.
	maxBytes := s.cfg.Image.MaxUploadSize
	if maxBytes <= 0 {
		maxBytes = s.cfg.Storage.MaxFileSize
	}
	// ...and never more than the account has left. A capture streams straight
	// from the device with no Content-Length the platform can pre-check, so this
	// limit IS the enforcement — without it the per-image cap would let a single
	// capture put an account arbitrarily far past its byte ceiling, once per
	// image slot. The record already exists, so it is excluded from its own
	// total (a re-captured image replaces its bytes).
	// Read what is left and reserve it as ONE step: the
	// stored sums see this capture only once its record is written at the
	// very end, so without the reservation two overlapping captures each
	// read the whole remaining quota, each fit alone, and together stored
	// twice it — and without the lock around read-plus-reserve they still
	// could, by both reading before either reserved. Held in the same
	// tracker the multipart admission uses, so a multipart upload admitted
	// meanwhile sees these bytes as used too, and released on every exit.
	// The cap on the stream is what makes the reservation honest: the
	// capture cannot store more than it reserved.
	reservation, maxBytes, err := s.admitCapture(ctx, img, maxBytes)
	if err != nil {
		s.failCapture(ctx, img, filePath)
		return nil, err
	}
	if reservation != nil {
		defer s.admission.releaseCapture(reservation)
	}
	hash := sha256.New()
	reader := io.Reader(io.TeeReader(body, hash))
	counter := &countingReader{reader: reader}
	var capped io.Reader = counter
	if maxBytes > 0 {
		// +1 so hitting exactly the cap is fine but exceeding it errors.
		capped = io.LimitReader(counter, maxBytes+1)
	}

	storageOptions := &storage.StorageOptions{
		ContentType: "application/zstd",
		Metadata: map[string]string{
			"account_id": img.AccountID.String(),
			"image_id":   img.ID.String(),
			"file_name":  fileName,
			"source":     "capture",
		},
	}
	if _, err := s.storageClient.StoreWithOptions(ctx, filePath, capped, storageOptions); err != nil {
		s.failCapture(ctx, img, filePath)
		return nil, fmt.Errorf("failed to store captured image: %w", err)
	}
	if maxBytes > 0 && counter.count > maxBytes {
		s.failCapture(ctx, img, filePath)
		return nil, fmt.Errorf("%w (max %d bytes)", app_errors.ErrImageLimitReached, maxBytes)
	}
	if counter.count == 0 {
		s.failCapture(ctx, img, filePath)
		return nil, fmt.Errorf("%w: empty capture upload", app_errors.ErrInvalidImageFile)
	}

	img.SetFileStorageInfo(s.storageClient.GetStorageType(), filePath, fileName, counter.count)
	img.Checksum = hex.EncodeToString(hash.Sum(nil))
	// A captured .zst is already the deploy format: empty status + progress 100
	// with no retained source is how the rest of the system recognizes it.
	img.CompressionStatus = ""
	img.CompressionProgress = 100
	// Conditional on OUR claim still holding the record: this write carries the
	// WHOLE record from a copy read before the bytes streamed, so it must never
	// land on a record somebody else owns. With the claim taken above, the only
	// way to lose it is a restart's sweep — and a stream that somehow outlived a
	// restart must not finalize a record the sweep already failed.
	finalized, err := s.imageRepo.UpdateIfCaptureUploading(ctx, img)
	if err != nil {
		// Don't leave the stored bytes orphaned + the record stuck claimed.
		s.failCapture(ctx, img, filePath)
		return nil, fmt.Errorf("failed to finalize captured image: %w", err)
	}
	if !finalized {
		// The claim is no longer ours. Drop only OUR bytes and leave the record
		// entirely alone — writing anything would be a statement about somebody
		// else's state.
		s.deleteStoredFiles(context.WithoutCancel(ctx), filePath)
		return nil, app_errors.ErrImageCompressionInProgress
	}
	s.notifyImageUpdated(*img)
	return img, nil
}

// failCapture settles a failed capture: drops the partial upload and returns
// the record from the stream claim to CaptureFailed, which is what keeps it
// retryable.
//
// On a DETACHED context, deliberately: the likeliest way a multi-GB capture
// stream ends is the agent vanishing, which cancels the request context — and a
// release that dies with the request leaves the claim held until the next
// restart, refusing the retry the failure exists to allow (the same trap the
// upload claim documents in UploadFile).
func (s *Service) failCapture(ctx context.Context, img *image.Image, filePath string) {
	detached := context.WithoutCancel(ctx)
	s.deleteStoredFiles(detached, filePath)
	// Clear what the object USED to be, on the copy about to be written. One
	// caller reaches here after SetFileStorageInfo has already run — the
	// UpdateIfCaptureUploading error branch — and without this the row landed in
	// capture_failed carrying size_bytes for an object this function had just
	// deleted. SumBytesByAccountID deliberately does not exclude failed
	// captures and the startup reset rewrites only compression_status, so
	// nothing ever cleared it: the account was charged forever for bytes that do
	// not exist. The other two callers run before SetFileStorageInfo and are
	// unaffected by clearing fields that are already empty.
	img.FileStorageInfo = nil
	img.Checksum = ""
	img.CompressionStatus = image.CaptureFailed
	// Conditional on our claim, like the success path: a failed capture must
	// not stamp `capture_failed` over a record it no longer owns.
	if won, err := s.imageRepo.UpdateIfCaptureUploading(detached, img); err == nil && won {
		s.notifyImageUpdated(*img)
	}
}

// notifyImageCreated sends an image created event via websocket
func (s *Service) notifyImageCreated(img image.Image) {
	if s.websocketClient == nil {
		return
	}
	imageResult := results.NewImageResult(&img)
	err := s.websocketClient.Send(websockets3.NewAccountMessage(img.AccountID, authz.ActionImageRead, websockets3.MessageWrapper{
		Type:    "image_created",
		Payload: imageResult,
	}))
	if err != nil && s.logger != nil {
		s.logger.Warn("failed to send image created websocket event", "image_id", img.ID, "error", err)
	}
}

// notifyImageUpdated sends an image updated event via websocket
func (s *Service) notifyImageUpdated(img image.Image) {
	if s.websocketClient == nil {
		return
	}
	imageResult := results.NewImageResult(&img)
	err := s.websocketClient.Send(websockets3.NewAccountMessage(img.AccountID, authz.ActionImageRead, websockets3.MessageWrapper{
		Type:    "image_updated",
		Payload: imageResult,
	}))
	if err != nil && s.logger != nil {
		s.logger.Warn("failed to send image updated websocket event", "image_id", img.ID, "error", err)
	}
}

// notifyImageDeleted sends an image deleted event via websocket
func (s *Service) notifyImageDeleted(img image.Image) {
	if s.websocketClient == nil {
		return
	}
	imageResult := results.NewImageResult(&img)
	err := s.websocketClient.Send(websockets3.NewAccountMessage(img.AccountID, authz.ActionImageRead, websockets3.MessageWrapper{
		Type:    "image_deleted",
		Payload: imageResult,
	}))
	if err != nil && s.logger != nil {
		s.logger.Warn("failed to send image deleted websocket event", "image_id", img.ID, "error", err)
	}
}
