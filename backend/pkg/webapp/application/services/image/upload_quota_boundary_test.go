package image

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
)

// Why these tests exist. Capture-versus-capture admission was serialized
// first, but the multipart path kept its own
// unlocked reads: BeginUpload reserved without reading the quota, the handler
// read it afterwards, and UploadFile's final check read it once more before
// the record was written. A capture and a multipart upload could therefore
// each admit against a state that did not include the other, and a multipart
// commit could land after a capture had already been sized against the bytes
// it was about to store. The three sites now share ONE boundary, Service.quotaMu.
//
// The tests hold a writer between its quota read and its reservation through
// the beforeQuotaSpend hook. Under the unfixed code both writers reach the hook
// with the same stale read; under the fixed code the second writer cannot
// reach it until the first has spent what it read, which is what the bounded
// wait for the second arrival observes.

// quotaBarrier is a per-site "arrived / may go" pair for the beforeQuotaSpend
// hook. A site the test never registers passes straight through.
type quotaBarrier struct {
	mu      sync.Mutex
	arrived map[quotaSite]chan struct{}
	release map[quotaSite]chan struct{}
}

func newQuotaBarrier(sites ...quotaSite) *quotaBarrier {
	b := &quotaBarrier{arrived: map[quotaSite]chan struct{}{}, release: map[quotaSite]chan struct{}{}}
	for _, site := range sites {
		b.arrived[site] = make(chan struct{})
		b.release[site] = make(chan struct{})
	}
	return b
}

func (b *quotaBarrier) hook(site quotaSite) {
	b.mu.Lock()
	arrived, ok := b.arrived[site]
	release := b.release[site]
	if ok {
		// One writer per site per test; a second arrival passes through.
		delete(b.arrived, site)
	}
	b.mu.Unlock()
	if !ok {
		return
	}
	close(arrived)
	<-release
}

// arrivedWithin reports whether a writer reached site before the deadline.
// The wait is what tells "held on the boundary" from "read the stale state":
// a writer that shares the mutex cannot arrive while another holds it.
func (b *quotaBarrier) arrivedWithin(site quotaSite, d time.Duration) bool {
	b.mu.Lock()
	ch := b.arrived[site]
	b.mu.Unlock()
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func (b *quotaBarrier) let(site quotaSite) {
	b.mu.Lock()
	ch := b.release[site]
	delete(b.release, site)
	b.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// boundaryFixture is a capture fixture whose service also accepts multipart
// uploads: one account, a 10-byte ceiling, generous slot counts.
func newBoundaryFixture(t *testing.T, barrier *quotaBarrier) *captureFixture {
	t.Helper()
	cfg := &config.Config{}
	cfg.JWT.SecretKey = "test-secret"
	cfg.Server.AdvertisementURL = "https://platform.test"
	cfg.Image.MaxBytesPerAccount = 10
	cfg.Image.MaxConcurrentUploads = 6
	cfg.Image.MaxConcurrentUploadsPerAccount = 6
	svc := NewService(newFakeImageRepo(), newFakeStorage(), cfg)
	svc.beforeQuotaSpend = barrier.hook
	return &captureFixture{svc: svc, accountID: uuid.New(), ctx: context.Background()}
}

// captureCap reads what a capture stream was allowed to store, 0 when it was
// never admitted.
func (f *captureFixture) captureCap(imageID uuid.UUID) int64 {
	f.svc.admission.mu.Lock()
	defer f.svc.admission.mu.Unlock()
	if r := f.svc.admission.captures[imageID]; r != nil {
		return r.bytes
	}
	return 0
}

func (f *captureFixture) storedBytes(t *testing.T) int64 {
	t.Helper()
	total, err := f.svc.imageRepo.SumBytesByAccountID(f.ctx, f.accountID, nil)
	if err != nil {
		t.Fatalf("SumBytesByAccountID: %v", err)
	}
	return total
}

const boundaryWait = 150 * time.Millisecond

// A capture that has read the remaining quota and a multipart BeginUpload
// admitted meanwhile: what the capture may stream plus what the multipart
// side is told it may store must not exceed the ceiling.
func TestCaptureAdmissionAndMultipartAdmissionShareOneQuotaBoundary(t *testing.T) {
	barrier := newQuotaBarrier(quotaSiteCaptureReserve)
	f := newBoundaryFixture(t, barrier)
	repo := f.svc.imageRepo.(*fakeImageRepo)

	// Capture A reads the whole remaining quota (10) and is held before it
	// reserves it.
	captureID, exp, sig, err := f.begin(t, "a")
	if err != nil {
		t.Fatalf("begin capture: %v", err)
	}
	pr, pw := io.Pipe()
	captureDone := make(chan error, 1)
	go func() {
		_, err := f.svc.FinalizeCapture(f.ctx, captureID, exp, sig, pr)
		captureDone <- err
	}()
	if !barrier.arrivedWithin(quotaSiteCaptureReserve, 5*time.Second) {
		t.Fatal("capture A never reached its reservation")
	}

	// Multipart B, six bytes, asks to be admitted while A is held. Under the
	// unfixed code it is admitted at once against the same 10; under the
	// fixed code it waits on the boundary until A has spent what it read.
	uploadID := seedImage(t, repo, f.accountID)
	type admitted struct {
		lease interface{ Release() }
		err   error
	}
	beginDone := make(chan admitted, 1)
	go func() {
		lease, err := f.svc.BeginUpload(f.ctx, f.accountID, uploadID, 6)
		beginDone <- admitted{lease, err}
	}()
	var b admitted
	select {
	case b = <-beginDone:
	case <-time.After(boundaryWait):
		// Held on the boundary: let A spend its read, then B gets its turn.
		barrier.let(quotaSiteCaptureReserve)
		b = <-beginDone
	}
	// What the handler would let B store: it reads the remaining quota after
	// BeginUpload returns (excluding B's own record) and caps the body by it.
	var permittedB int64
	if b.err == nil {
		defer b.lease.Release()
		remaining, err := f.svc.RemainingUploadBytes(f.ctx, f.accountID, &uploadID)
		if err != nil {
			t.Fatalf("RemainingUploadBytes for B: %v", err)
		}
		permittedB = min(remaining, 6)
		if remaining <= 0 {
			permittedB = 0
		}
	} else if !errors.Is(b.err, app_errors.ErrImageLimitReached) {
		t.Fatalf("BeginUpload for B = %v, want admission or ErrImageLimitReached", b.err)
	}
	barrier.let(quotaSiteCaptureReserve)
	waitFor(t, "capture A to settle its admission", func() bool { return f.captureCap(captureID) > 0 })
	capA := f.captureCap(captureID)

	if capA+permittedB > 10 {
		t.Fatalf("capture A may store %d and multipart B may store %d against a ceiling of 10: the two admissions read the same remaining quota", capA, permittedB)
	}

	// Finish A; whatever was admitted must still fit once written.
	_, _ = pw.Write([]byte(strings.Repeat("a", int(capA))))
	_ = pw.Close()
	if err := <-captureDone; err != nil {
		t.Fatalf("capture A: %v", err)
	}
	if got := f.storedBytes(t); got > 10 {
		t.Fatalf("stored %d bytes against a ceiling of 10", got)
	}
}

// A multipart upload that has passed its final quota check but not yet
// written its record, and a capture admitted in that window: the bytes the
// upload commits plus the bytes the capture is allowed to stream must not
// exceed the ceiling. The upload is a direct UploadFile — the path whose
// reservation is smaller than its body (here: absent; for the HTTP path, a
// chunked request with no per-request cap reserves 0), which is exactly when
// the commit is the only thing standing between the two writers.
func TestMultipartCommitAndCaptureAdmissionShareOneQuotaBoundary(t *testing.T) {
	barrier := newQuotaBarrier(quotaSiteCaptureReserve, quotaSiteUploadCommit)
	f := newBoundaryFixture(t, barrier)
	repo := f.svc.imageRepo.(*fakeImageRepo)

	// Upload B, six bytes, is held after its final quota read (10 left) and
	// before its record is written.
	uploadID := seedImage(t, repo, f.accountID)
	uploadDone := make(chan error, 1)
	go func() {
		_, err := f.svc.UploadFile(f.ctx, image_upload.ImageUploadCommand{
			ID: uploadID, FileName: "disk.img", SizeBytes: 6,
			AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
		}, strings.NewReader("bbbbbb"))
		uploadDone <- err
	}()
	if !barrier.arrivedWithin(quotaSiteUploadCommit, 5*time.Second) {
		t.Fatal("upload B never reached its commit")
	}

	// Capture A asks to be admitted while B is held. Under the unfixed code
	// it reads the same 10 and reaches its reservation; under the fixed code
	// it waits on the boundary until B has committed.
	captureID, exp, sig, err := f.begin(t, "a")
	if err != nil {
		t.Fatalf("begin capture: %v", err)
	}
	pr, pw := io.Pipe()
	captureDone := make(chan error, 1)
	go func() {
		_, err := f.svc.FinalizeCapture(f.ctx, captureID, exp, sig, pr)
		captureDone <- err
	}()
	if barrier.arrivedWithin(quotaSiteCaptureReserve, boundaryWait) {
		// Both writers hold the same read. Let B commit first, then A
		// reserve — the order in which the overshoot lands durably.
		barrier.let(quotaSiteUploadCommit)
		if err := <-uploadDone; err != nil {
			t.Fatalf("upload B: %v", err)
		}
		barrier.let(quotaSiteCaptureReserve)
	} else {
		barrier.let(quotaSiteUploadCommit)
		if err := <-uploadDone; err != nil {
			t.Fatalf("upload B: %v", err)
		}
		if !barrier.arrivedWithin(quotaSiteCaptureReserve, 5*time.Second) {
			t.Fatal("capture A never reached its reservation after B committed")
		}
		barrier.let(quotaSiteCaptureReserve)
	}

	// A streams ten bytes: everything it was allowed to, and more than what
	// is left after B.
	go func() {
		_, _ = pw.Write([]byte(strings.Repeat("a", 10)))
		_ = pw.Close()
	}()
	captureErr := <-captureDone
	if got := f.storedBytes(t); got > 10 {
		t.Fatalf("stored %d bytes against a ceiling of 10 (capture result: %v): the capture was sized against a quota the upload was about to spend", got, captureErr)
	}
	if captureErr != nil && !errors.Is(captureErr, app_errors.ErrImageLimitReached) {
		t.Fatalf("capture A = %v, want success within what was left or ErrImageLimitReached", captureErr)
	}
}

// The multipart admission refuses at the boundary when nothing is left, as a
// typed refusal the handler answers as itself, and holds nothing afterwards.
func TestBeginUploadRefusesAtTheByteQuotaWithNothingHeld(t *testing.T) {
	f := newBoundaryFixture(t, newQuotaBarrier())
	repo := f.svc.imageRepo.(*fakeImageRepo)
	full := seedImage(t, repo, f.accountID)
	stored, _ := repo.GetByID(f.ctx, f.accountID, full)
	stored.SetFileStorageInfo("local", "acct/full.img", "full.img", 10)
	if err := repo.Update(f.ctx, stored); err != nil {
		t.Fatalf("seed stored bytes: %v", err)
	}

	next := seedImage(t, repo, f.accountID)
	_, err := f.svc.BeginUpload(f.ctx, f.accountID, next, 1)
	if !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("BeginUpload into a full quota = %v, want ErrImageLimitReached", err)
	}
	var facing results.OperatorFacingError
	if !errors.As(err, &facing) || facing.HTTPStatus() != 409 || facing.ErrorCode() != "image_quota" {
		t.Fatalf("the refusal must be a typed 409 image_quota, got %v", err)
	}
	if got := len(f.svc.admission.inflight); got != 0 {
		t.Fatalf("inflight = %d after a quota refusal, want 0", got)
	}
	record, _ := repo.GetByID(f.ctx, f.accountID, next)
	if record.CompressionStatus != "" {
		t.Fatalf("compression_status = %q after a quota refusal, want no claim", record.CompressionStatus)
	}

	// A re-upload into the record that holds the bytes is a replacement and
	// is admitted: its own stored bytes are left out of its total.
	lease, err := f.svc.BeginUpload(f.ctx, f.accountID, full, 10)
	if err != nil {
		t.Fatalf("replacement upload refused: %v", err)
	}
	lease.Release()
}
