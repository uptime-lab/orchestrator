package image

import (
	"context"
	"errors"
	"io"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// Why these tests exist. FinalizeCapture read the account's
// remaining byte quota once, capped its own stream to it, and stored the
// result — but reserved nothing while the stream ran. Two overlapping
// captures therefore each read the whole remaining quota, each fit alone,
// and together stored twice it, durably; the multipart admission never saw a
// capture. A capture now reserves its cap in the same tracker for as long as
// it streams.

type captureFixture struct {
	svc       *Service
	accountID uuid.UUID
	ctx       context.Context
}

func newCaptureFixture(t *testing.T, perAccountBytes int64) *captureFixture {
	t.Helper()
	cfg := &config.Config{}
	cfg.JWT.SecretKey = "test-secret"
	cfg.Server.AdvertisementURL = "https://platform.test"
	cfg.Image.MaxBytesPerAccount = perAccountBytes
	return &captureFixture{svc: NewService(newFakeImageRepo(), newFakeStorage(), cfg), accountID: uuid.New(), ctx: context.Background()}
}

// begin creates a capture record and returns what FinalizeCapture needs. The
// creation itself consults the byte quota, so a refusal can land here —
// which is the fix working one step earlier, and the callers treat it as the
// same outcome.
func (f *captureFixture) begin(t *testing.T, name string) (uuid.UUID, string, string, error) {
	t.Helper()
	img, err := f.svc.CreateCaptureImage(f.ctx, f.accountID, name, false, uuid.New(), "node-1")
	if err != nil {
		return uuid.Nil, "", "", err
	}
	u, err := f.svc.CaptureUploadURL(f.ctx, f.accountID, img.ID, time.Hour)
	if err != nil {
		t.Fatalf("CaptureUploadURL: %v", err)
	}
	parsed, _ := neturl.Parse(u)
	return img.ID, parsed.Query().Get("exp"), parsed.Query().Get("sig"), nil
}

func (f *captureFixture) finalize(t *testing.T, name string, body io.Reader) (*image.Image, error) {
	t.Helper()
	id, exp, sig, err := f.begin(t, name)
	if err != nil {
		return nil, err
	}
	return f.svc.FinalizeCapture(f.ctx, id, exp, sig, body)
}

func (f *captureFixture) reserved() int64 {
	account, _ := f.svc.admission.reservedBytes(f.accountID, nil)
	return account
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOverlappingCapturesCannotTogetherExceedTheByteQuota(t *testing.T) {
	f := newCaptureFixture(t, 10)

	// Capture A is admitted and its body held open.
	pr, pw := io.Pipe()
	type outcome struct {
		img *image.Image
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		img, err := f.finalize(t, "a", pr)
		done <- outcome{img, err}
	}()
	waitFor(t, "capture A to reserve its bytes", func() bool { return f.reserved() > 0 })
	if got := f.reserved(); got != 10 {
		t.Fatalf("capture A reserved %d bytes, want the whole remaining quota (10)", got)
	}

	// Capture B, six bytes, would fit alone. While A holds the quota it must
	// be refused — before the fix it stored its six bytes and the account
	// ended at twelve.
	if _, err := f.finalize(t, "b", strings.NewReader("bbbbbb")); !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("capture B overlapping A = %v, want ErrImageLimitReached", err)
	}

	// A finishes with six bytes and releases the reservation.
	_, _ = pw.Write([]byte("aaaaaa"))
	_ = pw.Close()
	res := <-done
	if res.err != nil {
		t.Fatalf("capture A: %v", res.err)
	}
	if res.img.FileStorageInfo == nil || res.img.FileStorageInfo.SizeBytes != 6 {
		t.Fatalf("capture A stored %+v, want 6 bytes", res.img.FileStorageInfo)
	}
	if got := f.reserved(); got != 0 {
		t.Fatalf("%d bytes still reserved after capture A settled", got)
	}

	// Sequentially, what still fits (4 bytes) fits and what does not (5) does not.
	if _, err := f.finalize(t, "c", strings.NewReader("cccc")); err != nil {
		t.Fatalf("capture C (4 bytes into 4 remaining): %v", err)
	}
	if _, err := f.finalize(t, "d", strings.NewReader("d")); !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("capture D past the quota = %v, want ErrImageLimitReached", err)
	}
}

func TestACaptureReservationIsReleasedOnFailure(t *testing.T) {
	f := newCaptureFixture(t, 10)
	// An empty body is refused after the reservation was taken.
	if _, err := f.finalize(t, "empty", strings.NewReader("")); err == nil {
		t.Fatal("an empty capture was accepted")
	}
	if got := f.reserved(); got != 0 {
		t.Fatalf("%d bytes still reserved after a failed capture", got)
	}
	if _, err := f.finalize(t, "ok", strings.NewReader("123456")); err != nil {
		t.Fatalf("a capture after the failed one was refused: %v", err)
	}
}

func TestACaptureReservationIsSeenByTheMultipartAdmission(t *testing.T) {
	// The reservation lives in the same tracker BeginUpload consults, so a
	// multipart upload admitted while a capture streams sees its bytes as used.
	f := newCaptureFixture(t, 10)
	pr, pw := io.Pipe()
	go func() { _, _ = f.finalize(t, "a", pr) }()
	waitFor(t, "capture A to reserve its bytes", func() bool { return f.reserved() > 0 })
	remaining, err := f.svc.RemainingUploadBytes(f.ctx, f.accountID, nil)
	if err != nil {
		t.Fatalf("RemainingUploadBytes: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining = %d while a capture holds the quota, want 0", remaining)
	}
	_ = pw.Close()
}

func TestACaptureWithoutAByteCeilingReservesNothing(t *testing.T) {
	f := newCaptureFixture(t, 0)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() { _, _ = f.finalize(t, "a", pr); close(done) }()
	time.Sleep(20 * time.Millisecond)
	if got := f.reserved(); got != 0 {
		t.Fatalf("%d bytes reserved with no ceiling configured", got)
	}
	_, _ = pw.Write([]byte("x"))
	_ = pw.Close()
	<-done
}
