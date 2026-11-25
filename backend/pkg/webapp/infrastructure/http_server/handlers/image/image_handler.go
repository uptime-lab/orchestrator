package image

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_compress"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/middleware"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// ImageHandler handles image-related HTTP requests
type ImageHandler struct {
	imageService services.ImageService
	audit        *audit_service.Recorder
	cfg          *config.Config
	logger       *slog.Logger
}

// NewImageHandler creates a new image handler.
//
// The audit recorder is required rather than a setter, following the rule in
// application/services/audit: a handler that can hand out a whole-disk image
// must not be constructible without the thing that records that it did.
func NewImageHandler(imageService services.ImageService, auditRecorder *audit_service.Recorder, cfg *config.Config, logger *slog.Logger) *ImageHandler {
	if auditRecorder == nil {
		panic("image handler requires an audit recorder")
	}
	return &ImageHandler{
		imageService: imageService,
		audit:        auditRecorder,
		cfg:          cfg,
		logger:       logger.With("handler", "image"),
	}
}

// RegisterRoutes registers image routes
func (h *ImageHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc, enforcer *authz_service.Enforcer) {
	images := e.Group("/api/v1/images")
	images.Use(authMiddleware)

	// OS images are account-global: read for any member, manage for super_admin.
	// ActionImageRead is granted to every role down to viewer, so enforcing it
	// changes nothing for an account-wide binding — but it is what makes the
	// read answerable to an OU-scoped one, which resolves an account-level
	// action only through an account-wide binding. Enforcing it now costs
	// nothing (there are no OU-scoped bindings yet); adding it after the first
	// one exists would silently take these reads away from that member.
	read := middleware.RequireAction(enforcer, authz.ActionImageRead)
	manage := middleware.RequireAction(enforcer, authz.ActionImageManage)
	images.POST("", h.Create, manage)
	images.GET("/:id", h.Get, read)
	images.GET("", h.List, read)
	images.PUT("/:id", h.Update, manage)
	images.DELETE("/:id", h.Delete, manage)
	images.POST("/:id/upload", h.UploadFile, manage)
	images.POST("/:id/compress", h.Compress, manage)
	// Download sits on MANAGE, not read, and the split is the point. Reading an
	// image means seeing that it exists; downloading it means walking out with
	// the whole disk — and for a captured image that is every file, key and
	// credential the node held when the snapshot was taken. ActionImageRead is
	// granted down to RoleViewer, so putting the download there would let the
	// lowest role in the account exfiltrate its most sensitive single object.
	images.GET("/:id/download", h.Download, manage)
}

// RegisterCaptureRoutes registers the public, signature-authenticated capture
// upload endpoint at the top level (like /cdn), so a managed node's agent can
// stream a captured image straight to the platform without a JWT. The capability
// is the HMAC signature over the image id in the query string.
//
// Being outside jwtMiddleware, the route escapes the per-account limiter, so it
// carries its own per-IP one (same envelope as /adopt/exchange: sustained
// 1 req/2s, burst 10). The signature is 256-bit HMAC so guessing is hopeless;
// the limiter keeps signature probing from being free load. A legitimate agent
// does ONE PUT per capture and a capture takes minutes, so even a whole NAT'd
// site fits inside the burst.
func (h *ImageHandler) RegisterCaptureRoutes(e *echo.Echo) {
	limiter := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: rate.Limit(0.5), Burst: 10, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return echo.NewHTTPError(http.StatusTooManyRequests, "rate limit exceeded")
		},
	})
	e.PUT("/capture/:id", h.CaptureUpload, limiter)
}

// CaptureUpload receives a streamed captured disk image from an agent and
// finalizes the pending image record. Authenticated by the ?exp=&sig= capability
// minted by CaptureUploadURL — never a user JWT (nodes hold none over HTTP).
// @Summary Upload a captured disk image (agent, signed capability)
// @Description Streams a zstd-compressed disk image captured on a node into image storage and finalizes the pending image record. Authenticated by the HMAC signature in the query string, not a JWT.
// @Tags images
// @Accept application/octet-stream
// @Produce json
// @Param id path string true "Image ID" format(uuid)
// @Param exp query string true "Capability expiry (unix seconds)"
// @Param sig query string true "Capability HMAC signature"
// @Success 200 {object} results.APIResponse{data=results.ImageResult} "Capture stored"
// @Failure 403 {object} results.APIResponse "Invalid or expired signature"
// @Failure 404 {object} results.APIResponse "Image not found"
// @Failure 413 {object} results.APIResponse "Capture too large"
// @Failure 429 {object} results.APIResponse "Per-IP rate limit exceeded"
// @Router /capture/{id} [put]
func (h *ImageHandler) CaptureUpload(c echo.Context) error {
	imageIDStr := c.Param("id")
	imageID, err := uuid.Parse(imageIDStr)
	if err != nil {
		return results.SendValidationError(c, "invalid image ID")
	}
	exp := c.QueryParam("exp")
	sig := c.QueryParam("sig")

	// Establish WHO is uploading before deciding how long to wait for the
	// body. The request-body idle deadline is armed for every request that
	// carries a body and is what stops an anonymous client
	// from parking a connection here; a device with a valid capability may
	// legitimately go quiet between blocks for longer than that bound, so the
	// deadline is released for it — and only for it, which is why the
	// capability is verified first and the release sits AFTER the refusal.
	// FinalizeCapture verifies again as the service's own gate.
	if err := h.imageService.VerifyCaptureUploadCapability(imageID, exp, sig); err != nil {
		return results.SendForbiddenError(c, "invalid or expired signature")
	}
	middleware.ReleaseBodyReadDeadline(c)

	// No upload slot here, deliberately: this body is not multipart and is
	// never staged — FinalizeCapture streams it straight into storage — so
	// the TMPDIR exhaustion that BeginUpload bounds for the browser upload
	// has no equivalent on this path. What bounds captures
	// is the per-account image count with its in-flight window and the
	// byte quotas FinalizeCapture re-checks after the stream.
	req := c.Request()
	defer req.Body.Close()
	img, err := h.imageService.FinalizeCapture(req.Context(), imageID, exp, sig, req.Body)
	if err != nil {
		switch {
		case errors.Is(err, cdnsign.ErrInvalidSignature):
			return results.SendForbiddenError(c, "invalid or expired signature")
		case errors.Is(err, app_errors.ErrImageNotFound):
			return results.SendNotFoundError(c, "image not found")
		case errors.Is(err, app_errors.ErrImageLimitReached):
			return results.SendError(c, http.StatusRequestEntityTooLarge, err.Error(), nil)
		case errors.Is(err, app_errors.ErrInvalidImageFile):
			return results.SendValidationError(c, err.Error())
		default:
			results.LogFailure(c.Request().Context(), h.logger, "failed to finalize capture", err, "image_id", imageID)
			return results.SendInternalServerError(c, "failed to store capture", err)
		}
	}
	return results.SendOKResponse(c, results.NewImageResult(img), "capture stored successfully")
}

// Create creates a new image
// @Summary Create image
// @Description Creates a new image
// @Tags images
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param image body image_create.ImageCreateCommand true "Image creation data"
// @Success 201 {object} results.APIResponse{data=results.ImageResult} "Image created successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 409 {object} results.APIResponse "Image limit reached for this account"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/images [post]
func (h *ImageHandler) Create(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	var cmd image_create.ImageCreateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context and generate ID
	cmd.ID = uuid.New()
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	img, err := h.imageService.Create(c.Request().Context(), cmd)
	if err != nil {
		if errors.Is(err, app_errors.ErrImageLimitReached) {
			return results.SendError(c, http.StatusConflict, err.Error(), err)
		}
		results.LogFailure(c.Request().Context(), h.logger, "failed to create image", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to create image", err)
	}

	imageResult := results.NewImageResult(img)
	return results.SendCreatedResponse(c, imageResult, "image created successfully")
}

// Get retrieves an image by ID
// @Summary Get image by ID
// @Description Retrieves image details by ID
// @Tags images
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Image ID"
// @Success 200 {object} results.APIResponse{data=results.ImageResult} "Image retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Image not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/images/{id} [get]
func (h *ImageHandler) Get(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse image ID from URL parameter
	imageIDStr := c.Param("id")
	imageID, err := uuid.Parse(imageIDStr)
	if err != nil {
		h.logger.Warn("invalid image ID", "error", err, "image_id", imageIDStr)
		return results.SendValidationError(c, "invalid image ID")
	}

	query := image_get.ImageGetQuery{
		ID:          imageID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	img, err := h.imageService.GetByID(c.Request().Context(), query)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to get image", err, "image_id", imageID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrImageNotFound) {
			return results.SendNotFoundError(c, "image not found")
		}
		return results.SendInternalServerError(c, "failed to get image", err)
	}

	imageResult := results.NewImageResult(img)
	return results.SendOKResponse(c, imageResult, "image retrieved successfully")
}

// List retrieves all images for the account
// @Summary List images
// @Description Retrieves a list of all images for the authenticated account, optionally filtered by kind (os = uploaded install images, golden = generalized captures, backup = byte-exact node captures)
// @Tags images
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param kind query string false "Filter by image kind" Enums(os, golden, backup)
// @Success 200 {object} results.APIResponse{data=results.ImageListResult} "Images retrieved successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/images [get]
func (h *ImageHandler) List(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	query := image_list.ImageListQuery{
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	// Optional kind filter (Operations tabs: Install Images / Backups / Gold
	// Images). An empty stored kind is a legacy uploaded image and counts as os.
	kindFilter := c.QueryParam("kind")
	switch kindFilter {
	case "", image.KindOS, image.KindGolden, image.KindBackup:
	default:
		return results.SendValidationError(c, "invalid kind: must be os, golden or backup")
	}

	images, err := h.imageService.GetAllByAccountID(c.Request().Context(), query)
	if err != nil {
		h.logger.Error("failed to list images", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to list images", err)
	}

	// Convert to results
	imageResults := make([]results.ImageResult, 0, len(images))
	for i, img := range images {
		if kindFilter != "" {
			kind := img.Kind
			if kind == "" {
				kind = image.KindOS
			}
			if kind != kindFilter {
				continue
			}
		}
		imageResults = append(imageResults, results.NewImageResult(&images[i]))
	}

	listResult := results.NewImageListResult(imageResults)
	return results.SendOKResponse(c, listResult, "images retrieved successfully")
}

// Update updates an existing image
// @Summary Update image
// @Description Updates image information
// @Tags images
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Image ID"
// @Param image body image_update.ImageUpdateCommand true "Image update data"
// @Success 200 {object} results.APIResponse{data=results.ImageResult} "Image updated successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Image not found"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/images/{id} [put]
func (h *ImageHandler) Update(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse image ID from URL parameter
	imageIDStr := c.Param("id")
	imageID, err := uuid.Parse(imageIDStr)
	if err != nil {
		h.logger.Warn("invalid image ID", "error", err, "image_id", imageIDStr)
		return results.SendValidationError(c, "invalid image ID")
	}

	var cmd image_update.ImageUpdateCommand
	if err := c.Bind(&cmd); err != nil {
		h.logger.Error("failed to bind request", "error", err)
		return results.SendValidationError(c, "invalid request")
	}

	// Set auth context and image ID
	cmd.ID = imageID
	cmd.AuthContext = commands.NewAuthContext(accountID, userID)

	img, err := h.imageService.Update(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to update image", err, "image_id", imageID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrImageNotFound) {
			return results.SendNotFoundError(c, "image not found")
		}
		return results.SendInternalServerError(c, "failed to update image", err)
	}

	imageResult := results.NewImageResult(img)
	return results.SendOKResponse(c, imageResult, "image updated successfully")
}

// Delete deletes an image
// @Summary Delete image
// @Description Deletes an image by ID
// @Tags images
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Image ID"
// @Success 204 "No Content"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Image not found"
// @Failure 409 {object} results.APIResponse "Compression in progress"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/images/{id} [delete]
func (h *ImageHandler) Delete(c echo.Context) error {
	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	// Parse image ID from URL parameter
	imageIDStr := c.Param("id")
	imageID, err := uuid.Parse(imageIDStr)
	if err != nil {
		h.logger.Warn("invalid image ID", "error", err, "image_id", imageIDStr)
		return results.SendValidationError(c, "invalid image ID")
	}

	err = h.imageService.Delete(c.Request().Context(), accountID, imageID)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to delete image", err, "image_id", imageID, "account_id", accountID)
		switch {
		case errors.Is(err, app_errors.ErrImageNotFound):
			return results.SendNotFoundError(c, "image not found")
		case errors.Is(err, app_errors.ErrImageCompressionInProgress):
			return results.SendError(c, http.StatusConflict, "compression is in progress; retry when it finishes", err)
		default:
			return results.SendInternalServerError(c, "failed to delete image", err)
		}
	}

	return results.SendNoContentResponse(c)
}

// UploadFile uploads a file for an image
// @Summary Upload file for image
// @Description Uploads a file for an OS image (allowed extensions: .img, .iso, .xz, .zst). The file is stored atomically and the FileStorageInfo field (path, original name, size) plus the SHA256 checksum are updated. Re-uploading replaces the old file only after the new one is stored. Two limits apply: a per-image size cap, and the account's remaining storage quota — a file within the per-image cap is still refused when the account has less room left than the file needs (413), and an account or instance with no room left at all is refused at admission, before the body is read, as 409 image_quota — the same code the post-body accounting answers when the real byte count no longer fits. Re-uploading into this record does not count its current file against the quota, since the upload replaces it.
// @Tags images
// @Accept multipart/form-data
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Image ID" format(uuid)
// @Param file formData file true "Image file to upload"
// @Success 200 {object} results.APIResponse{data=results.ImageResult} "File uploaded successfully"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Image not found (checked before the body is read)"
// @Failure 409 {object} results.APIResponse "The image is busy: an upload or a compression is already in progress; or no storage room left (code image_quota) — delete an image to free space"
// @Failure 413 {object} results.FileTooLargeResult "File too large"
// @Failure 429 {object} results.APIResponse "No upload slot free (code upload_capacity): the server or this account is at its concurrent-upload bound — wait and retry"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/images/{id}/upload [post]
// sendFileTooLarge answers 413 with the effective limit (the response shape
// the frontend's upload error handling already reads — see
// results.FileTooLargeResult for why the limit sits beside the envelope).
func (h *ImageHandler) sendFileTooLarge(c echo.Context, size, maxSize int64) error {
	h.logger.Warn("image upload too large", "size", size, "max_size", maxSize)
	return results.SendFileTooLarge(c, maxSize)
}

func (h *ImageHandler) UploadFile(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	imageIDStr := c.Param("id")
	imageID, err := uuid.Parse(imageIDStr)
	if err != nil {
		h.logger.Warn("invalid image ID", "error", err, "image_id", imageIDStr)
		return results.SendValidationError(c, "invalid image ID")
	}

	// Effective cap = the tighter of the global storage ceiling and the
	// per-image limit. Reject on the request Content-Length FIRST — the
	// multipart form is otherwise fully received (gigabytes) before any size
	// check runs. The small slack covers multipart framing overhead.
	maxSize := h.cfg.Storage.MaxFileSize
	if h.cfg.Image.MaxUploadSize > 0 && (maxSize == 0 || h.cfg.Image.MaxUploadSize < maxSize) {
		maxSize = h.cfg.Image.MaxUploadSize
	}

	// Admission BEFORE a byte of the body is read: an upload
	// slot (typed 429 when the server or this account is at its bound), a
	// reservation of the staging bytes this request may come to hold, the
	// account-scoped lookup (a foreign or missing record is a 404 here, not
	// after gigabytes were spilled to TMPDIR) and the record's upload claim.
	// Admitting only after c.FormFile had parsed the whole form would leave
	// the arrival-rate limiter as the only bound on concurrent staging. The
	// reservation is the declared Content-Length
	// when the request carries one; a chunked request reserves the
	// per-request cap, since that is what it may hold.
	declared := c.Request().ContentLength
	if declared <= 0 {
		declared = maxSize
	}
	lease, err := h.imageService.BeginUpload(c.Request().Context(), accountID, imageID, declared)
	if err != nil {
		switch {
		case errors.Is(err, app_errors.ErrImageNotFound):
			return results.SendNotFoundError(c, "image not found")
		case errors.Is(err, app_errors.ErrImageCompressionInProgress):
			return results.SendError(c, http.StatusConflict, "this image is busy — an upload or a compression is already in progress", err)
		default:
			// A typed refusal (UploadCapacityError, 429) answers as itself here.
			h.logger.Warn("upload not admitted", "error", err, "image_id", imageID, "account_id", accountID)
			return results.SendInternalServerError(c, "failed to start the upload", err)
		}
	}
	// Every exit below returns what the lease holds: the slot, the byte
	// reservation and — unless the upload's final write cleared it — the claim.
	defer lease.Release()

	// The caller is authenticated (JWT), authorized (image.manage) by the
	// route middleware and now ADMITTED, so the request-body idle deadline
	// has done its job — it is what keeps an anonymous POST with a
	// declared-and-withheld body from parking a connection —
	// and a multi-gigabyte upload from a browser may now pace itself however
	// it likes. The byte cap below still applies; this lifts only the bound
	// on TIME, and only for a request that holds a slot.
	middleware.ReleaseBodyReadDeadline(c)

	// ...and tighter still by what the account's byte quota leaves. The
	// per-image cap and the per-account ceiling answer different questions —
	// three images of 5 GiB is 15 GiB against a 10 GiB account — so the third
	// upload must be refused by size even though every individual file is legal.
	// This record is excluded from its own total because an upload here REPLACES
	// its file.
	remaining, err := h.imageService.RemainingUploadBytes(c.Request().Context(), accountID, &imageID)
	if err != nil {
		h.logger.Error("failed to compute the remaining image quota", "error", err, "account_id", accountID)
		return results.SendInternalServerError(c, "failed to check the image quota", err)
	}
	if remaining != services.NoByteCeiling {
		if remaining <= 0 {
			return results.SendValidationError(c,
				"the stored image quota is full — delete an image to free space")
		}
		if maxSize == 0 || remaining < maxSize {
			maxSize = remaining
		}
	}
	if maxSize > 0 && c.Request().ContentLength > maxSize+(1<<20) {
		return h.sendFileTooLarge(c, c.Request().ContentLength, maxSize)
	}

	// The Content-Length check above is advisory only: a chunked request has
	// none, and the multipart form would otherwise be fully received before
	// file.Size is ever compared. MaxBytesReader is the hard cap —
	// it kills the read mid-body, so oversized chunked uploads die after at
	// most maxSize bytes of temp data, not after gigabytes.
	if maxSize > 0 {
		c.Request().Body = http.MaxBytesReader(c.Response().Writer, c.Request().Body, maxSize+(1<<20))
	}

	file, err := c.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return h.sendFileTooLarge(c, tooLarge.Limit, maxSize)
		}
		h.logger.Error("failed to get file from form", "error", err)
		return results.SendValidationError(c, "file is required")
	}

	// Remove the multipart spill when this handler returns, rather than relying
	// on net/http to do it when the response finishes. An image body is
	// gigabytes and TMPDIR is on the storage volume, so a spill that outlives
	// its request costs real image capacity — a multi-gigabyte orphan can
	// outlive its successful upload by many minutes with nothing holding it
	// open. Registered before the defer that closes the reader below
	// so it runs last (LIFO), and nil-guarded because FormFile can populate the
	// header from memory without ever parsing a form onto disk.
	defer func() {
		if form := c.Request().MultipartForm; form != nil {
			if err := form.RemoveAll(); err != nil {
				h.logger.Warn("failed to remove multipart spill", "error", err, "image_id", imageID)
			}
		}
	}()

	if maxSize > 0 && file.Size > maxSize {
		return h.sendFileTooLarge(c, file.Size, maxSize)
	}

	// Recorded for the log line only. The stored object's type is pinned by
	// the service to application/octet-stream, never taken from the part
	// header: a client-chosen `text/html` on an object the CDN serves from
	// the application's origin would render next to the session tokens.
	contentType := file.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	cmd := image_upload.ImageUploadCommand{
		ID:          imageID,
		FileName:    file.Filename,
		ContentType: contentType,
		SizeBytes:   file.Size,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	if err := cmd.Validate(); err != nil {
		h.logger.Warn("upload validation failed", "error", err, "image_id", imageID, "file_name", file.Filename)
		return results.SendValidationError(c, err.Error())
	}

	src, err := file.Open()
	if err != nil {
		h.logger.Error("failed to open file", "error", err)
		return results.SendInternalServerError(c, "failed to open file", err)
	}
	defer src.Close()

	img, err := h.imageService.UploadFile(c.Request().Context(), cmd, src)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to upload file", err, "image_id", imageID, "account_id", accountID)
		if errors.Is(err, app_errors.ErrImageNotFound) {
			return results.SendNotFoundError(c, "image not found")
		}
		if errors.Is(err, app_errors.ErrInvalidImageFile) {
			return results.SendValidationError(c, err.Error())
		}
		return results.SendInternalServerError(c, "failed to upload file", err)
	}

	imageResult := results.NewImageResult(img)
	return results.SendOKResponse(c, imageResult, "file uploaded successfully")
}

// Compress starts server-side compression of a raw image file
// @Summary Compress image file
// @Description Starts background compression of an uploaded image file to the deploy format — multi-block xz (zstd fallback). Raw files (.img/.iso) are compressed; .zst uploads and single-block .xz uploads are repacked (multi-block xz decompresses in parallel on devices). By default the original file is KEPT in storage and referenced via file_storage_info.source_file_path; with image.replace_original_on_compress the original is deleted after a successful compression instead. Files already in the deploy format are final. Progress is reported in compression_status/compression_progress.
// @Tags images
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Image ID" format(uuid)
// @Success 202 {object} results.APIResponse{data=results.ImageResult} "Compression started"
// @Failure 400 {object} results.APIResponse "Validation error (no file, already compressed, xz unavailable)"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Image not found"
// @Failure 409 {object} results.APIResponse "Compression already in progress"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/images/{id}/compress [post]
func (h *ImageHandler) Compress(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	imageIDStr := c.Param("id")
	imageID, err := uuid.Parse(imageIDStr)
	if err != nil {
		h.logger.Warn("invalid image ID", "error", err, "image_id", imageIDStr)
		return results.SendValidationError(c, "invalid image ID")
	}

	cmd := image_compress.ImageCompressCommand{
		ID:          imageID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	}

	img, err := h.imageService.StartCompression(c.Request().Context(), cmd)
	if err != nil {
		results.LogFailure(c.Request().Context(), h.logger, "failed to start image compression", err, "image_id", imageID, "account_id", accountID)
		switch {
		case errors.Is(err, app_errors.ErrImageNotFound):
			return results.SendNotFoundError(c, "image not found")
		case errors.Is(err, app_errors.ErrInvalidImageFile):
			return results.SendValidationError(c, err.Error())
		case errors.Is(err, app_errors.ErrImageCompressionInProgress):
			return results.SendError(c, http.StatusConflict, err.Error(), err)
		default:
			return results.SendInternalServerError(c, "failed to start compression", err)
		}
	}

	imageResult := results.NewImageResult(img)
	return results.SendSuccess(c, http.StatusAccepted, imageResult, "compression started")
}

// downloadURLTTL bounds the signed URL handed to a caller who wants their image
// back.
//
// It covers the gap between minting the URL and the transfer STARTING, not the
// transfer itself: /cdn verifies the signature once, at request start, and then
// streams for as long as the copy takes. Fifteen minutes is therefore generous
// for its actual job while keeping a leaked link short-lived — and the
// alternative reading, "make it long enough for a 5 GiB download", would have
// produced an hours-long capability for no benefit.
//
// The one thing it does not survive is a broken transfer: /cdn serves no Range
// requests, so a resumed download is a fresh request and needs a fresh URL.
const downloadURLTTL = 15 * time.Minute

// Download mints a time-bounded URL for the image's stored bytes.
// @Summary Download an OS image
// @Description Returns a short-lived signed URL from which the image file can be fetched, so an account can take an uploaded or captured image back out of the platform. The URL carries its own capability and needs no Authorization header, which is what lets a browser navigate to it. Requires image manage, not merely read.
// @Tags images
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path string true "Image ID"
// @Success 200 {object} results.APIResponse{data=results.ImageDownloadResult} "Download URL minted"
// @Failure 400 {object} results.APIResponse "Validation error"
// @Failure 401 {object} results.APIResponse "Unauthorized"
// @Failure 404 {object} results.APIResponse "Image not found"
// @Failure 409 {object} results.APIResponse "Image has no stored file yet"
// @Failure 500 {object} results.APIResponse "Internal server error"
// @Router /api/v1/images/{id}/download [get]
func (h *ImageHandler) Download(c echo.Context) error {
	userID, err := middleware.GetUserIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get user ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	accountID, err := middleware.GetAccountIDFromContext(c)
	if err != nil {
		h.logger.Error("failed to get account ID from context", "error", err)
		return results.SendUnauthorizedError(c, "unauthorized")
	}

	imageIDStr := c.Param("id")
	imageID, err := uuid.Parse(imageIDStr)
	if err != nil {
		return results.SendValidationError(c, "invalid image ID")
	}

	// Load the record first, for three reasons: it is the account-scoped read
	// that makes this tenant-safe, it carries the name/size/checksum the caller
	// needs to verify their copy, and it turns "no file yet" into a 409 that
	// says so instead of an opaque failure out of the signer.
	img, err := h.imageService.GetByID(c.Request().Context(), image_get.ImageGetQuery{
		ID:          imageID,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		if errors.Is(err, app_errors.ErrImageNotFound) {
			return results.SendNotFoundError(c, "image not found")
		}
		results.LogFailure(c.Request().Context(), h.logger, "failed to load image for download", err, "image_id", imageID)
		return results.SendInternalServerError(c, "failed to get image", err)
	}

	if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath == "" {
		// A capture that has not uploaded yet lands here, which is the common
		// case and is not an error the caller caused.
		return results.SendError(c, http.StatusConflict, "image has no stored file to download yet", nil)
	}

	signedURL, err := h.imageService.GetSignedURL(c.Request().Context(), accountID, imageID, downloadURLTTL)
	if err != nil {
		h.logger.ErrorContext(c.Request().Context(), "failed to mint image download URL", "error", err, "image_id", imageID)
		return results.SendInternalServerError(c, "failed to prepare the download", err)
	}

	fileName := downloadFileName(img.FileStorageInfo.FileName, img.FileStorageInfo.FilePath)
	signedURL = withDownloadFileName(signedURL, fileName)

	// Recorded at the mint, before the bytes move — see ActionImageDownload for
	// why that is the only point where an account can be attributed.
	h.audit.RecordDetail(
		c.Request().Context(),
		audit_service.UserActor(accountID, userID),
		audit_event.ActionImageDownload,
		"image", imageID.String(),
		fmt.Sprintf("file_name=%s size_bytes=%d", fileName, img.FileStorageInfo.SizeBytes),
	)

	return results.SendOKResponse(c, results.ImageDownloadResult{
		ID:        img.ID,
		URL:       signedURL,
		FileName:  fileName,
		SizeBytes: img.FileStorageInfo.SizeBytes,
		Checksum:  img.Checksum,
		ExpiresAt: time.Now().Add(downloadURLTTL).Unix(),
	}, "download URL created")
}

// downloadFileName picks the name a downloaded image should land under.
//
// Storage keys are `<account-id>/<image-id>.<ext>`, so the basename is a UUID:
// without the recorded upload name, an account taking three images out gets
// three files it cannot tell apart. The path basename is the fallback rather
// than the first choice, and it is still better than nothing because it at least
// carries the extension that says how to decompress the thing.
func downloadFileName(recorded, storagePath string) string {
	if name := cdnsign.SanitizeFileName(recorded); name != "" {
		return name
	}
	if name := cdnsign.SanitizeFileName(path.Base(storagePath)); name != "" {
		return name
	}
	return "image.bin"
}

// withDownloadFileName appends the filename hint /cdn turns into a
// Content-Disposition. It is deliberately NOT part of the HMAC: the signature
// covers the storage path, which is what decides which bytes are served, and
// widening it would change a scheme the agent, install.sh and every existing
// signed URL already share. The name only labels a response whose contents the
// holder is entitled to either way — sanitization, not signing, is what makes
// that safe.
func withDownloadFileName(rawURL, fileName string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set(cdnsign.ParamFileName, fileName)
	u.RawQuery = q.Encode()
	return u.String()
}
