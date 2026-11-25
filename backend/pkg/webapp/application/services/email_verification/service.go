// Package email_verification issues and confirms the one-time links that prove
// a user controls their email address.
package email_verification

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/email_verification"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/google/uuid"
)

const (
	// resendWindow / maxSendsPerWindow throttle how often one user can be
	// mailed. This protects the recipient's inbox (and our sending reputation)
	// from being used as an amplifier, independent of the per-IP HTTP limit.
	resendWindow      = 1 * time.Hour
	maxSendsPerWindow = 5
	// verifyPath is the SPA route that posts the token back.
	verifyPath = "/verify-email"
)

// Sender delivers a message. Declared here rather than in ports/ because it is
// an outbound port of this service alone (see the eeprom_provision precedent);
// the SMTP adapter lives in infrastructure/mailer.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Service mints, delivers and confirms verification links.
//
// A nil Sender (or an unconfigured deployment) disables delivery gracefully:
// nothing is minted or sent, users simply stay unverified. On the first-owner,
// invite-code and invitation paths verification is an enhancement — it
// unlocks the silent OIDC link — so its absence must never block those
// sign-ups or an email change. Open registration is the exception: an account
// it creates is usable only once verified, so without delivery the auth
// service keeps open registration closed.
type Service struct {
	repo       repository.EmailVerificationRepository
	userRepo   repository.UserRepository
	sender     Sender
	appBaseURL string
	logger     *slog.Logger
}

func NewService(repo repository.EmailVerificationRepository, userRepo repository.UserRepository, sender Sender, appBaseURL string, logger *slog.Logger) *Service {
	if repo == nil {
		panic("email_verification.Service requires a verification repository")
	}
	if userRepo == nil {
		panic("email_verification.Service requires a user repository")
	}
	return &Service{
		repo:       repo,
		userRepo:   userRepo,
		sender:     sender,
		appBaseURL: strings.TrimRight(strings.TrimSpace(appBaseURL), "/"),
		logger:     logger.With("service", "email_verification"),
	}
}

// Enabled reports whether verification mail can actually be delivered.
func (s *Service) Enabled() bool {
	return s.sender != nil && s.appBaseURL != ""
}

// SendFor mints a fresh link for the user's current address and mails it,
// revoking any outstanding link first so only the newest one works.
//
// Already-verified users are a no-op. Callers on the signup / email-change path
// should ignore the error: a mail failure must not fail the operation that
// triggered it.
func (s *Service) SendFor(ctx context.Context, usr *user.User) error {
	if !s.Enabled() {
		return app_errors.ErrEmailNotConfigured
	}
	if usr.EmailVerifiedAt != nil {
		return app_errors.ErrEmailAlreadyVerified
	}

	count, err := s.repo.CountRecentByUserID(ctx, usr.ID, time.Now().Add(-resendWindow))
	if err != nil {
		return err
	}
	if count >= maxSendsPerWindow {
		return app_errors.ErrEmailSendThrottled
	}

	// Revoke outstanding links: after a resend or an address change, only the
	// newest link may verify. Revoking marks the prior row spent rather than
	// removing it — the count above reads this same table, so deleting would
	// hide every send from the throttle bounding it.
	if err := s.repo.RevokeUnusedByUserID(ctx, usr.ID); err != nil {
		return err
	}

	verification, token, err := email_verification.New(usr.ID, usr.Email, email_verification.DefaultTTL)
	if err != nil {
		return err
	}
	if err := s.repo.Create(ctx, verification); err != nil {
		return err
	}

	link := fmt.Sprintf("%s%s?token=%s", s.appBaseURL, verifyPath, url.QueryEscape(token))
	if err := s.sender.Send(ctx, usr.Email, "Confirm your email address", verificationBody(usr.Name, link)); err != nil {
		s.logger.ErrorContext(ctx, "failed to send verification email", "error", err, "user_id", usr.ID)
		return err
	}
	s.logger.InfoContext(ctx, "verification email sent", "user_id", usr.ID)
	return nil
}

// SendForUserID is the resend entry point: it loads the user first.
func (s *Service) SendForUserID(ctx context.Context, userID uuid.UUID) error {
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	return s.SendFor(ctx, usr)
}

// Confirm consumes a token and stamps the user's verification.
//
// The address is re-checked: a link minted for an address the user has since
// changed must not verify the new one. Consumption is atomic in the repository,
// so a double click cannot verify twice.
func (s *Service) Confirm(ctx context.Context, token string) error {
	verification, err := s.repo.ClaimByHash(ctx, email_verification.Hash(token))
	if err != nil {
		return err
	}
	usr, err := s.userRepo.GetByID(ctx, verification.UserID)
	if err != nil {
		return app_errors.ErrEmailVerificationInvalid
	}
	if user.NormalizeEmail(usr.Email) != user.NormalizeEmail(verification.Email) {
		s.logger.WarnContext(ctx, "verification link is for a stale address", "user_id", usr.ID)
		return app_errors.ErrEmailVerificationStale
	}
	if usr.EmailVerifiedAt != nil {
		return nil // idempotent
	}
	// Conditional on the stored address still being the one the link proved,
	// and on nothing else in the row: a whole-row write from this copy could
	// carry a stale password hash over a concurrent reset.
	written, err := s.userRepo.MarkEmailVerified(ctx, usr.ID, verification.Email, time.Now())
	if err != nil {
		return err
	}
	if !written {
		s.logger.WarnContext(ctx, "verification link no longer matches the stored address", "user_id", usr.ID)
		return app_errors.ErrEmailVerificationStale
	}
	s.logger.InfoContext(ctx, "email verified", "user_id", usr.ID)
	return nil
}

func verificationBody(name, link string) string {
	greeting := "Hello,"
	if n := strings.TrimSpace(name); n != "" {
		greeting = "Hello " + n + ","
	}
	return greeting + `

Confirm this email address to finish setting up your Orchestrator account:

` + link + `

The link is valid for 24 hours and can be used once. If you did not create an
account, you can ignore this message — nothing will happen until the link is
opened.
`
}
