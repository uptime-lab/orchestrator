// Package credential_reset is the administrative credential reset of a
// self-hosted install: a forgotten password, or a lost authenticator app,
// put right by the operator from a shell on the host (the server binary's
// --reset-password and --reset-totp).
//
// It exists because nothing else does it there. Self-service reset needs a
// mail relay; a self-hosted install has no other reset path, for a password
// or for TOTP.
//
// Every write goes through the same repository statements the product's own
// flows use, so the invariants those carry hold here too:
//
//   - the password is written by UserRepository.SetPassword, which moves
//     hashed_password, password_changed_at and credential_generation in ONE
//     statement — never a whole-row write;
//   - the factor is removed by TwoFactorRepository.RemoveEnabledAndAdvanceGeneration,
//     which deletes the enabled row and advances the generation in one
//     transaction under the user's row lock;
//   - every stored session is then revoked by RefreshTokenRepository.RevokeAllForUser,
//     which takes the same lock.
//
// The generation advance is what makes the reset total even across process
// boundaries: refresh rotation (Rotate), session issuance (Issue) and both
// pending-login tickets compare it, so a session or a login in flight
// against the old credential is refused whether it lands before or after
// the revocation.
package credential_reset

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/credential_reset"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

var (
	// ErrAmbiguousEmail refuses an address more than one identity answers
	// to. The reset acts on exactly one person or on nobody.
	ErrAmbiguousEmail = errors.New("more than one user answers to this email address; nothing was changed")
	// ErrNoEnabledAuthenticator refuses a TOTP reset for a user with no
	// enabled authenticator app. Nothing is written, no session is ended.
	ErrNoEnabledAuthenticator = errors.New("this user has no enabled authenticator app; nothing was changed")
)

// GeneratedPasswordLength is the length of a generated temporary password.
// The alphabet has 56 symbols (no 0/O, 1/l/I), so 24 of them carry about
// 139 bits — far past guessing, still typeable from a terminal.
const GeneratedPasswordLength = 24

const passwordAlphabet = "23456789abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ"

// auditDetail names the route and nothing else. Never a password, a hash or
// a secret: audit_events is on no rewrap list.
const auditDetail = "via=server_command_line"

// Service performs the administrative resets.
type Service struct {
	users    repository.UserRepository
	factors  repository.TwoFactorRepository
	sessions repository.RefreshTokenRepository
	resets   repository.PasswordResetRepository
	members  repository.AccountMemberRepository
	hasher   services.PasswordHasher
	recorder *audit.Recorder
	logger   *slog.Logger
	now      func() time.Time
}

// NewService wires the resets. Every dependency is required.
func NewService(
	users repository.UserRepository,
	factors repository.TwoFactorRepository,
	sessions repository.RefreshTokenRepository,
	resets repository.PasswordResetRepository,
	members repository.AccountMemberRepository,
	hasher services.PasswordHasher,
	recorder *audit.Recorder,
	logger *slog.Logger,
) *Service {
	switch {
	case users == nil:
		panic("credential_reset.Service requires a user repository")
	case factors == nil:
		panic("credential_reset.Service requires a two-factor repository")
	case sessions == nil:
		panic("credential_reset.Service requires a refresh token repository")
	case resets == nil:
		panic("credential_reset.Service requires a password reset repository")
	case members == nil:
		panic("credential_reset.Service requires an account member repository")
	case hasher == nil:
		panic("credential_reset.Service requires a password hasher")
	case recorder == nil:
		panic("credential_reset.Service requires an audit recorder")
	case logger == nil:
		panic("credential_reset.Service requires a logger")
	}
	return &Service{
		users: users, factors: factors, sessions: sessions, resets: resets, members: members,
		hasher: hasher, recorder: recorder, logger: logger.With("service", "credential_reset"),
		now: time.Now,
	}
}

// ResetPassword installs a new password and ends every session of the user.
// With no password in the command one is generated and returned in the
// result, once.
//
// There is no "must change at next sign-in" flag in the user model, so the
// temporary password stays valid until the user changes it; the operator
// tells them to.
func (s *Service) ResetPassword(ctx context.Context, c cmd.ResetPasswordCommand) (*results.CredentialResetResult, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	usr, err := s.resolve(ctx, c.Email)
	if err != nil {
		return nil, err
	}

	password := c.Password
	generated := false
	if password == "" {
		if password, err = GeneratePassword(); err != nil {
			return nil, fmt.Errorf("generating a password: %w", err)
		}
		generated = true
	}
	// Hash BEFORE the write, as the self-service reset does, so the row is
	// never held across bcrypt.
	hashed, err := s.hasher.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hashing the password: %w", err)
	}
	// The one statement every password path shares: hash, change time and
	// credential generation together.
	if err := s.users.SetPassword(ctx, usr.ID, hashed, s.now()); err != nil {
		return nil, fmt.Errorf("writing the password: %w", err)
	}

	res := &results.CredentialResetResult{UserID: usr.ID}
	if generated {
		res.TemporaryPassword = password
	}
	res.SessionsRevoked = s.endSessions(ctx, usr.ID)
	// An outstanding emailed reset link is a takeover token in a mailbox the
	// user may be locked out of; spend it, as the self-service reset does.
	if err := s.resets.RevokeUnusedByUserID(ctx, usr.ID); err != nil {
		s.logger.WarnContext(ctx, "password reset done, but revoking outstanding reset links failed", "user_id", usr.ID, "error", err)
	}
	s.record(ctx, res, audit_event.ActionUserPasswordResetByOperator)
	s.logger.InfoContext(ctx, "password reset by the operator", "user_id", usr.ID, "generated", generated, "sessions_revoked", res.SessionsRevoked)
	return res, nil
}

// ResetTOTP removes the user's enabled authenticator app and its recovery
// codes, advances the credential generation and ends every session.
//
// Passkeys are left alone on purpose: a passkey is its own complete login
// (possession plus the user verification the ceremony demands), not a
// second factor bolted onto the password, and an operator who wants one
// gone has the user delete it from their account page.
func (s *Service) ResetTOTP(ctx context.Context, c cmd.ResetTOTPCommand) (*results.CredentialResetResult, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	usr, err := s.resolve(ctx, c.Email)
	if err != nil {
		return nil, err
	}
	removed, err := s.factors.RemoveEnabledAndAdvanceGeneration(ctx, usr.ID)
	if err != nil {
		return nil, fmt.Errorf("removing the authenticator: %w", err)
	}
	if !removed {
		return nil, ErrNoEnabledAuthenticator
	}
	res := &results.CredentialResetResult{UserID: usr.ID}
	res.SessionsRevoked = s.endSessions(ctx, usr.ID)
	s.record(ctx, res, audit_event.ActionUserTwoFactorResetByOperator)
	s.logger.InfoContext(ctx, "authenticator app reset by the operator", "user_id", usr.ID, "sessions_revoked", res.SessionsRevoked)
	return res, nil
}

// resolve finds exactly one identity for the address or refuses.
func (s *Service) resolve(ctx context.Context, email string) (*user.User, error) {
	found, err := s.users.ListByEmailIgnoringCase(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("looking the user up: %w", err)
	}
	switch len(found) {
	case 0:
		return nil, app_errors.ErrUserNotFound
	case 1:
		return found[0], nil
	default:
		return nil, ErrAmbiguousEmail
	}
}

// endSessions revokes every stored session of the user.
//
// The server's PENDING logins — a two-factor challenge or an account
// selection in progress — are tickets held in the memory of the running
// server process (auth.ticketStore), which this command, a separate
// process, cannot reach. They need no reaching: both carry the credential
// generation they were issued under, CompleteTwoFactor and SelectAccount
// compare it with the row, and RefreshTokenRepository.Issue re-reads it
// under the row lock before storing any session — so the generation this
// reset has already advanced refuses them.
//
// Access tokens already issued are stateless and verify until they expire
// (JWT_ACCESS_TOKEN_TTL, 15 minutes by default), exactly as after the
// product's own "sign out everywhere"; every route that proves a live
// session refuses them at once.
func (s *Service) endSessions(ctx context.Context, userID uuid.UUID) bool {
	if err := s.sessions.RevokeAllForUser(ctx, userID); err != nil {
		s.logger.ErrorContext(ctx, "credential changed, but revoking the stored sessions failed; the advanced credential generation still refuses them at refresh",
			"user_id", userID, "error", err)
		return false
	}
	return true
}

// record writes the event under every account the user is a member of, as
// the system actor: the operator at the host has no session and no user id,
// and attributing the row to the user being reset would be false.
//
// "Member" means every membership that still exists, active OR disabled: a
// disabled member keeps their bindings and may be re-enabled, so their
// account must see the credential change. A REMOVED member has no row —
// removal is a DELETE (AccountMemberRepository.Delete), and the statuses are
// only active and disabled — so ListByUserID never returns one. The result
// reports zero for a user with no membership (or an unreadable list), and
// the command line says the reset is recorded nowhere.
func (s *Service) record(ctx context.Context, res *results.CredentialResetResult, action string) {
	userID := res.UserID
	memberships, err := s.members.ListByUserID(ctx, userID)
	if err != nil {
		s.logger.WarnContext(ctx, "could not list the user's accounts; the reset is NOT in any audit trail", "user_id", userID, "error", err)
		res.MembershipsUnreadable = true
		return
	}
	sessionsRevoked := res.SessionsRevoked
	detail := fmt.Sprintf("%s sessions_revoked=%t", auditDetail, sessionsRevoked)
	seen := make(map[uuid.UUID]struct{}, len(memberships))
	for _, m := range memberships {
		if _, dup := seen[m.AccountID]; dup {
			continue
		}
		seen[m.AccountID] = struct{}{}
		s.recorder.RecordDetail(ctx, audit.SystemActor(m.AccountID), action, audit_event.TargetUser, userID.String(), detail)
	}
	res.AuditedAccounts = len(seen)
}

// GeneratePassword returns a random temporary password from crypto/rand.
func GeneratePassword() (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, GeneratedPasswordLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}
