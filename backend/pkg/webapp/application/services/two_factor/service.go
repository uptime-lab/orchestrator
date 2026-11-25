// Package two_factor enrolls, verifies and disables TOTP second factors.
package two_factor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/two_factor"
)

// issuer is what authenticator apps display next to the account name.
const issuer = "Orchestrator"

// skew allows codes from one step either side of now (±30s), absorbing ordinary
// clock drift between the phone and the server. Wider would meaningfully extend
// the window in which an observed code stays usable.
const skew = 1

// totpPeriod is the code lifetime in seconds. Named because the anti-replay
// ledger divides by it to get a step number, and a literal 30 in two places is
// how those drift apart.
const totpPeriod = 30

const (
	// maxVerifyFailures / verifyFailureWindow bound guessing.
	//
	// A six-digit code with ±1 step of skew leaves three codes live at any
	// instant, so a blind guess succeeds with p ≈ 3e-6. Without a per-USER
	// limit an attacker who already has the password can spread guesses across
	// IPs — the per-IP HTTP limiter does nothing against that — and expect a
	// hit in the low hundreds of thousands of attempts, which is hours, not
	// years. Counting per user is what makes the factor worth having.
	maxVerifyFailures   = 10
	verifyFailureWindow = 15 * time.Minute
)

// Encryptor protects the shared secret at rest.
type Encryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// Service owns the second-factor lifecycle.
type Service struct {
	repo      repository.TwoFactorRepository
	userRepo  repository.UserRepository
	encryptor Encryptor
	// failures counts recent wrong codes per user, so guessing is bounded even
	// when the attacker rotates source addresses.
	failures *failureCounter
	logger   *slog.Logger
}

func NewService(
	repo repository.TwoFactorRepository,
	userRepo repository.UserRepository,
	encryptor Encryptor,
	logger *slog.Logger,
) *Service {
	if repo == nil {
		panic("two_factor.Service requires a two-factor repository")
	}
	if userRepo == nil {
		panic("two_factor.Service requires a user repository")
	}
	if encryptor == nil {
		panic("two_factor.Service requires an encryptor")
	}
	return &Service{
		repo:      repo,
		userRepo:  userRepo,
		encryptor: encryptor,
		failures:  newFailureCounter(maxVerifyFailures, verifyFailureWindow),
		logger:    logger.With("service", "two_factor"),
	}
}

// Enrollment is what a user needs to add the account to their authenticator.
type Enrollment struct {
	// Secret is the base32 shared secret, for manual entry when a camera is
	// not an option.
	Secret string
	// OTPAuthURL is the otpauth:// URI behind the QR code.
	OTPAuthURL string
	// QRCodeDataURI is a ready-to-render PNG data: URI.
	//
	// Rendered server-side deliberately: the QR encodes the shared secret, and
	// the library that already generates the key can also draw it, so the
	// frontend needs no QR dependency and the secret does not pass through one.
	QRCodeDataURI string
}

// IsEnabled reports whether the user has a CONFIRMED factor. A pending
// enrollment counts as no factor at all — it must never gate a login, or a user
// who abandoned the flow could not get back in.
//
// It swallows a read error as "no factor", which is fine ONLY for the callers
// that are themselves deliberately fail-open (the enrollment-mandatory policy) or
// merely cosmetic (the status endpoint). A SECURITY decision — the login gate,
// the step-up availability — must use HasConfirmedFactor instead, so a DB blip
// cannot silently make MFA disappear.
func (s *Service) IsEnabled(ctx context.Context, userID uuid.UUID) bool {
	enabled, err := s.HasConfirmedFactor(ctx, userID)
	if err != nil {
		return false
	}
	return enabled
}

// HasConfirmedFactor is IsEnabled with the error kept: it distinguishes
// "not enrolled" (a real answer: no factor) from a genuine read failure (unknown),
// which the caller must NOT collapse to "no factor" at a security boundary.
func (s *Service) HasConfirmedFactor(ctx context.Context, userID uuid.UUID) (bool, error) {
	factor, err := s.repo.GetByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, app_errors.ErrTwoFactorNotEnrolled) {
			return false, nil
		}
		return false, err
	}
	return factor.Enabled(), nil
}

// Enroll mints a new pending secret. It does NOT enable anything: only Confirm
// does, once the user has proved their authenticator holds the same secret.
func (s *Service) Enroll(ctx context.Context, userID uuid.UUID) (*Enrollment, error) {
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	// An already-enabled factor is not silently replaced: a stolen session
	// could otherwise swap the factor for one the attacker controls, locking
	// the owner out with their own security feature.
	// Fail CLOSED on a read error: a transient failure used to fall through
	// to creating a fresh pending secret over an enabled factor. Only "not
	// enrolled" means there is nothing to protect.
	existing, err := s.repo.GetByUserID(ctx, userID)
	switch {
	case err == nil && existing.Enabled():
		return nil, app_errors.ErrTwoFactorAlreadyEnabled
	case err != nil && !errors.Is(err, app_errors.ErrTwoFactorNotEnrolled):
		return nil, fmt.Errorf("reading the current factor: %w", err)
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: usr.Email,
	})
	if err != nil {
		return nil, err
	}

	ciphertext, err := s.encryptor.Encrypt(key.Secret())
	if err != nil {
		return nil, err
	}
	now := time.Now()
	// The statement itself refuses an enabled factor, so the read above is
	// advice and this is the decision.
	if err := s.repo.CreatePending(ctx, &two_factor.TwoFactor{
		UserID:           userID,
		SecretCiphertext: ciphertext,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		return nil, err
	}

	qr, err := qrCodeDataURI(key)
	if err != nil {
		// A missing QR is a usability problem, not a failure: the secret can
		// still be typed in by hand.
		s.logger.WarnContext(ctx, "failed to render the enrollment QR code", "error", err, "user_id", userID)
	}

	s.logger.InfoContext(ctx, "two-factor enrollment started", "user_id", userID)
	return &Enrollment{Secret: key.Secret(), OTPAuthURL: key.URL(), QRCodeDataURI: qr}, nil
}

// qrCodeDataURI renders the enrollment key as an inline PNG.
func qrCodeDataURI(key *otp.Key) (string, error) {
	img, err := key.Image(240, 240)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// Confirm activates a pending enrollment and returns the recovery codes, which
// are shown exactly once.
//
// session is the proof of the session that asked. Enabling a factor is a
// durable credential write, so it is persisted under the same session-bound
// boundary as an OIDC link and a passkey: the step-up guard
// proved the session before the handler ran, and a sign-out or a recovery
// landing between that check and the write must leave the factor pending.
// A missing proof is refused outright — an absent session is not a live one.
func (s *Service) Confirm(ctx context.Context, userID uuid.UUID, code string, session services.SessionProof) ([]string, error) {
	if session.FamilyID == uuid.Nil || session.IssuedAt.IsZero() {
		s.logger.WarnContext(ctx, "two-factor confirmation refused: no session proof", "user_id", userID)
		return nil, app_errors.ErrTokenInvalid
	}
	factor, err := s.repo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if factor.Enabled() {
		return nil, app_errors.ErrTwoFactorAlreadyEnabled
	}

	secret, err := s.encryptor.Decrypt(factor.SecretCiphertext)
	if err != nil {
		return nil, err
	}
	step, matched := s.matchTOTPStep(code, secret)
	if !matched {
		return nil, app_errors.ErrTwoFactorInvalidCode
	}

	plain, hashes, err := two_factor.GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	factor.Confirm(hashes)
	// Conditional on the exact pending secret this code was verified
	// against: a second confirmation, or a re-enrolment that replaced the
	// secret meanwhile, writes nothing and gets no recovery codes. And
	// under the caller's session proof on the user's row lock:
	// ErrTokenInvalid means the session ended
	// first and the factor stays pending.
	if err := s.repo.ConfirmPendingWhileSessionLive(ctx, factor, factor.SecretCiphertext,
		session.FamilyID, session.IssuedAt, session.CredentialGeneration); err != nil {
		if errors.Is(err, app_errors.ErrTokenInvalid) {
			s.logger.InfoContext(ctx, "two-factor confirmation refused: the session ended before the factor was enabled",
				"user_id", userID, "family_id", session.FamilyID)
		}
		return nil, err
	}
	// Record the step, do NOT check it. Enrolment validates against the PENDING
	// secret and refuses once the factor is enabled, so this call cannot itself
	// be replayed — but recording closes the enrolment-code-to-Verify hop.
	// Best-effort: the factor is already enabled, and failing here would leave
	// the user enrolled with an error and no recovery codes.
	if err := s.repo.RecordTOTPStep(ctx, userID, step); err != nil {
		s.logger.WarnContext(ctx, "failed to record the enrolment code's time step", "user_id", userID, "error", err)
	}

	s.logger.InfoContext(ctx, "two-factor enabled", "user_id", userID)
	return plain, nil
}

// Verify checks a login-time code, accepting either a TOTP code or one of the
// single-use recovery codes.
//
// A consumed recovery code is removed immediately, so a replay fails even if
// the same code is submitted twice in the same second.
func (s *Service) Verify(ctx context.Context, userID uuid.UUID, code string) error {
	if s.failures.exhausted(userID) {
		s.logger.WarnContext(ctx, "two-factor verification locked out after repeated failures", "user_id", userID)
		return app_errors.ErrTwoFactorTooManyAttempts
	}

	factor, err := s.repo.GetByUserID(ctx, userID)
	if err != nil {
		return err
	}
	if !factor.Enabled() {
		return app_errors.ErrTwoFactorNotEnrolled
	}

	secret, err := s.encryptor.Decrypt(factor.SecretCiphertext)
	if err != nil {
		return err
	}
	if step, matched := s.matchTOTPStep(code, secret); matched {
		// RFC 6238 anti-replay: a code is valid for up to 90 seconds with skew,
		// and nothing recorded that it had been spent — so a code read over a
		// shoulder, or lifted from a phished form, worked again for the rest of
		// its window. The step-up path was the worse of the two: one read-out
		// code minted a five-minute confirmation ticket.
		//
		// The ledger is keyed on (user, step) and NOT on purpose. Purpose-keying
		// looks safer and is not: the attack is a code the victim already used
		// being replayed to a DIFFERENT endpoint, which a purpose-scoped ledger
		// leaves open.
		consumed, cerr := s.repo.ConsumeTOTPStep(ctx, userID, step)
		if cerr != nil {
			return cerr
		}
		if !consumed {
			// A distinct error that deliberately does NOT feed failures.record:
			// a user who double-submits a form would otherwise burn their
			// lockout budget on their own valid code.
			s.logger.WarnContext(ctx, "two-factor code replayed within its window", "user_id", userID)
			return app_errors.ErrTwoFactorCodeAlreadyUsed
		}
		s.failures.reset(userID)
		return nil
	}

	// Single-use is enforced by the database, not by rewriting the array from a
	// value read moments ago — two concurrent logins with the same code would
	// otherwise both succeed and leave the code live.
	consumed, err := s.repo.ConsumeRecoveryCode(ctx, userID, two_factor.HashRecoveryCode(code))
	if err != nil {
		return err
	}
	if consumed {
		s.failures.reset(userID)
		s.logger.WarnContext(ctx, "two-factor recovery code used", "user_id", userID)
		return nil
	}

	s.failures.record(userID)
	return app_errors.ErrTwoFactorInvalidCode
}

// Disable removes the factor. The caller must have already re-proved identity
// (a current code or the password) — this method does not decide that.
func (s *Service) Disable(ctx context.Context, userID uuid.UUID) error {
	if err := s.repo.Delete(ctx, userID); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "two-factor disabled", "user_id", userID)
	return nil
}

// RemainingRecoveryCodes reports how many single-use codes are left, so the UI
// can nag before the user runs out.
func (s *Service) RemainingRecoveryCodes(ctx context.Context, userID uuid.UUID) int {
	factor, err := s.repo.GetByUserID(ctx, userID)
	if err != nil {
		return 0
	}
	return len(factor.RecoveryCodeHashes)
}

func (s *Service) validateTOTP(code, secret string) bool {
	_, ok := s.matchTOTPStep(code, secret)
	return ok
}

// matchTOTPStep returns WHICH time step a code matched, not just whether it did.
//
// The library's boolean cannot tell us, and guessing is how a valid code gets
// refused — so the skew window is walked here, one step at a time, in the order
// now, now+1, now-1. An error from the library is treated as "no match" (never
// as a match), preserving the error-means-false rule the caller relied on.
func (s *Service) matchTOTPStep(code, secret string) (int64, bool) {
	now := time.Now()
	for _, offset := range totpSkewOffsets() {
		at := now.Add(time.Duration(offset) * totpPeriod * time.Second)
		ok, err := totp.ValidateCustom(code, secret, at, totp.ValidateOpts{
			Period: totpPeriod,
			// Zero skew: the window is walked explicitly above, so a match
			// identifies exactly one step.
			Skew:   0,
			Digits: 6,
		})
		if err == nil && ok {
			return at.Unix() / totpPeriod, true
		}
	}
	return 0, false
}

// totpSkewOffsets is the accept window in steps, nearest first.
func totpSkewOffsets() []int {
	offsets := []int{0}
	for i := 1; i <= skew; i++ {
		offsets = append(offsets, i, -i)
	}
	return offsets
}
