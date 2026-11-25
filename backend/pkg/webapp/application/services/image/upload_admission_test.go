package image

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// The multipart body used to be parsed in full before the service saw the
// record, and nothing bounded how many such parses ran at once. These tests
// pin the admission that now runs BEFORE the body.

func seedImage(t *testing.T, repo *fakeImageRepo, accountID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := repo.Create(context.Background(), &image.Image{ID: id, AccountID: accountID}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

func admissionService(t *testing.T, global, perAccount int) (*Service, *fakeImageRepo) {
	t.Helper()
	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxConcurrentUploads = global
	cfg.Image.MaxConcurrentUploadsPerAccount = perAccount
	return NewService(repo, newFakeStorage(), cfg), repo
}

func TestBeginUploadRefusesAThirdUploadInOneAccountWithA429(t *testing.T) {
	svc, repo := admissionService(t, 6, 2)
	ctx := context.Background()
	acct := uuid.New()

	l1, err := svc.BeginUpload(ctx, acct, seedImage(t, repo, acct), 10)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	defer l1.Release()
	l2, err := svc.BeginUpload(ctx, acct, seedImage(t, repo, acct), 10)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	defer l2.Release()

	third := seedImage(t, repo, acct)
	_, err = svc.BeginUpload(ctx, acct, third, 10)
	var capacity *UploadCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("third upload: err = %v, want UploadCapacityError", err)
	}
	if !capacity.PerAccount || capacity.Limit != 2 {
		t.Errorf("refusal = %+v, want the per-account bound of 2", capacity)
	}
	var facing results.OperatorFacingError
	if !errors.As(err, &facing) || facing.HTTPStatus() != 429 || facing.ErrorCode() != "upload_capacity" {
		t.Errorf("refusal must be a typed 429 upload_capacity, got %v", err)
	}
	// A refused request holds nothing: no claim on the record.
	stored, _ := repo.GetByID(ctx, acct, third)
	if stored.CompressionStatus != "" {
		t.Errorf("refused upload left compression_status=%q on the record", stored.CompressionStatus)
	}

	// Releasing one lease frees the account's slot.
	l1.Release()
	l3, err := svc.BeginUpload(ctx, acct, third, 10)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l3.Release()
}

func TestBeginUploadRefusesPastTheServerWideBound(t *testing.T) {
	svc, repo := admissionService(t, 2, 2)
	ctx := context.Background()
	a, b, c := uuid.New(), uuid.New(), uuid.New()

	la, err := svc.BeginUpload(ctx, a, seedImage(t, repo, a), 0)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	defer la.Release()
	lb, err := svc.BeginUpload(ctx, b, seedImage(t, repo, b), 0)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	defer lb.Release()

	_, err = svc.BeginUpload(ctx, c, seedImage(t, repo, c), 0)
	var capacity *UploadCapacityError
	if !errors.As(err, &capacity) || capacity.PerAccount || capacity.Limit != 2 {
		t.Fatalf("err = %v, want the server-wide bound of 2", err)
	}
}

// A refusal AFTER the slot was taken must give the slot back, or a burst of
// requests for someone else's image ids drains the pool without ever uploading.
func TestBeginUploadReturnsTheSlotWhenTheRecordIsForeignOrBusy(t *testing.T) {
	svc, repo := admissionService(t, 1, 1)
	ctx := context.Background()
	acct, other := uuid.New(), uuid.New()
	mine := seedImage(t, repo, acct)
	theirs := seedImage(t, repo, other)

	if _, err := svc.BeginUpload(ctx, acct, theirs, 0); !errors.Is(err, app_errors.ErrImageNotFound) {
		t.Fatalf("foreign record: err = %v, want ErrImageNotFound", err)
	}
	if _, err := svc.BeginUpload(ctx, acct, uuid.New(), 0); !errors.Is(err, app_errors.ErrImageNotFound) {
		t.Fatalf("missing record: err = %v, want ErrImageNotFound", err)
	}
	// A compression holds the record.
	if ok, err := repo.ClaimCompression(ctx, acct, mine); err != nil || !ok {
		t.Fatalf("seed claim: %v %v", ok, err)
	}
	if _, err := svc.BeginUpload(ctx, acct, mine, 0); !errors.Is(err, app_errors.ErrImageCompressionInProgress) {
		t.Fatalf("busy record: err = %v, want ErrImageCompressionInProgress", err)
	}
	if err := repo.SetCompressionState(ctx, acct, mine, "", 0); err != nil {
		t.Fatalf("unclaim: %v", err)
	}

	// Three refusals against a pool of ONE: the slot must still be free.
	lease, err := svc.BeginUpload(ctx, acct, mine, 0)
	if err != nil {
		t.Fatalf("the pool leaked a slot on a refusal: %v", err)
	}
	lease.Release()
}

// The claim is what makes an upload and a compression mutually exclusive;
// BeginUpload takes it and a release without a completed upload gives it back.
func TestLeaseReleaseWithoutAnUploadReleasesTheClaim(t *testing.T) {
	svc, repo := admissionService(t, 6, 2)
	ctx := context.Background()
	acct := uuid.New()
	id := seedImage(t, repo, acct)

	lease, err := svc.BeginUpload(ctx, acct, id, 0)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stored, _ := repo.GetByID(ctx, acct, id)
	if stored.CompressionStatus != image.UploadInProgress {
		t.Fatalf("compression_status = %q after BeginUpload, want the upload claim", stored.CompressionStatus)
	}
	// e.g. the multipart parse failed, or the client vanished.
	lease.Release()
	lease.Release() // idempotent
	stored, _ = repo.GetByID(ctx, acct, id)
	if stored.CompressionStatus != "" {
		t.Fatalf("compression_status = %q after Release, want the claim gone", stored.CompressionStatus)
	}
}

// UploadFile against a leased record must not claim a second time (it would
// refuse its own upload) and, once its final write cleared the claim, the
// lease's release must not touch the record again — a compression may have
// claimed it by then.
func TestUploadFileAgainstALeaseDoesNotClaimTwiceAndReleaseLeavesAFinishedRecordAlone(t *testing.T) {
	svc, repo := admissionService(t, 6, 2)
	ctx := context.Background()
	acct := uuid.New()
	id := seedImage(t, repo, acct)

	lease, err := svc.BeginUpload(ctx, acct, id, 5)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	content := "12345"
	img, err := svc.UploadFile(ctx, image_upload.ImageUploadCommand{
		ID:          id,
		FileName:    "disk.img",
		ContentType: "text/html",
		SizeBytes:   int64(len(content)),
		AuthContext: commands.NewAuthContext(acct, uuid.New()),
	}, strings.NewReader(content))
	if err != nil {
		t.Fatalf("UploadFile under a lease: %v", err)
	}
	if img.CompressionStatus != "" {
		t.Fatalf("compression_status = %q after a finished upload", img.CompressionStatus)
	}

	// A compression takes the record the instant the upload finished.
	if ok, err := repo.ClaimCompression(ctx, acct, id); err != nil || !ok {
		t.Fatalf("compression claim after upload: %v %v", ok, err)
	}
	lease.Release()
	stored, _ := repo.GetByID(ctx, acct, id)
	if stored.CompressionStatus != image.CompressionInProgress {
		t.Fatalf("compression_status = %q after the lease released, want the compression's claim untouched", stored.CompressionStatus)
	}
	// The slot is back.
	if got := len(svc.admission.inflight); got != 0 {
		t.Errorf("inflight = %d after release, want 0", got)
	}
}

// Without a lease (a direct caller) UploadFile still claims and releases for
// itself — the pre-existing contract every service test relies on.
func TestUploadFileWithoutALeaseStillClaimsForItself(t *testing.T) {
	svc, repo := admissionService(t, 6, 2)
	ctx := context.Background()
	acct := uuid.New()
	id := seedImage(t, repo, acct)
	if ok, err := repo.ClaimCompression(ctx, acct, id); err != nil || !ok {
		t.Fatalf("seed claim: %v %v", ok, err)
	}
	_, err := svc.UploadFile(ctx, image_upload.ImageUploadCommand{
		ID: id, FileName: "disk.img", SizeBytes: 1,
		AuthContext: commands.NewAuthContext(acct, uuid.New()),
	}, strings.NewReader("x"))
	if !errors.Is(err, app_errors.ErrImageCompressionInProgress) {
		t.Fatalf("err = %v, want ErrImageCompressionInProgress", err)
	}
}

// The stored type is pinned regardless of what the client's part claimed.
func TestUploadFilePinsTheStoredContentType(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	svc := NewService(repo, store, &config.Config{})
	ctx := context.Background()
	acct := uuid.New()
	id := seedImage(t, repo, acct)
	if _, err := svc.UploadFile(ctx, image_upload.ImageUploadCommand{
		ID: id, FileName: "page.img", ContentType: "text/html; charset=utf-8", SizeBytes: 1,
		AuthContext: commands.NewAuthContext(acct, uuid.New()),
	}, strings.NewReader("x")); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	stored := store.lastOptions
	if stored == nil || stored.ContentType != "application/octet-stream" {
		t.Fatalf("stored Content-Type = %v, want application/octet-stream", stored)
	}
}

// N parallel quota-sized uploads all read the same "remaining" until the
// admitted-but-unwritten bytes count against it.
func TestRemainingUploadBytesCountsAdmittedUploads(t *testing.T) {
	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 1000
	cfg.Image.MaxBytesInstance = 1500
	cfg.Image.MaxConcurrentUploads = 6
	cfg.Image.MaxConcurrentUploadsPerAccount = 3
	svc := NewService(repo, newFakeStorage(), cfg)
	ctx := context.Background()
	acct, other := uuid.New(), uuid.New()
	first := seedImage(t, repo, acct)
	second := seedImage(t, repo, acct)
	foreign := seedImage(t, repo, other)

	l1, err := svc.BeginUpload(ctx, acct, first, 600)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	defer l1.Release()

	// The first upload's own check leaves its own reservation out.
	if remaining, err := svc.RemainingUploadBytes(ctx, acct, &first); err != nil || remaining != 1000 {
		t.Fatalf("own remaining = %d, %v; want 1000", remaining, err)
	}
	// A second upload in the same account sees 600 already spoken for.
	if remaining, err := svc.RemainingUploadBytes(ctx, acct, &second); err != nil || remaining != 400 {
		t.Fatalf("second remaining = %d, %v; want 400", remaining, err)
	}
	// Another account is untouched by the per-account side but the instance
	// ceiling sees every admitted byte: 1500 - 600 = 900.
	if remaining, err := svc.RemainingUploadBytes(ctx, other, &foreign); err != nil || remaining != 900 {
		t.Fatalf("foreign remaining = %d, %v; want 900", remaining, err)
	}
	l1.Release()
	if remaining, err := svc.RemainingUploadBytes(ctx, acct, &second); err != nil || remaining != 1000 {
		t.Fatalf("after release remaining = %d, %v; want 1000", remaining, err)
	}
}

var _ services.UploadLease = (*uploadLease)(nil)

// The slot counts bound how MANY bodies are in flight; the staging budget
// bounds how MUCH they may ask of TMPDIR together. Six free slots and a
// 15 GiB body must still be refused when the disk cannot hold it on top of
// what is already staging.
func TestBeginUploadRefusesPastTheStagingByteBudget(t *testing.T) {
	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxConcurrentUploads = 6
	cfg.Image.MaxConcurrentUploadsPerAccount = 6
	cfg.Image.MaxStagingBytes = 10 << 20
	svc := NewService(repo, newFakeStorage(), cfg)
	account := uuid.New()
	first := seedImage(t, repo, account)
	second := seedImage(t, repo, account)
	third := seedImage(t, repo, account)

	lease, err := svc.BeginUpload(context.Background(), account, first, 6<<20)
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	defer lease.Release()

	_, err = svc.BeginUpload(context.Background(), account, second, 6<<20)
	var capacity *UploadCapacityError
	if !errors.As(err, &capacity) || !capacity.StagingBytes {
		t.Fatalf("second upload past the budget: want a staging-bytes 429, got %v", err)
	}
	if capacity.HTTPStatus() != 429 {
		t.Fatalf("status = %d, want 429", capacity.HTTPStatus())
	}
	// The refusal held nothing: a body that fits still passes.
	small, err := svc.BeginUpload(context.Background(), account, third, 2<<20)
	if err != nil {
		t.Fatalf("a body within the remaining budget was refused: %v", err)
	}
	small.Release()
	// A body larger than the whole budget is refused even on an idle server.
	lease.Release()
	if _, err := svc.BeginUpload(context.Background(), account, second, 11<<20); !errors.As(err, &capacity) || !capacity.StagingBytes {
		t.Fatalf("oversized body on an idle server: want a staging-bytes 429, got %v", err)
	}
}

