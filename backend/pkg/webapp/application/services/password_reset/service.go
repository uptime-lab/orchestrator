// Package password_reset issues and consumes the one-time links that let a
// user who has lost their password set a new one.
//
// Two properties drive the whole design:
//
//   - The request endpoint must not be a user-enumeration oracle. Every caller
//     gets the same answer whether or not the address exists, so the decision
//     to actually mail anything is made silently, inside this service.
//   - A reset is a credential change, not a login. Consuming a link stamps
//     PasswordChangedAt, which the auth service already uses to reject refresh
//     tokens issued earlier — so a reset kicks whoever was holding a stolen
//     session, which is exactly what a user resetting under duress expects.
package password_reset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/password_reset"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/google/uuid"
)

const (
	// resendWindow / maxSendsPerWindow bound how often one mailbox can be
	// mailed. This protects the recipient's inbox (and our sending reputation)
	// from being used as an amplifier, independent of the per-IP HTTP limit.
	// Tighter than verification's: a reset is unusual, five per hour is
	// already generous.
	resendWindow      = 1 * time.Hour
	maxSendsPerWindow = 3
	// resetPath is the SPA route that collects the new password and posts the
	// token back.
	resetPath = "/reset-password"
)

// Sender delivers a message. Declared here rather than in ports/ because it is
// an outbound port of this service alone (mirrors email_verification); the SMTP
// adapter lives in infrastructure/mailer.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Service mints, delivers and consumes password-reset links.
//
// A nil Sender (or an unconfigured deployment) disables the flow gracefully:
// nothing is minted or sent. An install without SMTP resets passwords from
// the host with the server binary's --reset-password (cmd/server,
// services/credential_reset), or through an operator console outside this
// tree where one exists.
// sessionRevoker kills every session a user holds (satisfied by the auth
// service).
type sessionRevoker interface {
	RevokeAllSessions(ctx context.Context, userID uuid.UUID) error
}

type Service struct {
	repo     repository.PasswordResetRepository
	userRepo repository.UserRepository
	// revoker drops stored refresh tokens on a completed reset. Optional only
	// so the composition root can wire it after the auth service exists.
	revoker    sessionRevoker
	sender     Sender
	hasher     services.PasswordHasher
	appBaseURL string
	logger     *slog.Logger
}

func NewService(
	repo repository.PasswordResetRepository,
	userRepo repository.UserRepository,
	sender Sender,
	appBaseURL string,
	logger *slog.Logger,
) *Service {
	if repo == nil {
		panic("password_reset.Service requires a password reset repository")
	}
	if userRepo == nil {
		panic("password_reset.Service requires a user repository")
	}
	return &Service{
		repo:       repo,
		userRepo:   userRepo,
		sender:     sender,
		hasher:     services.NewBCryptPasswordHasher(12),
		appBaseURL: strings.TrimRight(strings.TrimSpace(appBaseURL), "/"),
		logger:     logger.With("service", "password_reset"),
	}
}

// SetSessionRevoker wires session revocation into the reset (see Confirm).
func (s *Service) SetSessionRevoker(r sessionRevoker) {
	s.revoker = r
}

// Enabled reports whether reset mail can actually be delivered.
func (s *Service) Enabled() bool {
	return s.sender != nil && s.appBaseURL != ""
}

// Request mails a reset link for the address, if there is anything to mail.
//
// It returns nil in every non-infrastructure case — unknown address, throttled,
// OIDC-only account — because the caller reports a fixed success message. The
// reasons are logged, never returned: distinguishing them to the client would
// turn this endpoint into a directory of registered users.
func (s *Service) Request(ctx context.Context, email string) error {
	if !s.Enabled() {
		return app_errors.ErrEmailNotConfigured
	}

	usr, err := s.userRepo.GetByEmail(ctx, user.NormalizeEmail(email))
	if err != nil {
		s.logger.InfoContext(ctx, "password reset requested for unknown address")
		return nil
	}

	// An identity whose only credential is OIDC has no password to reset, and
	// minting one here would let mailbox control alone graft a password onto a
	// Google-only account. Those users recover through their IdP, or through
	// an operator: --reset-password on the host, or an operator console.
	if strings.TrimSpace(usr.HashedPassword) == "" {
		s.logger.InfoContext(ctx, "password reset requested for a passwordless (OIDC-only) account", "user_id", usr.ID)
		return nil
	}

	count, err := s.repo.CountRecentByUserID(ctx, usr.ID, time.Now().Add(-resendWindow))
	if err != nil {
		return err
	}
	if count >= maxSendsPerWindow {
		s.logger.WarnContext(ctx, "password reset throttled", "user_id", usr.ID)
		return nil
	}

	// Only the newest link may work: a user who requests twice should not be
	// left with two live takeover tokens in their mailbox. Revoking marks the
	// prior row spent rather than removing it — the count above reads this same
	// table, so deleting would hide every send from the throttle bounding it.
	if err := s.repo.RevokeUnusedByUserID(ctx, usr.ID); err != nil {
		return err
	}

	reset, token, err := password_reset.New(usr.ID, usr.Email, password_reset.DefaultTTL)
	if err != nil {
		return err
	}
	if err := s.repo.Create(ctx, reset); err != nil {
		return err
	}

	link := fmt.Sprintf("%s%s?token=%s", s.appBaseURL, resetPath, url.QueryEscape(token))
	if err := s.sender.Send(ctx, usr.Email, "Reset your Orchestrator password", resetBody(usr.Name, link)); err != nil {
		s.logger.ErrorContext(ctx, "failed to send password reset email", "error", err, "user_id", usr.ID)
		return err
	}
	s.logger.InfoContext(ctx, "password reset email sent", "user_id", usr.ID)

	// Opportunistic sweep — this flow is rare enough that it needs no job.
	if err := s.repo.DeleteExpired(ctx); err != nil {
		s.logger.WarnContext(ctx, "failed to prune expired password resets", "error", err)
	}
	return nil
}

// Confirm consumes a token and installs the new password.
//
// The address is re-checked against the user's current one: a link mailed to an
// address the user has since changed must not reset the account, or losing an
// old mailbox would mean losing the account.
func (s *Service) Confirm(ctx context.Context, token, newPassword string) error {
	if len(newPassword) < user.MinPasswordLength {
		return app_errors.ErrUserPasswordTooShort
	}
	// Before the claim, like the floor: a password that cannot be hashed must
	// not spend the single-use link.
	if len(newPassword) > user.MaxPasswordLength {
		return app_errors.ErrUserPasswordTooLong
	}

	reset, err := s.repo.ClaimByHash(ctx, password_reset.Hash(token))
	if err != nil {
		return err
	}
	usr, err := s.userRepo.GetByID(ctx, reset.UserID)
	if err != nil {
		return app_errors.ErrPasswordResetInvalid
	}
	if user.NormalizeEmail(usr.Email) != user.NormalizeEmail(reset.Email) {
		s.logger.WarnContext(ctx, "password reset link is for a stale address", "user_id", usr.ID)
		return app_errors.ErrPasswordResetStale
	}

	// Hash BEFORE the write, so the row is never held across bcrypt.
	hashed, err := s.hasher.HashPassword(newPassword)
	if err != nil {
		return err
	}
	now := time.Now()
	// Only the password columns, with the credential generation advanced in
	// the same statement. Invalidates every refresh token
	// issued before this moment — see AuthService.RefreshToken. A reset must
	// end sessions an attacker may hold.
	//
	// Conditional on the grant's address IN THE STATEMENT:
	// the comparison above read a snapshot, and an email change committing
	// between it and this write — the owner moving the account away from a
	// mailbox somebody else now reads — would otherwise let that mailbox's
	// grant replace the password and end the owner's sessions. The address
	// in the WHERE makes the refusal the database's, whatever landed in
	// between.
	if err := s.userRepo.SetPasswordIfEmail(ctx, usr.ID, hashed, reset.Email, now); err != nil {
		if errors.Is(err, app_errors.ErrPasswordResetStale) {
			s.logger.WarnContext(ctx, "password reset refused: the address moved before the write", "user_id", usr.ID)
		}
		return err
	}
	usr.HashedPassword = hashed
	usr.PasswordChangedAt = &now
	usr.UpdatedAt = now
	// Reaching the mailbox proved control of it, so the address is verified as
	// a side effect — it is the same proof email verification asks for.
	// Conditional on the address still being the one the link was sent to.
	if _, err := s.userRepo.MarkEmailVerified(ctx, usr.ID, usr.Email, now); err != nil {
		s.logger.WarnContext(ctx, "password reset completed but the email verification stamp failed", "error", err, "user_id", usr.ID)
	} else {
		usr.MarkEmailVerified()
	}

	// Actually end the other sessions the email promises to end.
	// PasswordChangedAt alone only blocks REFRESH; a stored refresh token that
	// is never presented for refresh, and any session resumed by other means,
	// would survive. Best-effort: the password change already succeeded, and
	// failing the reset over this would be worse than a stale session.
	if s.revoker != nil {
		if rerr := s.revoker.RevokeAllSessions(ctx, usr.ID); rerr != nil {
			s.logger.WarnContext(ctx, "password reset completed but revoking sessions failed",
				"error", rerr, "user_id", usr.ID)
		}
	}

	// Any other outstanding link is now moot; spend it rather than leave a live
	// takeover token in a mailbox the user may be resetting BECAUSE it leaked.
	if err := s.repo.RevokeUnusedByUserID(ctx, usr.ID); err != nil {
		s.logger.WarnContext(ctx, "failed to revoke remaining password resets", "error", err, "user_id", usr.ID)
	}

	s.logger.InfoContext(ctx, "password reset completed", "user_id", usr.ID)
	return nil
}

func resetBody(name, link string) string {
	greeting := "Hello,"
	if n := strings.TrimSpace(name); n != "" {
		greeting = "Hello " + n + ","
	}
	return greeting + `

Someone asked to reset the password for your Orchestrator account. Choose a new
password here:

` + link + `

The link is valid for one hour and can be used once. Resetting your password
signs out every device that is currently signed in.

Note that a passkey or authenticator app already registered on your account
keeps working — if you think someone else added one, remove it from Account
settings after signing in.

If this was not you, no action is needed — your password stays as it is, and
nothing happens until the link is opened.
`
}
