package two_factor

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/two_factor"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// plainEncryptor keeps the test readable; the real one is AES-256-GCM.
type plainEncryptor struct{}

func (plainEncryptor) Encrypt(p string) (string, error) { return "enc:" + p, nil }

func (plainEncryptor) Decrypt(c string) (string, error) {
	return strings.TrimPrefix(c, "enc:"), nil
}

type stubFactorRepo struct {
	repository.TwoFactorRepository
	factor *two_factor.TwoFactor
	// getErr makes GetByUserID fail like a database blip.
	getErr error
	// lastStep mirrors the two_factors.last_used_step ledger; nil = never used.
	lastStep *int64
	// sessionDead makes the session-bound confirm refuse, the way the SQL
	// boundary does once the family was revoked or the generation moved.
	sessionDead bool
	// lastProof is what the session-bound confirm was handed.
	lastProof *recordedProof
}

func (r *stubFactorRepo) GetByUserID(_ context.Context, _ uuid.UUID) (*two_factor.TwoFactor, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.factor == nil {
		return nil, app_errors.ErrTwoFactorNotEnrolled
	}
	// A COPY, as a database read is: the service must not be able to mutate
	// the stored row through the pointer it read.
	copied := *r.factor
	return &copied, nil
}

func (r *stubFactorRepo) Upsert(_ context.Context, f *two_factor.TwoFactor) error {
	r.factor = f
	return nil
}

func (r *stubFactorRepo) ConsumeRecoveryCode(_ context.Context, _ uuid.UUID, codeHash string) (bool, error) {
	if r.factor == nil {
		return false, nil
	}
	for i, h := range r.factor.RecoveryCodeHashes {
		if h == codeHash {
			r.factor.RecoveryCodeHashes = append(
				r.factor.RecoveryCodeHashes[:i], r.factor.RecoveryCodeHashes[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// Same predicate as the SQL implementation, including the NULL tolerance: a
// bare `<` against a nil ledger would refuse every code forever.
func (r *stubFactorRepo) ConsumeTOTPStep(_ context.Context, _ uuid.UUID, step int64) (bool, error) {
	if r.lastStep != nil && *r.lastStep >= step {
		return false, nil
	}
	r.lastStep = &step
	return true, nil
}

func (r *stubFactorRepo) RecordTOTPStep(_ context.Context, _ uuid.UUID, step int64) error {
	if r.lastStep == nil || *r.lastStep < step {
		r.lastStep = &step
	}
	return nil
}

func (r *stubFactorRepo) Delete(_ context.Context, _ uuid.UUID) error {
	r.factor = nil
	return nil
}

func (r *stubFactorRepo) RemoveEnabledAndAdvanceGeneration(_ context.Context, _ uuid.UUID) (bool, error) {
	if r.factor == nil || r.factor.ConfirmedAt == nil {
		return false, nil
	}
	r.factor = nil
	return true, nil
}

type stubUserRepo struct {
	repository.UserRepository
	user *user.User
}

func (r *stubUserRepo) GetByID(_ context.Context, _ uuid.UUID) (*user.User, error) {
	return r.user, nil
}

func newFixture() (*Service, *stubFactorRepo, uuid.UUID) {
	usr := user.NewUser("owner@example.com", "Owner", "hash")
	repo := &stubFactorRepo{}
	svc := NewService(repo, &stubUserRepo{user: usr}, plainEncryptor{}, slog.Default())
	return svc, repo, usr.ID
}

func currentCode(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	return code
}

// Generating a secret must not enable anything: a user who closes the tab
// before scanning the QR would otherwise be locked out by a factor they never
// possessed.
func TestEnrollDoesNotEnableTheFactor(t *testing.T) {
	svc, repo, userID := newFixture()

	if _, err := svc.Enroll(context.Background(), userID); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if repo.factor.Enabled() {
		t.Error("a pending enrollment must not count as an enabled factor")
	}
	if svc.IsEnabled(context.Background(), userID) {
		t.Error("IsEnabled must stay false until the enrollment is confirmed")
	}
}

func TestConfirmEnablesTheFactorAndIssuesRecoveryCodes(t *testing.T) {
	svc, repo, userID := newFixture()
	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	codes, err := svc.Confirm(context.Background(), userID, currentCode(t, enrollment.Secret), liveProof())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if !repo.factor.Enabled() {
		t.Error("a confirmed factor must be enabled")
	}
	if len(codes) == 0 {
		t.Fatal("expected recovery codes")
	}
	// Codes are stored as digests only — showing them again must be impossible.
	for _, c := range codes {
		for _, h := range repo.factor.RecoveryCodeHashes {
			if h == c {
				t.Error("a recovery code was stored in the clear")
			}
		}
	}
}

func TestConfirmRejectsAWrongCode(t *testing.T) {
	svc, _, userID := newFixture()
	if _, err := svc.Enroll(context.Background(), userID); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	_, err := svc.Confirm(context.Background(), userID, "000000", liveProof())

	if !errors.Is(err, app_errors.ErrTwoFactorInvalidCode) {
		t.Fatalf("expected ErrTwoFactorInvalidCode, got %v", err)
	}
}

// Re-enrolling over a CONFIRMED factor is refused: a hijacked session could
// otherwise swap the factor for one the attacker holds, locking out the owner
// with their own security feature.
func TestEnrollRefusesToReplaceAConfirmedFactor(t *testing.T) {
	svc, _, userID := newFixture()
	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if _, err := svc.Confirm(context.Background(), userID, currentCode(t, enrollment.Secret), liveProof()); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	_, err = svc.Enroll(context.Background(), userID)

	if !errors.Is(err, app_errors.ErrTwoFactorAlreadyEnabled) {
		t.Fatalf("expected ErrTwoFactorAlreadyEnabled, got %v", err)
	}
}

func TestVerifyAcceptsARecoveryCodeExactlyOnce(t *testing.T) {
	svc, _, userID := newFixture()
	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	codes, err := svc.Confirm(context.Background(), userID, currentCode(t, enrollment.Secret), liveProof())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if err := svc.Verify(context.Background(), userID, codes[0]); err != nil {
		t.Fatalf("first use of a recovery code should succeed: %v", err)
	}
	if err := svc.Verify(context.Background(), userID, codes[0]); !errors.Is(err, app_errors.ErrTwoFactorInvalidCode) {
		t.Fatalf("a recovery code must not work twice, got %v", err)
	}
	if got := svc.RemainingRecoveryCodes(context.Background(), userID); got != len(codes)-1 {
		t.Errorf("remaining codes = %d, want %d", got, len(codes)-1)
	}
}

// Someone copying a code off paper will not reproduce the dashes or the case.
func TestRecoveryCodesAreForgivingAboutFormatting(t *testing.T) {
	svc, _, userID := newFixture()
	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	codes, err := svc.Confirm(context.Background(), userID, currentCode(t, enrollment.Secret), liveProof())
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	messy := "  " + strings.ToUpper(strings.ReplaceAll(codes[0], "-", "")) + " "
	if err := svc.Verify(context.Background(), userID, messy); err != nil {
		t.Fatalf("a code retyped without dashes and in upper case must still work, got %v", err)
	}
}

// A pending enrollment must not gate a login — that would lock out a user who
// abandoned the flow.
func TestVerifyRejectsAnUnconfirmedFactor(t *testing.T) {
	svc, _, userID := newFixture()
	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	err = svc.Verify(context.Background(), userID, currentCode(t, enrollment.Secret))

	if !errors.Is(err, app_errors.ErrTwoFactorNotEnrolled) {
		t.Fatalf("expected ErrTwoFactorNotEnrolled, got %v", err)
	}
}

func TestSecretIsEncryptedAtRest(t *testing.T) {
	svc, repo, userID := newFixture()

	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if repo.factor.SecretCiphertext == enrollment.Secret {
		t.Error("the TOTP secret is password-equivalent and must not be stored in the clear")
	}
}

// The conditional transitions on the single stored factor.
func (r *stubFactorRepo) CreatePending(_ context.Context, f *two_factor.TwoFactor) error {
	if r.factor != nil && r.factor.Enabled() {
		return app_errors.ErrTwoFactorAlreadyEnabled
	}
	r.factor = f
	return nil
}

func (r *stubFactorRepo) ConfirmPending(_ context.Context, f *two_factor.TwoFactor, expectedSecret string) error {
	if r.factor == nil || r.factor.Enabled() {
		return app_errors.ErrTwoFactorAlreadyEnabled
	}
	if r.factor.SecretCiphertext != expectedSecret {
		return app_errors.ErrTwoFactorEnrollmentChanged
	}
	copied := *f
	r.factor = &copied
	return nil
}

// ConfirmPendingWhileSessionLive applies the session boundary the SQL
// repository applies: a nil family or zero issue time is no
// proof, a family marked dead is refused, and only then the same conditional
// confirm runs. The proof it was handed is recorded so a test can assert the
// service forwarded the caller's session and not something it made up.
func (r *stubFactorRepo) ConfirmPendingWhileSessionLive(ctx context.Context, f *two_factor.TwoFactor, expectedSecret string, familyID uuid.UUID, issuedAt time.Time, generation int64) error {
	r.lastProof = &recordedProof{familyID: familyID, issuedAt: issuedAt, generation: generation}
	if familyID == uuid.Nil || issuedAt.IsZero() || r.sessionDead {
		return app_errors.ErrTokenInvalid
	}
	return r.ConfirmPending(ctx, f, expectedSecret)
}

type recordedProof struct {
	familyID   uuid.UUID
	issuedAt   time.Time
	generation int64
}

// Why these tests exist. Enrolment refused to replace an
// enabled factor only when the read said "enabled"; every read ERROR fell
// through to a fresh pending secret over it. And confirmation was an
// unconditional whole-row upsert, so two confirmations both returned recovery
// codes while only the last set was stored.

func TestEnrollFailsClosedWhenTheFactorCannotBeRead(t *testing.T) {
	svc, repo, userID := newFixture()
	repo.getErr = errors.New("pool timeout")
	if _, err := svc.Enroll(context.Background(), userID); err == nil {
		t.Fatal("a read failure let a new pending secret replace whatever was enrolled")
	}
	if repo.factor != nil {
		t.Fatal("a pending factor was stored despite the read failure")
	}
}

// A PIN, not a regression proof: sequentially, the second confirmation is
// refused by the service's own read-side check, which the old code had too.
// The statement-level refusal — two confirmations racing, or a stale secret —
// is proven against Postgres in sql/credential_transitions_integration_test.go.
func TestConfirmIsAcceptedExactlyOnce(t *testing.T) {
	svc, repo, userID := newFixture()
	enrol, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	code := currentCode(t, enrol.Secret)
	first, err := svc.Confirm(context.Background(), userID, code, liveProof())
	if err != nil {
		t.Fatalf("first Confirm: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("the winning confirmation must return recovery codes")
	}
	if _, err := svc.Confirm(context.Background(), userID, code, liveProof()); !errors.Is(err, app_errors.ErrTwoFactorAlreadyEnabled) {
		t.Fatalf("a second confirmation = %v, want ErrTwoFactorAlreadyEnabled and no second code set", err)
	}
	if !repo.factor.Enabled() {
		t.Fatal("the factor lost its confirmation")
	}
}
