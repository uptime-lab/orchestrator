package image

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_compress"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/image_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	domain_image "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
)

// --- fakes -----------------------------------------------------------------

type fakeImageService struct {
	img       *domain_image.Image
	getErr    error
	signedURL string
	signTTL   time.Duration
	signErr   error
}

func (f *fakeImageService) GetByID(context.Context, image_get.ImageGetQuery) (*domain_image.Image, error) {
	return f.img, f.getErr
}

func (f *fakeImageService) GetSignedURL(_ context.Context, _, _ uuid.UUID, ttl time.Duration) (string, error) {
	f.signTTL = ttl
	return f.signedURL, f.signErr
}

func (f *fakeImageService) Create(context.Context, image_create.ImageCreateCommand) (*domain_image.Image, error) {
	panic("not used")
}
func (f *fakeImageService) Update(context.Context, image_update.ImageUpdateCommand) (*domain_image.Image, error) {
	panic("not used")
}
func (f *fakeImageService) GetAllByAccountID(context.Context, image_list.ImageListQuery) ([]domain_image.Image, error) {
	panic("not used")
}
func (f *fakeImageService) Delete(context.Context, uuid.UUID, uuid.UUID) error { panic("not used") }
func (f *fakeImageService) BeginUpload(context.Context, uuid.UUID, uuid.UUID, int64) (services.UploadLease, error) {
	panic("not used")
}
func (f *fakeImageService) UploadFile(context.Context, image_upload.ImageUploadCommand, io.Reader) (*domain_image.Image, error) {
	panic("not used")
}
func (f *fakeImageService) StartCompression(context.Context, image_compress.ImageCompressCommand) (*domain_image.Image, error) {
	panic("not used")
}
func (f *fakeImageService) CreateCaptureImage(context.Context, uuid.UUID, string, bool, uuid.UUID, string) (*domain_image.Image, error) {
	panic("not used")
}
func (f *fakeImageService) FailAwaitingCapture(context.Context, uuid.UUID, uuid.UUID) error {
	panic("not used")
}
func (f *fakeImageService) CaptureUploadURL(context.Context, uuid.UUID, uuid.UUID, time.Duration) (string, error) {
	panic("not used")
}
func (f *fakeImageService) VerifyCaptureUploadCapability(uuid.UUID, string, string) error {
	panic("not used")
}
func (f *fakeImageService) FinalizeCapture(context.Context, uuid.UUID, string, string, io.Reader) (*domain_image.Image, error) {
	panic("not used")
}
func (f *fakeImageService) RemainingUploadBytes(context.Context, uuid.UUID, *uuid.UUID) (int64, error) {
	panic("not used")
}

type fakeAuditRepo struct{ events []audit_event.AuditEvent }

func (f *fakeAuditRepo) Create(_ context.Context, e *audit_event.AuditEvent) error {
	f.events = append(f.events, *e)
	return nil
}
func (f *fakeAuditRepo) List(context.Context, uuid.UUID, repository.AuditEventListFilter, int, int) ([]audit_event.AuditEvent, int64, error) {
	return nil, 0, nil
}
func (f *fakeAuditRepo) CountByAccountAndActionsSince(context.Context, uuid.UUID, []string, time.Time) (int64, error) {
	return 0, nil
}

// --- helpers ---------------------------------------------------------------

func newDownloadHandler(t *testing.T, svc *fakeImageService) (*ImageHandler, *fakeAuditRepo) {
	t.Helper()
	repo := &fakeAuditRepo{}
	logger := slog.New(slog.DiscardHandler)
	h := NewImageHandler(svc, audit_service.NewRecorder(repo, logger), &config.Config{}, logger)
	return h, repo
}

func callDownload(t *testing.T, h *ImageHandler, imageID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/"+imageID.String()+"/download", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/images/:id/download")
	c.SetParamNames("id")
	c.SetParamValues(imageID.String())
	c.Set("userID", uuid.New().String())
	c.Set("accountID", uuid.New().String())
	if err := h.Download(c); err != nil {
		t.Fatalf("Download returned an error: %v", err)
	}
	return rec
}

func storedImage(fileName string) *domain_image.Image {
	return &domain_image.Image{
		ID:       uuid.New(),
		Title:    "Ubuntu",
		Checksum: "sha256:abc",
		FileStorageInfo: &domain_image.FileStorageInfo{
			FilePath:  "8b1a9953-0000-0000-0000-000000000000/2f2d0f16-0000-0000-0000-000000000000.xz",
			FileName:  fileName,
			SizeBytes: 1234,
		},
	}
}

// --- tests -----------------------------------------------------------------

// The whole point of the endpoint: an operator offboarding gets a URL that needs
// no Authorization header, plus the metadata to verify the copy they took.
func TestDownloadMintsACapabilityWithTheRecordedName(t *testing.T) {
	svc := &fakeImageService{
		img:       storedImage("ubuntu-24.04-server.img.xz"),
		signedURL: "https://app.example/cdn/acct/img.xz?exp=1&sig=deadbeef",
	}
	h, _ := newDownloadHandler(t, svc)

	rec := callDownload(t, h, svc.img.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			URL       string `json:"url"`
			FileName  string `json:"file_name"`
			SizeBytes int64  `json:"size_bytes"`
			Checksum  string `json:"checksum"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v (%s)", err, rec.Body.String())
	}
	if body.Data.FileName != "ubuntu-24.04-server.img.xz" {
		t.Errorf("file_name = %q, want the recorded upload name", body.Data.FileName)
	}
	if body.Data.SizeBytes != 1234 || body.Data.Checksum != "sha256:abc" {
		t.Errorf("size/checksum not carried through: %+v", body.Data)
	}
	if body.Data.ExpiresAt <= time.Now().Unix() {
		t.Errorf("expires_at = %d, want a future time", body.Data.ExpiresAt)
	}

	u, err := url.Parse(body.Data.URL)
	if err != nil {
		t.Fatalf("parsing minted URL: %v", err)
	}
	q := u.Query()
	if q.Get(cdnsign.ParamFileName) != "ubuntu-24.04-server.img.xz" {
		t.Errorf("URL filename hint = %q", q.Get(cdnsign.ParamFileName))
	}
	// The existing capability must survive the rewrite that adds the hint.
	if q.Get(cdnsign.ParamExpires) != "1" || q.Get(cdnsign.ParamSignature) != "deadbeef" {
		t.Errorf("appending the filename dropped the signature: %s", u.RawQuery)
	}
}

// The TTL covers minting-to-start, not the transfer. Asserted because widening
// it "so a 5 GiB download fits" is the plausible wrong change.
func TestDownloadUsesAShortLivedCapability(t *testing.T) {
	svc := &fakeImageService{img: storedImage("a.img.xz"), signedURL: "https://h/cdn/x?exp=1&sig=s"}
	h, _ := newDownloadHandler(t, svc)
	callDownload(t, h, svc.img.ID)
	if svc.signTTL != downloadURLTTL {
		t.Errorf("ttl = %v, want %v", svc.signTTL, downloadURLTTL)
	}
	if downloadURLTTL > time.Hour {
		t.Errorf("downloadURLTTL = %v: a leaked link should not outlive the session that made it", downloadURLTTL)
	}
}

// A capture that has not uploaded yet is the common case, and it must not read
// as a server fault.
func TestDownloadRefusesAnImageWithNoStoredFile(t *testing.T) {
	img := storedImage("a.img.xz")
	img.FileStorageInfo = nil
	svc := &fakeImageService{img: img}
	h, _ := newDownloadHandler(t, svc)

	rec := callDownload(t, h, img.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if svc.signTTL != 0 {
		t.Error("no URL should have been minted for an image with no file")
	}
}

func TestDownloadIs404ForAnotherAccountsImage(t *testing.T) {
	// The account scoping lives in the service read; the handler's job is to
	// keep it a 404 rather than leaking existence through a 500.
	svc := &fakeImageService{getErr: app_errors.ErrImageNotFound}
	h, _ := newDownloadHandler(t, svc)
	if rec := callDownload(t, h, uuid.New()); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// Recorded at the mint, because /cdn has no account to attribute a fetch to.
func TestDownloadIsAudited(t *testing.T) {
	svc := &fakeImageService{img: storedImage("golden.img.zst"), signedURL: "https://h/cdn/x?exp=1&sig=s"}
	h, repo := newDownloadHandler(t, svc)
	callDownload(t, h, svc.img.ID)

	if len(repo.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(repo.events))
	}
	ev := repo.events[0]
	if ev.Action != audit_event.ActionImageDownload {
		t.Errorf("action = %q, want %q", ev.Action, audit_event.ActionImageDownload)
	}
	if ev.TargetID != svc.img.ID.String() {
		t.Errorf("target = %q, want the image id", ev.TargetID)
	}
}

// A refused download must not leave a row saying one happened.
func TestRefusedDownloadIsNotAudited(t *testing.T) {
	img := storedImage("a.img.xz")
	img.FileStorageInfo = nil
	h, repo := newDownloadHandler(t, &fakeImageService{img: img})
	callDownload(t, h, img.ID)
	if len(repo.events) != 0 {
		t.Fatalf("audit events = %d, want none for a refused download", len(repo.events))
	}
}

func TestDownloadFileNameFallsBackToTheStorageKey(t *testing.T) {
	// No recorded name: the UUID basename is poor, but it still carries the
	// extension that says how to decompress the file.
	if got := downloadFileName("", "acct/2f2d0f16-1111.xz"); got != "2f2d0f16-1111.xz" {
		t.Errorf("got %q", got)
	}
	// Nothing usable anywhere: a name is still required, because the header
	// would otherwise be built from an empty string.
	if got := downloadFileName("", ""); got != "image.bin" {
		t.Errorf("got %q, want the last-resort name", got)
	}
	// A hostile recorded name never reaches the header verbatim.
	if got := downloadFileName("a.img\r\nSet-Cookie: x=1", "acct/x.xz"); got != "a.imgSet-Cookie_x1" {
		t.Errorf("got %q", got)
	}
}
