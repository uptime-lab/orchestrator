package image

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	image_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/image"
	domain_image "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// The handler must ask the service to ADMIT the upload
// (slot, reservation, account-scoped lookup, claim) before it reads a byte of
// the body, and must give the admission back on every exit.

// countingBody records how many body bytes the handler consumed.
type countingBody struct {
	r io.Reader
	n int
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += n
	return n, err
}

type fakeLease struct{ released int }

func (l *fakeLease) Release() { l.released++ }

// uploadFakeService is the fake for the upload path: BeginUpload answers
// with a configured error or a recording lease, UploadFile records the
// command and answers with an image.
type uploadFakeService struct {
	fakeImageService
	beginErr      error
	lease         *fakeLease
	declared      int64
	uploaded      *image_upload.ImageUploadCommand
	uploadErr     error
	remaining     int64
	remainingCall bool
}

func (f *uploadFakeService) BeginUpload(_ context.Context, _, _ uuid.UUID, declared int64) (services.UploadLease, error) {
	f.declared = declared
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	if f.lease == nil {
		f.lease = &fakeLease{}
	}
	return f.lease, nil
}

func (f *uploadFakeService) UploadFile(_ context.Context, cmd image_upload.ImageUploadCommand, r io.Reader) (*domain_image.Image, error) {
	f.uploaded = &cmd
	_, _ = io.Copy(io.Discard, r)
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	return &domain_image.Image{ID: cmd.ID, AccountID: cmd.AuthContext.AccountID}, nil
}

func (f *uploadFakeService) RemainingUploadBytes(context.Context, uuid.UUID, *uuid.UUID) (int64, error) {
	f.remainingCall = true
	if f.remaining == 0 {
		return services.NoByteCeiling, nil
	}
	return f.remaining, nil
}

func multipartBody(t *testing.T, name, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

func callUpload(t *testing.T, svc services.ImageService, cfg *config.Config, body *countingBody, contentType string, contentLength int64) *httptest.ResponseRecorder {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	h := NewImageHandler(svc, audit_service.NewRecorder(&fakeAuditRepo{}, logger), cfg, logger)
	imageID := uuid.New()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/images/"+imageID.String()+"/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = contentLength
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/images/:id/upload")
	c.SetParamNames("id")
	c.SetParamValues(imageID.String())
	c.Set("userID", uuid.New().String())
	c.Set("accountID", uuid.New().String())
	if err := h.UploadFile(c); err != nil {
		t.Fatalf("UploadFile returned an error: %v", err)
	}
	return rec
}

func TestUploadRefusesAForeignImageBeforeReadingTheBody(t *testing.T) {
	buf, ct := multipartBody(t, "disk.img", strings.Repeat("x", 4096))
	body := &countingBody{r: buf}
	svc := &uploadFakeService{beginErr: app_errors.ErrImageNotFound}

	rec := callUpload(t, svc, &config.Config{}, body, ct, int64(buf.Len()))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %s", rec.Code, rec.Body.String())
	}
	if body.n != 0 {
		t.Fatalf("the handler read %d body bytes before the account-scoped lookup refused the record", body.n)
	}
	if svc.remainingCall {
		t.Errorf("the quota was consulted for a record the account does not own")
	}
}

func TestUploadAnswersACapacityRefusalAs429WithoutReadingTheBody(t *testing.T) {
	buf, ct := multipartBody(t, "disk.img", "payload")
	body := &countingBody{r: buf}
	svc := &uploadFakeService{beginErr: &image_service.UploadCapacityError{Limit: 2, PerAccount: true}}

	rec := callUpload(t, svc, &config.Config{}, body, ct, int64(buf.Len()))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Code != "upload_capacity" {
		t.Errorf("code = %q, want upload_capacity", envelope.Code)
	}
	if body.n != 0 {
		t.Fatalf("the handler read %d body bytes for a request it had no slot for", body.n)
	}
}

func TestUploadAnswersABusyRecordAs409(t *testing.T) {
	buf, ct := multipartBody(t, "disk.img", "payload")
	svc := &uploadFakeService{beginErr: app_errors.ErrImageCompressionInProgress}
	rec := callUpload(t, svc, &config.Config{}, &countingBody{r: buf}, ct, int64(buf.Len()))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

// The reservation is the declared Content-Length when there is one and the
// per-request cap for a chunked request.
func TestUploadReservesTheDeclaredLengthOrTheCap(t *testing.T) {
	buf, ct := multipartBody(t, "disk.img", "payload")
	svc := &uploadFakeService{}
	callUpload(t, svc, &config.Config{}, &countingBody{r: bytes.NewReader(buf.Bytes())}, ct, int64(buf.Len()))
	if svc.declared != int64(buf.Len()) {
		t.Errorf("declared = %d, want the Content-Length %d", svc.declared, buf.Len())
	}

	cfg := &config.Config{}
	cfg.Image.MaxUploadSize = 1 << 20
	svc = &uploadFakeService{}
	callUpload(t, svc, cfg, &countingBody{r: bytes.NewReader(buf.Bytes())}, ct, -1)
	if svc.declared != 1<<20 {
		t.Errorf("declared = %d for a chunked request, want the cap %d", svc.declared, 1<<20)
	}
}

// Every exit path returns the lease: success, a body that is not a form, a
// refused size, and a service failure.
func TestUploadReleasesTheLeaseOnEveryExit(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		buf, ct := multipartBody(t, "disk.img", "payload")
		svc := &uploadFakeService{}
		rec := callUpload(t, svc, &config.Config{}, &countingBody{r: buf}, ct, int64(buf.Len()))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; %s", rec.Code, rec.Body.String())
		}
		if svc.lease.released != 1 {
			t.Errorf("lease released %d times, want 1", svc.lease.released)
		}
		if svc.uploaded == nil || svc.uploaded.FileName != "disk.img" {
			t.Errorf("UploadFile command = %+v", svc.uploaded)
		}
	})
	t.Run("not a form", func(t *testing.T) {
		svc := &uploadFakeService{}
		rec := callUpload(t, svc, &config.Config{}, &countingBody{r: strings.NewReader("not multipart")}, "text/plain", 13)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if svc.lease == nil || svc.lease.released != 1 {
			t.Errorf("lease not released on a parse failure: %+v", svc.lease)
		}
	})
	t.Run("too large by declared length", func(t *testing.T) {
		buf, ct := multipartBody(t, "disk.img", "payload")
		cfg := &config.Config{}
		cfg.Image.MaxUploadSize = 1
		svc := &uploadFakeService{}
		rec := callUpload(t, svc, cfg, &countingBody{r: buf}, ct, 64<<20)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
		if svc.lease == nil || svc.lease.released != 1 {
			t.Errorf("lease not released on a size refusal: %+v", svc.lease)
		}
	})
	t.Run("storage failure", func(t *testing.T) {
		buf, ct := multipartBody(t, "disk.img", "payload")
		svc := &uploadFakeService{uploadErr: io.ErrUnexpectedEOF}
		rec := callUpload(t, svc, &config.Config{}, &countingBody{r: buf}, ct, int64(buf.Len()))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if svc.lease.released != 1 {
			t.Errorf("lease released %d times, want 1", svc.lease.released)
		}
	})
}
