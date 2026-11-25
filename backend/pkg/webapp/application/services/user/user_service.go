package user

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_beta_notice_ack"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_password_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/user_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// emailVerifier mails a fresh proof-of-mailbox link. Optional: when unset (no
// mail transport configured) an email change simply leaves the user unverified.
type emailVerifier interface {
	SendFor(ctx context.Context, usr *user.User) error
}

// changeNotifier delivers plain notification mail. Declared here rather than in
// ports/ because it is an outbound port of this service alone (the
// email_verification precedent); the SMTP adapter lives in
// infrastructure/mailer. Optional, like the verifier: with no mail transport an
// email change simply sends no notice.
type changeNotifier interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Service implements the UserService interface
type Service struct {
	userRepo  repository.UserRepository
	auditRepo repository.AuditEventRepository
	verifier  emailVerifier
	notifier  changeNotifier
	hasher    services.PasswordHasher
	logger    *slog.Logger
}

// NewUserService creates a new UserService instance
func NewUserService(
	userRepo repository.UserRepository,
	auditRepo repository.AuditEventRepository,
	logger *slog.Logger,
) *Service {
	if userRepo == nil {
		panic("user.Service requires a user repository")
	}
	if auditRepo == nil {
		panic("user.Service requires an audit event repository")
	}
	return &Service{
		userRepo:  userRepo,
		auditRepo: auditRepo,
		// Cost 12, matching auth, member and password_reset. This service used
		// to call bcrypt.GenerateFromPassword with bcrypt.DefaultCost (10)
		// directly, so a user who registered at 12 and later changed their
		// password in-app silently ended up at 10, with nothing to restore it.
		hasher: services.NewBCryptPasswordHasher(12),
		logger: logger.With("service", "user_service"),
	}
}

// SetEmailVerifier wires verification mail for email changes (optional; wired
// after construction because the verifier depends on the user repository too).
func (s *Service) SetEmailVerifier(verifier emailVerifier) {
	s.verifier = verifier
}

// SetChangeNotifier wires the notice mailed to the PREVIOUS address when the
// email changes (optional; nil disables it).
func (s *Service) SetChangeNotifier(notifier changeNotifier) {
	s.notifier = notifier
}

// GetByID retrieves the current user by ID
func (s *Service) GetByID(ctx context.Context, userID uuid.UUID) (*user.User, error) {
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, app_errors.ErrUserNotFound
	}
	return usr, nil
}

// ListByAccountID retrieves all users belonging to the requesting account
func (s *Service) ListByAccountID(ctx context.Context, query user_list.UserListQuery) ([]*user.User, error) {
	// Limit -1 cancels the GORM limit clause: return the whole team.
	users, _, err := s.userRepo.ListByAccountID(ctx, query.AuthContext.AccountID, 0, -1)
	if err != nil {
		return nil, err
	}
	return users, nil
}

// UpdateMe updates the current user's profile information
func (s *Service) UpdateMe(ctx context.Context, cmd user_update.UserUpdateCommand, session services.SessionProof) (*user.User, error) {
	s.logger.Info("updating user", "id", cmd.ID, "account_id", cmd.AuthContext.AccountID, "user_id", cmd.AuthContext.UserID)

	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	// The email is the sign-in identifier, so this write persists under the
	// caller's session proof like every other credential write. An absent
	// proof is refused before anything
	// is read: it is not a live session, and a path that forgot to pass one
	// must fail here rather than write.
	if err := requireSessionProof(session); err != nil {
		s.logger.WarnContext(ctx, "profile update refused: no session proof", "user_id", cmd.ID)
		return nil, err
	}

	// Validate that the user is updating their own profile
	if cmd.AuthContext.UserID != cmd.ID {
		s.logger.Warn("unauthorized user update attempt",
			"user_id", cmd.ID,
			"requesting_user_id", cmd.AuthContext.UserID,
			"account_id", cmd.AuthContext.AccountID)
		return nil, app_errors.ErrUnauthorized
	}

	// Get the existing user. The self-check above is the ownership check:
	// users are global identities, no longer pinned to one account.
	existingUser, err := s.userRepo.GetByID(ctx, cmd.ID)
	if err != nil {
		return nil, app_errors.ErrUserNotFound
	}

	// A nil field means "unchanged" — see the command's doc comment for why
	// this is a partial update and not a replace — and it travels to the
	// repository AS nil: the statement COALESCEs an omitted field to the
	// stored column, so there is no snapshot value here to write back over a
	// concurrent change (a name-only update that read before another
	// request's email change would otherwise restore the previous address).
	// The email is normalized before persist (signup
	// already normalizes via NewUser; raw bytes here could slip a
	// case-variant duplicate past the byte-wise unique index). Whether the
	// address CHANGED is the statement's verdict against the stored value,
	// not this snapshot's: a REAL change clears the verification stamp —
	// proven mailbox ownership no longer holds for the new address, and
	// email_verified_at gates silent OIDC→local linking and any
	// operator tool that trusts a verified address — and advances the credential
	// generation; a case-only change of the same address does neither.
	var email *string
	if cmd.Email != nil {
		normalized := user.NormalizeEmail(*cmd.Email)
		email = &normalized
	}

	// Column-specific, never the whole row: a copy of the user read a moment
	// ago must not write the password hash and PasswordChangedAt back over a
	// reset that landed in between. And under the session
	// proof on the row lock: ErrTokenInvalid means the session ended between
	// the step-up guard and this statement, and nothing was written.
	change, err := s.userRepo.UpdateProfileWhileSessionLive(ctx, cmd.ID, cmd.Name, email,
		session.FamilyID, session.IssuedAt, session.CredentialGeneration)
	if err != nil {
		if errors.Is(err, app_errors.ErrTokenInvalid) {
			s.logger.InfoContext(ctx, "profile update refused: the session ended before the write",
				"user_id", cmd.ID, "family_id", session.FamilyID)
		}
		return nil, err
	}
	emailChanged := change.EmailChanged
	updatedUser := *existingUser
	if cmd.Name != nil {
		updatedUser.Name = *cmd.Name
	}
	if email != nil {
		updatedUser.Email = *email
		if emailChanged {
			updatedUser.EmailVerifiedAt = nil
		}
	}
	updatedUser.UpdatedAt = time.Now()
	if fresh, ferr := s.userRepo.GetByID(ctx, cmd.ID); ferr == nil {
		updatedUser = *fresh
	}

	// Re-prove the new address. Delivery failures are logged, never returned:
	// the profile update did succeed, and the user can resend from the UI.
	// No mail delivery configured is the install's shape, not a failure.
	if emailChanged && s.verifier != nil {
		if verr := s.verifier.SendFor(ctx, &updatedUser); verr != nil {
			if errors.Is(verr, app_errors.ErrEmailNotConfigured) {
				s.logger.DebugContext(ctx, "verification for changed email not sent: email delivery is not configured",
					"user_id", updatedUser.ID)
			} else {
				s.logger.WarnContext(ctx, "failed to send verification for changed email",
					"error", verr, "user_id", updatedUser.ID)
			}
		}
	}

	// Tell the PREVIOUS address its sign-in identifier was repointed. The old
	// mailbox is the one an evicted owner still reads — password reset resolves
	// by the NEW address, so this notice is the only signal that reaches them.
	// The address comes from the statement's locked read, not from the
	// snapshot above, which may predate another request's change.
	// Best-effort like the verification mail: the update already happened.
	if emailChanged && s.notifier != nil {
		if nerr := s.notifier.Send(ctx, change.PreviousEmail,
			"Your Orchestrator email address was changed",
			emailChangedBody(existingUser.Name, updatedUser.Email)); nerr != nil {
			s.logger.WarnContext(ctx, "failed to notify previous address of email change",
				"error", nerr, "user_id", updatedUser.ID)
		}
	}

	return &updatedUser, nil
}

// emailChangedBody is the notice mailed to the address being replaced. It names
// the new address so the legitimate owner can tell their own edit from a
// takeover, and it does not suggest a password reset — reset mail resolves by
// the NEW address, so for a hijacked account that advice would be useless.
func emailChangedBody(name, newEmail string) string {
	greeting := "Hello,"
	if n := strings.TrimSpace(name); n != "" {
		greeting = "Hello " + n + ","
	}
	return greeting + `

The email address on your Orchestrator account was just changed to:

` + newEmail + `

Sign-in and account recovery now use that address; this mailbox is no longer
attached to the account.

If you made this change, no action is needed.

If you did NOT make this change, someone with access to your session performed
it — contact the operator of your Orchestrator instance immediately and mention
this message.
`
}

// UpdatePassword updates the user's password
func (s *Service) UpdatePassword(ctx context.Context, cmd user_password_update.UserUpdatePasswordCommand, session services.SessionProof) (*user.User, error) {
	s.logger.Info("updating user password", "id", cmd.ID, "account_id", cmd.AuthContext.AccountID, "user_id", cmd.AuthContext.UserID)

	// Persisted under the caller's session proof;
	// an absent proof is refused up front, see UpdateMe.
	if err := requireSessionProof(session); err != nil {
		s.logger.WarnContext(ctx, "password change refused: no session proof", "user_id", cmd.ID)
		return nil, err
	}

	// user.MinPasswordLength documents itself as the floor for "registration,
	// invite acceptance, password reset and change-password". Three of those
	// four enforced it; this one — the only path an already-signed-in user
	// reaches — did not, and its `validate:"min=8"` tag never ran because no
	// echo.Validator is installed. The browser's minLength={8} was the whole
	// floor, so any API client could set a one-character password.
	if len(cmd.Password) == 0 {
		return nil, app_errors.ErrUserPasswordRequired
	}
	if len(cmd.Password) < user.MinPasswordLength {
		return nil, app_errors.ErrUserPasswordTooShort
	}
	if len(cmd.Password) > user.MaxPasswordLength {
		return nil, app_errors.ErrUserPasswordTooLong
	}

	// Validate that the user is updating their own password
	if cmd.AuthContext.UserID != cmd.ID {
		s.logger.Warn("unauthorized password update attempt",
			"user_id", cmd.ID,
			"requesting_user_id", cmd.AuthContext.UserID,
			"account_id", cmd.AuthContext.AccountID)
		return nil, app_errors.ErrUnauthorized
	}

	// Get the existing user. The self-check above is the ownership check:
	// users are global identities, no longer pinned to one account.
	existingUser, err := s.userRepo.GetByID(ctx, cmd.ID)
	if err != nil {
		return nil, app_errors.ErrUserNotFound
	}

	// Hash the new password
	hashedPassword, err := s.hashPassword(cmd.Password)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	// Only the password columns, and the credential generation with them in
	// the same statement. Refresh tokens issued before this
	// moment become invalid. Under the session proof on the row lock:
	// ErrTokenInvalid means the session ended between the step-up guard and
	// this statement, and the password is unchanged.
	if err := s.userRepo.SetPasswordWhileSessionLive(ctx, cmd.ID, hashedPassword, now,
		session.FamilyID, session.IssuedAt, session.CredentialGeneration); err != nil {
		if errors.Is(err, app_errors.ErrTokenInvalid) {
			s.logger.InfoContext(ctx, "password change refused: the session ended before the write",
				"user_id", cmd.ID, "family_id", session.FamilyID)
		}
		return nil, err
	}
	existingUser.HashedPassword = hashedPassword
	existingUser.PasswordChangedAt = &now
	existingUser.UpdatedAt = now
	existingUser.CredentialGeneration++

	return existingUser, nil
}

// requireSessionProof is the fail-closed gate on an absent proof: a nil
// family or a zero issue time is no session, whatever the middleware said.
func requireSessionProof(session services.SessionProof) error {
	if session.FamilyID == uuid.Nil || session.IssuedAt.IsZero() {
		return app_errors.ErrTokenInvalid
	}
	return nil
}

// AcknowledgeBetaNotice records the caller's acceptance of the operator's
// notice or terms of use — sent with a sign-up, or by the acknowledgement
// endpoint. Idempotent: accepting twice just refreshes the stamp.
//
// The audit event, not the user column, is the record of consent — the column
// is overwritten by the next revision, while the log keeps every acceptance
// with its own timestamp. An audit write failure is therefore NOT swallowed
// here (unlike everywhere else in this codebase): a stored acknowledgement with
// no corresponding event is exactly the evidence gap this endpoint exists to
// close, so the event is written first and a failure aborts the whole thing.
// The reverse gap — an event whose user update then failed — is harmless and
// self-correcting: the user is simply asked again, and accepting logs a second
// event. Extra evidence is fine; missing evidence is not.
func (s *Service) AcknowledgeBetaNotice(
	ctx context.Context,
	cmd user_beta_notice_ack.UserBetaNoticeAckCommand,
) (*user.User, error) {
	if cmd.AuthContext.UserID != cmd.ID {
		s.logger.Warn("unauthorized terms acknowledgement attempt",
			"user_id", cmd.ID,
			"requesting_user_id", cmd.AuthContext.UserID)
		return nil, app_errors.ErrUnauthorized
	}

	existingUser, err := s.userRepo.GetByID(ctx, cmd.ID)
	if err != nil {
		return nil, app_errors.ErrUserNotFound
	}

	event := audit_event.NewUserEvent(
		cmd.AuthContext.AccountID,
		cmd.AuthContext.UserID,
		audit_event.ActionBetaNoticeAcknowledge,
		audit_event.TargetUser,
		cmd.ID.String(),
	)
	if err := s.auditRepo.Create(ctx, event); err != nil {
		s.logger.ErrorContext(ctx, "failed to record terms acknowledgement",
			"error", err, "user_id", cmd.ID)
		return nil, err
	}

	updatedUser := *existingUser
	updatedUser.AcknowledgeBetaNotice()
	// The two acknowledgement columns only. This route needs no step-up, so
	// it was the cheapest way to race a whole-row write against the owner's
	// password recovery and put the old hash back.
	if err := s.userRepo.AcknowledgeBetaNotice(ctx, cmd.ID, updatedUser.BetaNoticeAckVersion, *updatedUser.BetaNoticeAckAt); err != nil {
		return nil, err
	}

	s.logger.Info("terms acknowledged",
		"user_id", cmd.ID, "version", updatedUser.BetaNoticeAckVersion)
	return &updatedUser, nil
}

// hashPassword hashes a password through the shared hasher, so there is one
// cost constant in the codebase rather than one per service.
func (s *Service) hashPassword(password string) (string, error) {
	return s.hasher.HashPassword(password)
}

// VerifyPassword verifies a password against a hash. Verification reads the
// cost from the hash itself, so hashes written at the old cost keep working.
func (s *Service) VerifyPassword(hashedPassword, password string) error {
	return s.hasher.ComparePasswords(hashedPassword, password)
}
