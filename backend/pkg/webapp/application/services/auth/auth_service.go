package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_beta_notice_ack"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_register"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// preAuthTokenTTL bounds the account-selection window after password login.
const preAuthTokenTTL = 5 * time.Minute

// productionBcryptCost is the cost this service hashes and compares at. It is
// also the cost dummyPasswordHash was generated at; the two must stay equal or
// the dummy comparison becomes a timing signal of its own.
const productionBcryptCost = 12

// dummyPasswordHash is what the credential paths compare against when there is
// no stored hash to compare against — an email nobody holds, or an account
// provisioned through OIDC that never had a password. Both branches used to
// return before bcrypt ran, so the response TIME answered the question the
// uniform error body refuses to: a few hundred milliseconds meant "this address
// has a local password account", microseconds meant it does not.
//
// The plaintext behind it is deliberately not recorded anywhere: nothing may
// ever match it.
const dummyPasswordHash = "$2a$12$i8yVyKE7PnGAq7kfFlRsueBZhibPqmh9jEToaEwvWH7uvYNmehKhe"

// AuthService implements the authentication service
type AuthService struct {
	jwtConfig  config.JWTConfig
	inviteCode string
	// maxAccounts is the self-registration ceiling (0 = unlimited). It backs up
	// the invite code, which says WHO may register and nothing about how many.
	maxAccounts int
	// openRegistration admits a new organisation without any code
	// (SERVER_OPEN_REGISTRATION). It takes precedence over inviteCode, and
	// never over maxAccounts.
	openRegistration OpenRegistration
	// settledAccounts remembers accounts the open-signup gate found settled.
	// A settled account never becomes pending again (the mark is written only
	// at creation), so the gate reads each account at most once per process.
	settledAccounts sync.Map
	// now and runAsync are seams for tests: the clock the pending window is
	// measured on, and how the open sign-up's confirmation mail is sent off
	// the request. nil means time.Now and a goroutine.
	now             func() time.Time
	runAsync        func(func())
	userRepo        repository.UserRepository
	accountRepo     repository.AccountRepository
	memberRepo      repository.AccountMemberRepository
	bindingRepo     repository.RoleBindingRepository
	inviteRepo      repository.AccountInviteRepository
	extIdentityRepo repository.ExternalIdentityRepository
	// passkeys counts a user's WebAuthn credentials for the last-credential
	// guard in UnlinkOIDC. Required by the constructor; the guard fails CLOSED
	// if it is somehow absent, because "I could not count your other
	// credentials" must never resolve to "so go ahead and remove this one".
	passkeys passkeyCounter
	// credLock serializes UnlinkOIDC's last-credential guard against the other
	// removal paths. Optional; absent means the guard runs unprotected.
	credLock       userLocker
	accountService services.AccountService
	passwordHasher services.PasswordHasher
	// emailVerifier mails proof-of-mailbox links on password signup. Optional:
	// nil when no mail transport is configured.
	emailVerifier emailVerifier
	// terms records a terms-of-use acceptance sent with a sign-up. Optional:
	// absent, an acceptance is logged and not stored.
	terms termsRecorder
	// refreshTokens records issued refresh credentials so they can be rotated
	// and revoked. Required — a nil store would silently restore the
	// unrevocable stateless behaviour this replaced.
	refreshTokens repository.RefreshTokenRepository
	// twoFactor gates password logins for users who enrolled a second factor.
	// Required rather than optional: a nil gate would silently let everyone
	// past the factor they deliberately turned on.
	twoFactor twoFactorGate
	// twoFactorTickets makes the second-factor ticket single-use.
	twoFactorTickets *ticketStore
	// preAuthTickets makes the account-selection ticket single-use. Without it
	// the pre-auth JWT was replayable for its whole TTL and, being stateless,
	// outlived a password reset and a sign-out-everywhere: a challenge captured
	// during a login could mint a fresh session AFTER the recovery.
	// SelectAccount also refuses a ticket issued before
	// PasswordChangedAt, the same rule refresh tokens follow.
	preAuthTickets *ticketStore
	// maxSessions caps concurrent logins per user; 0 is unlimited. See
	// session.go. anomalyRepo is the optional operator feed for the
	// session-growth signal.
	maxSessions int
	anomalyRepo repository.InstanceAnomalyRepository
	// authBurst counts recent credential failures per source; see auth_burst.go.
	authBurst *authBurstTracker
	// loginThrottle slows password guessing against one address; see
	// login_throttle.go.
	loginThrottle *loginThrottle
	// refreshGrace answers a second presentation of a just-rotated refresh
	// token from the same client; see refresh_grace.go.
	refreshGrace *refreshGrace
	// invites accepts an invite on the federated sign-in path with the same
	// checks, seat cap and audit event as the invite link. Wired after
	// construction (the member service is built later and depends on this
	// one); absent, that path refuses rather than accept unchecked.
	invites inviteAcceptor
	logger  *slog.Logger
}

// inviteAcceptor is the member service's one shared invite acceptance.
type inviteAcceptor interface {
	AcceptInviteRecord(ctx context.Context, usr *user.User, invite *account_invite.AccountInvite) error
}

// SetInviteAcceptor wires the invite acceptance used when a federated
// sign-in onboards an invited address.
func (s *AuthService) SetInviteAcceptor(a inviteAcceptor) {
	s.invites = a
}

// emailVerifier mails a fresh proof-of-mailbox link for a user. Enabled
// reports whether a link can actually be delivered on this install.
type emailVerifier interface {
	SendFor(ctx context.Context, usr *user.User) error
	Enabled() bool
}

// OpenRegistration is the open-registration policy (SERVER_OPEN_REGISTRATION
// and its two bounds).
type OpenRegistration struct {
	Enabled bool
	// MaxPending caps open sign-ups that are still unconfirmed and younger
	// than PendingWindow; past it an open sign-up answers "try again later".
	MaxPending int
	// PendingWindow is how long an unconfirmed open sign-up counts toward
	// MaxPending. An older one stops holding the cap, stays unusable, and can
	// still be confirmed later under the account ceiling.
	PendingWindow time.Duration
}

// normalised floors the bounds, so a policy built without them is the
// strictest one rather than an unbounded one. The configuration supplies the
// real defaults.
func (o OpenRegistration) normalised() OpenRegistration {
	if o.MaxPending <= 0 {
		o.MaxPending = 1
	}
	if o.PendingWindow <= 0 {
		o.PendingWindow = time.Hour
	}
	return o
}

// twoFactorGate is the subset of the two-factor service the login path needs.
type twoFactorGate interface {
	// HasConfirmedFactor keeps the read error, so a DB blip cannot silently skip
	// the second-factor challenge and hand out a full session.
	HasConfirmedFactor(ctx context.Context, userID uuid.UUID) (bool, error)
	Verify(ctx context.Context, userID uuid.UUID, code string) error
}

// passkeyCounter is the one thing UnlinkOIDC needs from the passkey store:
// how many credentials would remain if this identity went away. Narrow on
// purpose — the auth service must not be able to create or delete a passkey.
type passkeyCounter interface {
	CountByUserID(ctx context.Context, userID uuid.UUID) (int64, error)
}

// SetEmailVerifier wires signup verification mail (optional; wired after
// construction because the verifier depends on the user repository too).
// userLocker serializes credential removals per user. The SAME instance must
// be given to the passkey service, or UnlinkOIDC and passkey.Delete do not
// serialize against each other.
type userLocker interface {
	WithUser(userID uuid.UUID, fn func() error) error
}

// SetCredentialLock wires the shared per-user credential lock.
func (s *AuthService) SetCredentialLock(l userLocker) {
	s.credLock = l
}

func (s *AuthService) SetEmailVerifier(verifier emailVerifier) {
	s.emailVerifier = verifier
}

// termsRecorder stores a user's acceptance of the operator's terms: the user
// service's acknowledgement, which writes the audit event first and the user
// columns after it.
type termsRecorder interface {
	AcknowledgeBetaNotice(ctx context.Context, cmd user_beta_notice_ack.UserBetaNoticeAckCommand) (*user.User, error)
}

// SetTermsRecorder wires where a sign-up's terms acceptance is recorded
// (optional; wired after construction because the user service is built
// separately).
func (s *AuthService) SetTermsRecorder(r termsRecorder) {
	s.terms = r
}

// NewAuthService creates a new authentication service
func NewAuthService(
	jwtConfig config.JWTConfig,
	inviteCode string,
	maxAccounts int,
	openRegistration OpenRegistration,
	userRepo repository.UserRepository,
	accountRepo repository.AccountRepository,
	memberRepo repository.AccountMemberRepository,
	bindingRepo repository.RoleBindingRepository,
	inviteRepo repository.AccountInviteRepository,
	extIdentityRepo repository.ExternalIdentityRepository,
	refreshTokens repository.RefreshTokenRepository,
	twoFactor twoFactorGate,
	passkeys passkeyCounter,
	accountService services.AccountService,
	logger *slog.Logger,
) *AuthService {
	if refreshTokens == nil {
		panic("AuthService requires a refresh token repository")
	}
	if twoFactor == nil {
		panic("AuthService requires a two-factor gate")
	}
	if passkeys == nil {
		panic("AuthService requires a passkey counter (the unlink last-credential guard)")
	}
	return &AuthService{
		jwtConfig:        jwtConfig,
		inviteCode:       inviteCode,
		maxAccounts:      maxAccounts,
		openRegistration: openRegistration.normalised(),
		userRepo:         userRepo,
		accountRepo:      accountRepo,
		memberRepo:       memberRepo,
		bindingRepo:      bindingRepo,
		inviteRepo:       inviteRepo,
		extIdentityRepo:  extIdentityRepo,
		passkeys:         passkeys,
		refreshTokens:    refreshTokens,
		twoFactor:        twoFactor,
		twoFactorTickets: newTicketStore(preAuthTokenTTL),
		preAuthTickets:   newTicketStore(preAuthTokenTTL),
		authBurst:        newAuthBurstTracker(),
		loginThrottle:    newLoginThrottle(),
		refreshGrace:     newRefreshGrace(),
		accountService:   accountService,
		passwordHasher:   services.NewBCryptPasswordHasher(productionBcryptCost),
		logger:           logger.With("service", "auth"),
	}
}

// checkPassword compares a submitted password against a stored hash, and
// ALWAYS performs one bcrypt comparison at the production cost — including when
// there is no stored hash, where it compares against dummyPasswordHash and
// reports a mismatch. Callers must route every credential failure through it,
// or the branch that skips it is the one that leaks.
func (s *AuthService) checkPassword(hashedPassword, password string) error {
	if hashedPassword == "" {
		_ = s.passwordHasher.ComparePasswords(dummyPasswordHash, password)
		return app_errors.ErrInvalidCredentials
	}
	return s.passwordHasher.ComparePasswords(hashedPassword, password)
}

// logSignInRefused is the one line a refused password sign-in writes, whether
// the address is unknown or the password is wrong.
func (s *AuthService) logSignInRefused(email string) {
	s.logger.Warn("password sign-in refused", "address_ref", services.AddressLogRef(email))
}

// Login authenticates a user with email and password. Single-account users
// get a token pair immediately; multi-account users get a short-lived
// pre-auth token plus the account list to choose from. clientIP is the
// request's trusted client address, which the per-address brake keys on.
func (s *AuthService) Login(email, password, clientIP string) (*services.LoginResult, error) {
	s.logger.Info("attempting login", "address_ref", services.AddressLogRef(email))

	// The per-address brake, asked before anything is looked up or compared:
	// a refused attempt reveals neither the password's correctness nor the
	// account's existence. An admitted attempt counts as a failure until the
	// password proves right. See login_throttle.go.
	if wait := s.loginThrottle.admit(email, clientIP); wait > 0 {
		s.logger.Warn("password sign-in throttled for this address", "address_ref", services.AddressLogRef(email), "retry_after", wait)
		return nil, &app_errors.LoginThrottledError{RetryAfter: wait}
	}

	usr, err := s.userRepo.GetByEmail(context.Background(), email)
	if err != nil {
		// An unknown address and a wrong password log the SAME line: the log
		// must not answer "which addresses have accounts here" either. Only a
		// lookup that failed for another reason is a server fault.
		if errors.Is(err, app_errors.ErrUserNotFound) {
			s.logSignInRefused(email)
		} else {
			s.logger.Error("could not look up the address for a password sign-in",
				"address_ref", services.AddressLogRef(email), "error", err)
		}
		// Pay for the comparison this branch does not get to make, so response
		// time does not distinguish "no such account" from "wrong password".
		_ = s.checkPassword("", password)
		return nil, app_errors.ErrInvalidCredentials
	}

	if err := s.checkPassword(usr.HashedPassword, password); err != nil {
		s.logSignInRefused(email)
		return nil, app_errors.ErrInvalidCredentials
	}
	s.loginThrottle.succeed(email, clientIP)

	// The password alone is not a login for a user who enrolled a factor.
	// Nothing is issued here — not even the account-selection ticket — so a
	// stolen password cannot enumerate the victim's accounts either.
	//
	// This gate is on the PASSWORD path only. An OIDC login is federated: the
	// identity provider owns that account's MFA policy, and layering a second
	// TOTP prompt on top of the provider's own would duplicate it.
	ctx := context.Background()
	hasFactor, err := s.twoFactor.HasConfirmedFactor(ctx, usr.ID)
	if err != nil {
		// Refuse the login rather than skip the factor: a transient read error is
		// retryable, whereas handing out a full session with MFA silently bypassed
		// is the exact failure MFA exists to prevent.
		s.logger.Error("could not determine two-factor status; refusing to sign in without it",
			"user_id", usr.ID, "error", err)
		return nil, err
	}
	// A registered passkey is an enrolled factor too. The mandatory-2FA policy
	// (two_factor.Policy.UserHasTwoFactor) counts a passkey as satisfying the
	// requirement, on the doctrine that a passkey LOGIN is two factors. That
	// doctrine only holds if the session was actually opened with the passkey:
	// were a password alone to produce a full session for a passkey-holder
	// without TOTP, the policy would wave that session through as compliant
	// — a stolen password would be enough against an account that mandates
	// two factors. So a passkey-holder whose
	// accounts require two factors is challenged after the password, and the
	// only way to complete that challenge is the passkey ceremony itself.
	//
	// Fails CLOSED on either read error, for the same reason as hasFactor.
	challenge, methods, err := s.secondFactorAfterPassword(ctx, usr, hasFactor)
	if err != nil {
		return nil, err
	}
	if challenge {
		ticket, err := s.generateTwoFactorToken(usr)
		if err != nil {
			return nil, err
		}
		s.twoFactorTickets.issue(ticket, usr.ID)
		s.logger.Info("password accepted, awaiting second factor", "user_id", usr.ID, "methods", methods)
		return &services.LoginResult{TwoFactorToken: ticket, TwoFactorMethods: methods}, nil
	}

	return s.loginResultForUser(ctx, usr)
}

// secondFactorAfterPassword decides whether a correct password may open a
// session on its own, and which factor kinds could complete the challenge if
// not. hasFactor is the TOTP answer the caller already holds.
func (s *AuthService) secondFactorAfterPassword(ctx context.Context, usr *user.User, hasFactor bool) (challenge bool, methods []string, err error) {
	passkeyCount, err := s.passkeys.CountByUserID(ctx, usr.ID)
	if err != nil {
		s.logger.Error("could not count passkeys; refusing to sign in without knowing the second factor",
			"user_id", usr.ID, "error", err)
		return false, nil, err
	}
	methods = make([]string, 0, 2)
	if hasFactor {
		methods = append(methods, services.TwoFactorMethodTOTP)
	}
	if passkeyCount > 0 {
		methods = append(methods, services.TwoFactorMethodPasskey)
	}
	challenge = hasFactor
	if !challenge && passkeyCount > 0 {
		mandatory, err := s.memberOfMandatoryTwoFactorAccount(ctx, usr.ID)
		if err != nil {
			s.logger.Error("could not resolve the two-factor policy; refusing to sign in without it",
				"user_id", usr.ID, "error", err)
			return false, nil, err
		}
		// A passkey-holder outside any mandatory-2FA account keeps the plain
		// password login: the passkey is a complete alternative sign-in, not a
		// factor bolted onto the password, and forcing it here would lock out
		// anyone who lost the authenticator (recovery codes exist only for
		// TOTP). Inside a mandatory account that lockout IS the policy.
		challenge = mandatory
	}
	return challenge, methods, nil
}

// passwordNeedsSecondFactor is secondFactorAfterPassword for a caller that has
// not asked the TOTP gate yet.
func (s *AuthService) passwordNeedsSecondFactor(ctx context.Context, usr *user.User) (bool, error) {
	hasFactor, err := s.twoFactor.HasConfirmedFactor(ctx, usr.ID)
	if err != nil {
		return false, err
	}
	challenge, _, err := s.secondFactorAfterPassword(ctx, usr, hasFactor)
	return challenge, err
}

// memberOfMandatoryTwoFactorAccount reports whether any of the user's active
// memberships is in an account that requires a second factor. The answer is
// resolved BEFORE the account is chosen because the challenge happens before
// account selection: a passkey completes it for every account at once.
//
// NOT built on ListAccounts: that helper skips an account on ANY read error,
// which is right for an account picker (a missing row is not selectable
// anyway) and wrong here, where a pool timeout on the mandatory account's row
// would read as "no mandatory account" and let the password alone through —
// the exact failure "fail closed" promises against. Only a
// genuinely missing account is skipped; every other error refuses the login.
func (s *AuthService) memberOfMandatoryTwoFactorAccount(ctx context.Context, userID uuid.UUID) (bool, error) {
	memberships, err := s.memberRepo.ListByUserID(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, m := range memberships {
		if m.Status != account_member.StatusActive {
			continue
		}
		acc, err := s.accountRepo.GetByID(ctx, m.AccountID)
		if err != nil {
			if errors.Is(err, app_errors.ErrAccountNotFound) {
				continue
			}
			return false, fmt.Errorf("resolving the two-factor policy of account %s: %w", m.AccountID, err)
		}
		if acc.RequireTwoFactor {
			return true, nil
		}
	}
	return false, nil
}

// CompletePasskeyLogin issues a session for an identity a passkey ceremony has
// already verified.
//
// No second factor is demanded here even when the user enrolled TOTP: a passkey
// login is ALREADY two-factor — possession of the authenticator plus the user
// verification (PIN or biometric) it performed before releasing the assertion.
// Asking for a TOTP code on top would be ceremony without security.
func (s *AuthService) CompletePasskeyLogin(userID uuid.UUID) (*services.LoginResult, error) {
	ctx := context.Background()
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, app_errors.ErrUserNotFound
	}
	return s.loginResultForUser(ctx, usr)
}

// CompleteTwoFactor exchanges the ticket from Login plus a TOTP or recovery
// code for the real login result.
func (s *AuthService) CompleteTwoFactor(ctx context.Context, twoFactorToken, code string) (*services.LoginResult, error) {
	claims, err := s.ValidateToken(twoFactorToken)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != services.TokenTypeTwoFactor {
		return nil, app_errors.ErrTokenInvalid
	}
	usr, err := s.userRepo.GetByID(ctx, claims.UserID)
	if err != nil {
		return nil, app_errors.ErrUserNotFound
	}
	// The challenge was earned with the OLD credentials. A password change or
	// recovery performed since — most often because that password is believed
	// compromised — ends it, exactly as it ends the account-selection ticket
	// and every refresh token; so does a sign-in email change, which moves
	// the generation and not the timestamp. Checked before the ticket is
	// spent so the refusal
	// costs nothing but the stale challenge.
	if ticketEpochMoved(usr, claims) {
		s.logger.Info("two-factor challenge predates a credential change", "user_id", usr.ID)
		return nil, app_errors.ErrTokenInvalid
	}
	// Spend the ticket BEFORE checking the code. A stateless JWT ticket is
	// otherwise good for its whole TTL, so one /auth/login gives an attacker a
	// five-minute window of unlimited guesses; consuming it forces a fresh
	// password round trip per attempt and makes the per-user failure counter
	// the binding limit.
	if !s.twoFactorTickets.consume(twoFactorToken) {
		return nil, app_errors.ErrTokenInvalid
	}
	if err := s.twoFactor.Verify(ctx, usr.ID, code); err != nil {
		s.logger.Warn("second factor rejected", "user_id", usr.ID)
		return nil, err
	}
	s.logger.Info("second factor accepted", "user_id", usr.ID)
	return s.loginResultForUser(ctx, usr)
}

// loginResultForUser resolves the user's active memberships into either an
// immediate token pair or an account-selection challenge.
func (s *AuthService) loginResultForUser(ctx context.Context, usr *user.User) (*services.LoginResult, error) {
	accounts, err := s.ListAccounts(ctx, usr.ID)
	if err != nil {
		return nil, err
	}

	switch len(accounts) {
	case 0:
		s.logger.Error("user has no active memberships", "user_id", usr.ID)
		return nil, app_errors.ErrNotAMember
	case 1:
		tokens, err := s.GenerateTokens(usr, accounts[0].ID)
		if err != nil {
			s.logger.Error("failed to generate tokens", "user_id", usr.ID, "error", err)
			return nil, err
		}
		s.logger.Info("login successful", "user_id", usr.ID)
		return &services.LoginResult{Tokens: tokens, User: usr}, nil
	default:
		preToken, err := s.generatePreAuthToken(usr)
		if err != nil {
			s.logger.Error("failed to generate pre-auth token", "user_id", usr.ID, "error", err)
			return nil, err
		}
		s.preAuthTickets.issue(preToken, usr.ID)
		s.logger.Info("login requires account selection", "user_id", usr.ID, "accounts", len(accounts))
		return &services.LoginResult{User: usr, PreToken: preToken, Accounts: accounts}, nil
	}
}

// ListAccounts returns the accounts the user holds an ACTIVE membership in.
func (s *AuthService) ListAccounts(ctx context.Context, userID uuid.UUID) ([]services.AccountOption, error) {
	memberships, err := s.memberRepo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	options := make([]services.AccountOption, 0, len(memberships))
	for _, m := range memberships {
		if m.Status != account_member.StatusActive {
			continue
		}
		acc, err := s.accountRepo.GetByID(ctx, m.AccountID)
		if err != nil {
			s.logger.Warn("membership references missing account", "account_id", m.AccountID, "user_id", userID)
			continue
		}
		options = append(options, services.AccountOption{
			ID: acc.ID, Name: acc.Name, RequireTwoFactor: acc.RequireTwoFactor,
		})
	}
	return options, nil
}

// SelectAccount exchanges a pre-auth token for an account-pinned token pair.
func (s *AuthService) SelectAccount(ctx context.Context, preToken string, accountID uuid.UUID) (*services.TokenPair, *user.User, error) {
	claims, err := s.ValidateToken(preToken)
	if err != nil {
		return nil, nil, err
	}
	if claims.TokenType != services.TokenTypePreAuth {
		return nil, nil, app_errors.ErrTokenInvalid
	}

	usr, err := s.userRepo.GetByID(ctx, claims.UserID)
	if err != nil {
		return nil, nil, app_errors.ErrUserNotFound
	}
	// A credential change ends every login challenge issued before it,
	// exactly as it ends the refresh tokens: the ticket was earned with the
	// OLD password, and a reset is most often performed because that password
	// is believed compromised; an email change moves the generation instead
	// of the timestamp and ends it the same way.
	if ticketEpochMoved(usr, claims) {
		s.logger.Info("pre-auth token predates a credential change", "user_id", usr.ID)
		return nil, nil, app_errors.ErrTokenInvalid
	}

	if err := s.requireActiveMembership(ctx, accountID, usr.ID); err != nil {
		return nil, nil, err
	}

	// Spend the ticket LAST, immediately before minting: it is the atomic gate
	// against a concurrent double use (exactly one caller passes), and spending
	// it after the checks means a mis-clicked account does not cost the user a
	// fresh password round trip.
	if !s.preAuthTickets.consume(preToken) {
		return nil, nil, app_errors.ErrTokenInvalid
	}

	tokens, err := s.GenerateTokens(usr, accountID)
	if err != nil {
		return nil, nil, err
	}
	s.logger.Info("account selected", "user_id", usr.ID, "account_id", accountID)
	return tokens, usr, nil
}

// SwitchAccount mints a token pair for another account of an already
// authenticated user. The pair stays in the caller's session family, and that
// family must be live. The access token the caller presents is stateless: it
// keeps verifying until its TTL after a sign-out or a password change, and
// minting a FRESH family off it handed anyone holding such a token a session
// that survived every revocation the user had just performed. Staying
// inside the family also
// keeps "sign out of this device" one action: it revokes both accounts' tokens.
func (s *AuthService) SwitchAccount(ctx context.Context, userID, accountID uuid.UUID, session services.SessionProof) (*services.TokenPair, error) {
	if err := s.requireActiveMembership(ctx, accountID, userID); err != nil {
		return nil, err
	}
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, app_errors.ErrUserNotFound
	}
	if err := s.requireLiveSession(ctx, usr, session); err != nil {
		return nil, err
	}
	tokens, err := s.generateTokens(usr, accountID, session.FamilyID)
	if err != nil {
		return nil, err
	}
	s.logger.Info("account switched", "user_id", userID, "account_id", accountID, "family_id", session.FamilyID)
	return tokens, nil
}

// RequireLiveSession is requireLiveSession behind the user id, for callers
// that hold no *user.User. The device terminal re-runs it on its timer: the
// access token that opened a shell is stateless, so a sign-out, a
// "sign out everywhere" or a password change would otherwise leave the shell
// open until the browser stopped answering pings.
func (s *AuthService) RequireLiveSession(ctx context.Context, userID uuid.UUID, session services.SessionProof) error {
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return app_errors.ErrUserNotFound
	}
	return s.requireLiveSession(ctx, usr, session)
}

// requireLiveSession applies to an access token the two tests RefreshToken
// applies to a refresh token: it must postdate the user's last password
// change, and its family must be live (unrevoked, unexpired). Fails closed —
// an unknown family, a missing iat or an unreadable session list all refuse,
// because the answer decides whether a credential is minted.
func (s *AuthService) requireLiveSession(ctx context.Context, usr *user.User, session services.SessionProof) error {
	if session.FamilyID == uuid.Nil || session.IssuedAt.IsZero() {
		return app_errors.ErrTokenInvalid
	}
	if credentialEpochMoved(usr, session.IssuedAt, session.CredentialGeneration) {
		s.logger.Info("session predates a credential change", "user_id", usr.ID, "family_id", session.FamilyID)
		return app_errors.ErrTokenInvalid
	}
	sessions, err := s.refreshTokens.ListSessions(ctx, usr.ID)
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}
	for _, live := range sessions {
		if live.FamilyID == session.FamilyID {
			return nil
		}
	}
	s.logger.Info("session family is not live", "user_id", usr.ID, "family_id", session.FamilyID)
	return app_errors.ErrTokenInvalid
}

// credentialEpochMoved is the ONE credential-epoch policy for anything minted
// for a user — access token, refresh token, second-factor ticket, account
// selection ticket: it is dead once it predates the user's last password
// change, or once its `cg` claim is not the row's current generation. The
// generation covers what PasswordChangedAt does not, a sign-in email change;
// 0 is a token from before the claim existed and is tolerated until it is
// refreshed, exactly as the row-lock predicates tolerate it. The pending
// tickets once applied the password test only, so an email
// change that ended every session left a login in progress able to finish.
// A zero issuedAt skips the password test: the callers that require one
// refuse it before asking here.
func credentialEpochMoved(usr *user.User, issuedAt time.Time, generation int64) bool {
	if !issuedAt.IsZero() && usr.PasswordChangedAt != nil && issuedAt.Before(*usr.PasswordChangedAt) {
		return true
	}
	return generation > 0 && generation != usr.CredentialGeneration
}

// ticketEpochMoved applies credentialEpochMoved to a parsed ticket.
func ticketEpochMoved(usr *user.User, claims *services.TokenClaims) bool {
	issuedAt := time.Time{}
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	return credentialEpochMoved(usr, issuedAt, claims.CredentialGeneration)
}

// requireActiveMembership fails unless the user is an ACTIVE member of the
// account.
func (s *AuthService) requireActiveMembership(ctx context.Context, accountID, userID uuid.UUID) error {
	member, err := s.memberRepo.GetByAccountAndUser(ctx, accountID, userID)
	if err != nil {
		// Only a definitive absence is "not a member": a read that failed is a
		// server fault the client may retry, and answering it as a refusal
		// would end a session over a database hiccup.
		if errors.Is(err, app_errors.ErrAccountMemberNotFound) || errors.Is(err, app_errors.ErrNotAMember) {
			return app_errors.ErrNotAMember
		}
		return fmt.Errorf("reading the membership: %w", err)
	}
	if member.Status != account_member.StatusActive {
		return app_errors.ErrMembershipDisabled
	}
	return nil
}

// Register registers a new user with email and password
func (s *AuthService) Register(cmd user_register.RegisterCommand) (*services.RegisterResult, error) {
	s.logger.Info("attempting registration", "address_ref", services.AddressLogRef(cmd.Email))

	// Validate command
	if err := cmd.Validate(); err != nil {
		return nil, err
	}

	// The admission (ceiling, closed instance, invite code, open
	// registration and its pending cap) is asked FIRST, before the address
	// is looked up: a caller who may not register at all must get the same
	// refusal whether or not the address is taken, or the public sign-up
	// form answers "which emails have accounts here" to anyone. This is
	// advice taken early; the decision that counts is still made under the
	// lock in provisionNewOwner. A count that cannot be read refuses, as it
	// does there. The probe is a throwaway candidate: it learns whether this
	// sign-up would be an open one, and nothing is written.
	ctx := context.Background()
	admit := s.admitSignup(cmd.InviteCode)
	counts, err := s.accountRepo.Counts(ctx, s.pendingSince())
	if err != nil {
		s.logger.ErrorContext(ctx, "could not count accounts; refusing this registration", "error", err)
		return nil, &app_errors.RegistrationClosedError{Reason: app_errors.ReasonCountUnavailable}
	}
	probe := &account.Account{}
	if err := admit(counts, probe); err != nil {
		return nil, err
	}
	open := probe.OpenSignupPendingSince != nil

	// An admitted caller on the first-owner or invite-code path is still
	// told that the address is taken: that is the sign-up form's own answer
	// to a person who already has an account, and the code is what the
	// operator hands to people they let register. On the OPEN path anyone
	// is admitted, so that answer would tell anyone which addresses are
	// registered: it gets the fresh sign-up's answer instead, nothing is
	// sent, and the password is hashed anyway so the two take similar time.
	// The owner of the address loses nothing: they sign in, or reset their
	// password.
	_, err = s.userRepo.GetByEmail(ctx, cmd.Email)
	if err == nil {
		if open {
			_, _ = s.passwordHasher.HashPassword(cmd.Password)
			s.logger.Info("open registration for an address that already has an account; answered as pending, nothing sent",
				"address_ref", services.AddressLogRef(cmd.Email))
			return &services.RegisterResult{VerificationPending: true}, nil
		}
		s.logger.Warn("registration refused: the address already has an account",
			"address_ref", services.AddressLogRef(cmd.Email))
		return nil, app_errors.ErrUserAlreadyExists
	}

	// Hash password
	hashedPassword, err := s.passwordHasher.HashPassword(cmd.Password)
	if err != nil {
		s.logger.Error("failed to hash password", "address_ref", services.AddressLogRef(cmd.Email), "error", err)
		return nil, app_errors.ErrUserPasswordRequired
	}

	// Generate default account name if name is empty
	accountName := cmd.Name
	if accountName == "" {
		accountName = "My Account" // Default account name
	}

	newUser, newAccount, err := s.provisionOwner(ctx, cmd.Email, cmd.Name, accountName, hashedPassword, admit)
	if err != nil {
		return nil, err
	}
	// What the admission decided under the lock is what counts, not the
	// probe: the account it marked pending gets no session. Its owner
	// confirms the address from the mail, then signs in.
	if newAccount.OpenSignupPendingSince != nil {
		s.sendVerificationInBackground(newUser)
		s.recordSignupTerms(ctx, cmd, newUser, newAccount.ID)
		s.logger.Info("open registration pending confirmation", "user_id", newUser.ID, "account_id", newAccount.ID)
		return &services.RegisterResult{VerificationPending: true}, nil
	}
	tokens, err := s.sessionForNewOwner(ctx, newUser, newAccount.ID)
	if err != nil {
		return nil, err
	}
	return &services.RegisterResult{Tokens: tokens, User: s.recordSignupTerms(ctx, cmd, newUser, newAccount.ID)}, nil
}

// verificationSendTimeout bounds the confirmation mail sent off the request.
const verificationSendTimeout = time.Minute

// sendVerificationInBackground mails an open sign-up's confirmation link off
// the request, so the answer does not wait on the mail server: the time an
// SMTP round-trip takes would otherwise tell a fresh address apart from one
// that already had an account, which is answered without any mail.
func (s *AuthService) sendVerificationInBackground(usr *user.User) {
	if s.emailVerifier == nil {
		return
	}
	run := s.runAsync
	if run == nil {
		run = func(f func()) { go f() }
	}
	run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), verificationSendTimeout)
		defer cancel()
		if err := s.emailVerifier.SendFor(ctx, usr); err != nil {
			s.logger.Warn("failed to send the confirmation email of an open sign-up", "error", err, "user_id", usr.ID)
		}
	})
}

// recordSignupTerms stores the terms acceptance a sign-up carried, and returns
// the user as it now stands. The server does not decide whether terms apply —
// that is the frontend's TERMS_URL — so it records what it was told and never
// refuses a sign-up over it.
//
// A failure here does not undo the registration: the account and the session
// already exist, and answering an error would leave a working account behind a
// failed form. It is logged at error level instead, because the acceptance the
// person gave is then unrecorded. The recorder writes the audit event before
// the user columns, so a stored acceptance never lacks its audit row.
func (s *AuthService) recordSignupTerms(ctx context.Context, cmd user_register.RegisterCommand, newUser *user.User, accountID uuid.UUID) *user.User {
	if !cmd.TermsAccepted {
		return newUser
	}
	if s.terms == nil {
		s.logger.ErrorContext(ctx, "a sign-up accepted the terms of use but no recorder is wired; not stored",
			"user_id", newUser.ID)
		return newUser
	}
	updated, err := s.terms.AcknowledgeBetaNotice(ctx, user_beta_notice_ack.UserBetaNoticeAckCommand{
		ID:          newUser.ID,
		AuthContext: commands.NewAuthContext(accountID, newUser.ID),
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to record the terms acceptance of a new sign-up",
			"error", err, "user_id", newUser.ID)
		return newUser
	}
	return updated
}

// admission decides, given the instance's account counts, whether one more
// account may be created, and marks the candidate when it is an open
// sign-up. It is asked at the one place an account comes into existence
// (provisionNewOwner), so every path that creates one — the sign-up form and
// OIDC auto-provision — answers it by construction.
type admission func(counts repository.AccountCounts, candidate *account.Account) error

// admitSignup is the sign-up form's policy, decided against the account
// counts:
//
//   - at the ceiling (SERVER_MAX_ACCOUNTS > 0, counted over settled
//     accounts): closed;
//   - ZERO accounts: the first owner is admitted without an invite code —
//     an empty install belongs to whoever installed it, and there is nobody
//     yet who could have handed out a code;
//   - otherwise, open registration (SERVER_OPEN_REGISTRATION): see
//     admitOpenSignup. It wins over a configured invite code — the code is
//     then neither asked for nor checked, and the configuration advisory
//     says it is unused — so the sign-up page and the registration never
//     disagree about whether a code is needed;
//   - otherwise, no invite code configured: CLOSED. An empty code used to
//     mean "open to anyone", which a fresh install reached by leaving one
//     secret file empty; from the first account on, the way in is an
//     invitation from a member, and a code is what an operator sets to let
//     further ORGANISATIONS register;
//   - otherwise the presented code must match (constant-time, so response
//     timing leaks nothing under brute force).
//
// The ceiling comes first so that a full instance says "closed" rather
// than "wrong code" to a code that would have been right, and so that an
// open instance is still bounded by it.
func (s *AuthService) admitSignup(presented string) admission {
	return func(counts repository.AccountCounts, candidate *account.Account) error {
		if err := s.admitUnderCeiling(counts, candidate); err != nil {
			return err
		}
		if counts.Total == 0 {
			return nil
		}
		if s.openRegistration.Enabled {
			return s.admitOpenSignup(counts, candidate)
		}
		if s.inviteCode == "" {
			s.logger.Warn("refusing a registration: the instance has accounts and no invite code is configured")
			return &app_errors.RegistrationClosedError{Reason: app_errors.ReasonNoInviteCode}
		}
		if subtle.ConstantTimeCompare([]byte(presented), []byte(s.inviteCode)) != 1 {
			return app_errors.ErrInvalidInviteCode
		}
		return nil
	}
}

// admitOpenSignup admits an open sign-up as PENDING: the account is marked,
// is usable for nothing until its owner confirms their address (see
// RequireSettledAccount), and does not count toward the ceiling meanwhile.
// What bounds pending accounts instead is MaxPending over the pending
// window, so junk sign-ups cannot exhaust the ceiling and cannot pile up
// faster than the cap allows. An install that cannot send the confirmation
// mail is closed rather than admitting accounts nobody could ever confirm.
func (s *AuthService) admitOpenSignup(counts repository.AccountCounts, candidate *account.Account) error {
	if !s.mailDelivers() {
		s.logger.Warn("refusing an open registration: this install cannot send the confirmation mail")
		return &app_errors.RegistrationClosedError{Reason: app_errors.ReasonOpenWithoutMail}
	}
	if counts.Pending >= int64(s.openRegistration.MaxPending) {
		s.logger.Warn("refusing an open registration: too many sign-ups are waiting for confirmation",
			"pending", counts.Pending, "limit", s.openRegistration.MaxPending)
		return &app_errors.RegistrationBusyError{}
	}
	now := s.clock()
	candidate.OpenSignupPendingSince = &now
	return nil
}

// mailDelivers reports whether a confirmation link can reach anyone here.
func (s *AuthService) mailDelivers() bool {
	return s.emailVerifier != nil && s.emailVerifier.Enabled()
}

func (s *AuthService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// pendingSince is the start of the pending window.
func (s *AuthService) pendingSince() time.Time {
	return s.clock().Add(-s.openRegistration.PendingWindow)
}

// admitUnderCeiling is the half every path shares: the invite code says WHO
// may register and nothing about how many times; SERVER_MAX_ACCOUNTS (0 =
// unlimited) is that missing bound, counted over SETTLED accounts. It bounds
// open registration too, both when an account is created and when a pending
// one settles. The OIDC auto-provision path asks this alone — its gate (open
// registration, the invite code, OIDC_AUTO_PROVISION) is applied by the
// handler, which holds the cookie — and never marks its candidate: the
// provider asserted a verified address.
func (s *AuthService) admitUnderCeiling(counts repository.AccountCounts, _ *account.Account) error {
	if s.atAccountCeiling(counts.Settled) {
		// Logged with the numbers because the operator's remedy is to raise the
		// limit, and nothing else in the system will tell them it was reached.
		s.logger.Warn("refusing a registration: the instance is at its account ceiling",
			"accounts", counts.Settled, "limit", s.maxAccounts)
		return &app_errors.RegistrationClosedError{Reason: app_errors.ReasonAccountCeiling}
	}
	return nil
}

// atAccountCeiling is the ceiling test admitUnderCeiling and
// RegistrationState share (SERVER_MAX_ACCOUNTS, 0 = unlimited).
func (s *AuthService) atAccountCeiling(settled int64) bool {
	return s.maxAccounts > 0 && settled >= int64(s.maxAccounts)
}

// RegistrationState answers admitSignup's question without a candidate, in
// the same order (ceiling, empty instance, open registration, configured
// code), so the sign-up page cannot promise what the registration then
// refuses. Open registration on an install that cannot send mail is
// closed, as admitOpenSignup decides. A full pending cap is still "open":
// it empties on its own, and the sign-up says "try again later". It logs
// nothing, because the tokenless sign-in page asks it on every load. A
// count that cannot be read is returned as an error; the caller decides
// what to show.
func (s *AuthService) RegistrationState(ctx context.Context) (services.RegistrationState, error) {
	counts, err := s.accountRepo.Counts(ctx, s.pendingSince())
	if err != nil {
		return "", err
	}
	switch {
	case s.atAccountCeiling(counts.Settled):
		return services.RegistrationClosed, nil
	case counts.Total == 0:
		return services.RegistrationFirstOwner, nil
	case s.openRegistration.Enabled && !s.mailDelivers():
		return services.RegistrationClosed, nil
	case s.openRegistration.Enabled:
		return services.RegistrationOpen, nil
	case s.inviteCode == "":
		return services.RegistrationClosed, nil
	default:
		return services.RegistrationInviteCode, nil
	}
}

// RequireSettledAccount is the open-signup gate, asked by the API middleware
// for every authenticated request (see the port). An account marked pending
// is usable only once its owner's address is confirmed; the first request
// after that settles it — under the account ceiling, in the same locked step
// a sign-up takes — and from then on it is an ordinary account. So the
// mark proves something worth knowing: while it is set, no request was ever
// admitted for the account, and it holds nothing but its owner.
//
// Fails CLOSED on a read error: a gate that admitted on doubt would let a
// database blip hand a stranger's unconfirmed account the whole API.
func (s *AuthService) RequireSettledAccount(ctx context.Context, accountID, userID uuid.UUID) error {
	if _, ok := s.settledAccounts.Load(accountID); ok {
		return nil
	}
	acc, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return fmt.Errorf("reading the account for the open-signup gate: %w", err)
	}
	if acc.OpenSignupPendingSince == nil {
		s.settledAccounts.Store(accountID, struct{}{})
		return nil
	}
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("reading the user for the open-signup gate: %w", err)
	}
	if usr.EmailVerifiedAt == nil {
		return app_errors.ErrEmailVerificationRequired
	}
	settled, err := s.accountRepo.SettleOpenSignup(ctx, accountID, s.pendingSince(), s.admitUnderCeiling)
	if err != nil {
		return err
	}
	if !settled {
		return app_errors.ErrEmailVerificationRequired
	}
	s.settledAccounts.Store(accountID, struct{}{})
	s.logger.InfoContext(ctx, "open sign-up confirmed; the account is settled", "account_id", accountID, "user_id", userID)
	return nil
}

// provisionNewOwner creates a new user, their account, and the membership +
// super_admin binding that make them its owner. It does NOT mint tokens — the
// caller decides how to establish the session (password register mints a pair;
// OIDC auto-provision routes through loginResultForUser).
//
// The admission and the insert are ONE locked step in the repository
// (CreateAdmitted): two sign-ups arriving together on an empty instance must
// not both read zero accounts and both become "the first owner", and two at
// one below the ceiling must not both pass it. The policy stays here; the
// serialization is the repository's.
//
// Fails CLOSED when the count cannot be read — the opposite of the device
// quotas, deliberately: the admission decides whether an invite code is even
// required, so a count that is unknown would otherwise admit a stranger with
// no code at all. Refusing a sign-up for a minute is safe; refusing a device
// admission was not, which is why that one fails open.
func (s *AuthService) provisionNewOwner(
	ctx context.Context,
	email, name, accountName, hashedPassword string,
	admit admission,
) (*user.User, error) {
	newUser, _, err := s.provisionOwner(ctx, email, name, accountName, hashedPassword, admit)
	return newUser, err
}

// provisionOwner is provisionNewOwner that also returns the account as it
// was written — marked pending or not, as the admission decided.
func (s *AuthService) provisionOwner(
	ctx context.Context,
	email, name, accountName, hashedPassword string,
	admit admission,
) (*user.User, *account.Account, error) {
	newAccount := account.NewAccount(accountName)
	if err := s.accountRepo.CreateAdmitted(ctx, newAccount, s.pendingSince(), repository.AccountAdmission(admit)); err != nil {
		switch {
		case errors.Is(err, repository.ErrAccountCountUnavailable):
			s.logger.ErrorContext(ctx, "could not count accounts; refusing this registration",
				"error", err)
			return nil, nil, &app_errors.RegistrationClosedError{Reason: app_errors.ReasonCountUnavailable}
		case errors.Is(err, app_errors.ErrRegistrationClosed), errors.Is(err, app_errors.ErrInvalidInviteCode),
			errors.As(err, new(*app_errors.RegistrationBusyError)):
			// The admission's own refusal, returned unchanged.
			return nil, nil, err
		}
		s.logger.Error("failed to create account", "address_ref", services.AddressLogRef(email), "error", err)
		return nil, nil, app_errors.ErrAccountNameRequired
	}

	// Generate default name if empty
	userName := name
	if userName == "" {
		userName = "User" // Default user name
	}

	newUser := user.NewUser(email, userName, hashedPassword)
	if err := s.userRepo.Create(ctx, newUser); err != nil {
		s.logger.Error("failed to create user", "address_ref", services.AddressLogRef(email), "error", err)
		s.accountRepo.Delete(ctx, newAccount.ID)
		return nil, nil, app_errors.ErrUserEmailRequired
	}

	// The creator owns the account: active membership + super_admin binding.
	member := account_member.NewAccountMember(newAccount.ID, newUser.ID)
	if err := s.memberRepo.Create(ctx, member); err != nil {
		s.logger.Error("failed to create membership", "user_id", newUser.ID, "error", err)
		s.userRepo.Delete(ctx, newUser.ID)
		s.accountRepo.Delete(ctx, newAccount.ID)
		return nil, nil, err
	}
	binding := role_binding.NewRoleBinding(newAccount.ID, newUser.ID, authz.RoleSuperAdmin, nil, nil)
	if err := s.bindingRepo.Create(ctx, binding); err != nil {
		s.logger.Error("failed to create super_admin binding", "user_id", newUser.ID, "error", err)
		s.memberRepo.Delete(ctx, newAccount.ID, newUser.ID)
		s.userRepo.Delete(ctx, newUser.ID)
		s.accountRepo.Delete(ctx, newAccount.ID)
		return nil, nil, err
	}
	return newUser, newAccount, nil
}

// InternalRegister creates a new owner (account + user + super_admin) and mints
// a token pair for the new single-account owner.
func (s *AuthService) InternalRegister(
	ctx context.Context,
	email, name, accountName, hashedPassword string,
	admit admission,
) (*services.TokenPair, *user.User, error) {
	tokens, newUser, _, err := s.registerOwner(ctx, email, name, accountName, hashedPassword, admit)
	return tokens, newUser, err
}

// registerOwner is InternalRegister that also returns the new account's id.
func (s *AuthService) registerOwner(
	ctx context.Context,
	email, name, accountName, hashedPassword string,
	admit admission,
) (*services.TokenPair, *user.User, uuid.UUID, error) {
	newUser, newAccount, err := s.provisionOwner(ctx, email, name, accountName, hashedPassword, admit)
	if err != nil {
		return nil, nil, uuid.Nil, err
	}
	tokens, err := s.sessionForNewOwner(ctx, newUser, newAccount.ID)
	if err != nil {
		return nil, nil, uuid.Nil, err
	}
	return tokens, newUser, newAccount.ID, nil
}

// sessionForNewOwner mails the first-owner or invite-code sign-up its
// confirmation link and mints its session.
func (s *AuthService) sessionForNewOwner(ctx context.Context, newUser *user.User, accountID uuid.UUID) (*services.TokenPair, error) {
	// Password signup cannot prove the mailbox on its own — mail the link.
	// Only here, not in provisionNewOwner: the OIDC provisioning path shares
	// that function and its email is already IdP-verified. Delivery failures
	// are logged, never fatal: on these paths an unverified user is a working
	// user. An install with no mail delivery configured is not a failure — it
	// is the install's shape, and it would warn on every sign-up including
	// the first owner's.
	if s.emailVerifier != nil {
		if verr := s.emailVerifier.SendFor(ctx, newUser); verr != nil {
			if errors.Is(verr, app_errors.ErrEmailNotConfigured) {
				s.logger.Debug("signup verification email not sent: email delivery is not configured", "user_id", newUser.ID)
			} else {
				s.logger.Warn("failed to send signup verification email", "error", verr, "user_id", newUser.ID)
			}
		}
	}
	// A freshly provisioned owner has exactly one account, the one just made.
	accounts, err := s.ListAccounts(ctx, newUser.ID)
	if err != nil || len(accounts) != 1 || accounts[0].ID != accountID {
		return nil, fmt.Errorf("provisioned owner has unexpected account set: %w", err)
	}
	tokens, err := s.GenerateTokens(newUser, accountID)
	if err != nil {
		s.logger.Error("failed to generate tokens", "user_id", newUser.ID, "error", err)
		return nil, err
	}
	s.logger.Info("registration successful", "user_id", newUser.ID)
	return tokens, nil
}

// LinkOIDC attaches a verified external identity to an existing (already
// authenticated) user. Idempotent when the identity is already linked to the
// same user; conflicts when it belongs to someone else. When the IdP-verified
// email matches the local account's email, the link PROVES mailbox ownership —
// stamp EmailVerifiedAt.
func (s *AuthService) LinkOIDC(ctx context.Context, userID uuid.UUID, identity services.OIDCIdentity) error {
	return s.linkOIDC(ctx, userID, identity, nil)
}

// LinkOIDCFromSession: see the interface.
func (s *AuthService) LinkOIDCFromSession(ctx context.Context, userID uuid.UUID, identity services.OIDCIdentity, session services.SessionProof) error {
	// The cheap refusal first (the same tests the guarded routes apply);
	// the authoritative one is inside the repository's transaction.
	if err := s.RequireLiveSession(ctx, userID, session); err != nil {
		return err
	}
	return s.linkOIDC(ctx, userID, identity, &session)
}

func (s *AuthService) linkOIDC(ctx context.Context, userID uuid.UUID, identity services.OIDCIdentity, session *services.SessionProof) error {
	if existing, err := s.extIdentityRepo.GetByProviderSubject(ctx, identity.Provider, identity.Subject); err == nil {
		if existing.UserID == userID {
			return nil // already linked to this user
		}
		return app_errors.ErrExternalIdentityConflict
	}
	linkedEmail := user.NormalizeEmail(identity.Email)
	link := external_identity.New(userID, identity.Provider, identity.Subject, linkedEmail)
	var err error
	if session != nil {
		err = s.extIdentityRepo.CreateWhileSessionLive(ctx, link, session.FamilyID, session.IssuedAt, session.CredentialGeneration)
	} else {
		err = s.extIdentityRepo.Create(ctx, link)
	}
	if err != nil {
		return err
	}
	if usr, err := s.userRepo.GetByID(ctx, userID); err == nil &&
		usr.EmailVerifiedAt == nil && user.NormalizeEmail(usr.Email) == linkedEmail {
		s.stampEmailVerified(ctx, usr)
	}
	return nil
}

// LinkOIDCWithPassword resolves the "an account with this email already exists"
// conflict in a single step: the caller proves control of the local account
// with its password while the IdP has just proven control of the mailbox, so
// the identity can be linked and the session established at once. This is the
// same trust as the manual link from Account settings, which it reuses — the
// user simply does not have to log in first.
//
// Every failure returns ErrInvalidCredentials, and every failure that reaches
// the password step pays the same bcrypt cost, so neither the body nor the
// response time says which emails exist or which have passwords. The uniform
// body alone was not enough: an unknown email and a passwordless account both
// used to return before bcrypt ran.
func (s *AuthService) LinkOIDCWithPassword(ctx context.Context, identity services.OIDCIdentity, password, clientIP string) (*services.LoginResult, error) {
	email := user.NormalizeEmail(identity.Email)
	if email == "" {
		return nil, app_errors.ErrInvalidCredentials
	}
	// The same per-address brake as Login: this path checks the same password.
	if wait := s.loginThrottle.admit(email, clientIP); wait > 0 {
		return nil, &app_errors.LoginThrottledError{RetryAfter: wait}
	}
	usr, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil {
		_ = s.checkPassword("", password)
		return nil, app_errors.ErrInvalidCredentials
	}
	// A passwordless (OIDC-provisioned) account has no password to prove with;
	// checkPassword refuses an empty hash rather than letting bcrypt decide.
	if err := s.checkPassword(usr.HashedPassword, password); err != nil {
		s.logger.Warn("failed password on OIDC link-on-conflict", "user_id", usr.ID)
		return nil, app_errors.ErrInvalidCredentials
	}
	s.loginThrottle.succeed(email, clientIP)
	// The same gate Login applies to a password. This path proves the PASSWORD
	// and then opens a session AND permanently links an identity that is
	// factor-free by design — so for a user who enrolled a factor (TOTP, or a
	// passkey under a mandatory account) a stolen password plus control of the
	// IdP mailbox would buy both at once. Such a
	// user signs in normally, completes their factor, and links from account
	// settings behind step-up. Fails CLOSED on a read error, like Login.
	challenge, err := s.passwordNeedsSecondFactor(ctx, usr)
	if err != nil {
		return nil, err
	}
	if challenge {
		s.logger.Info("refusing OIDC link-on-conflict for a user with an enrolled factor", "user_id", usr.ID)
		return nil, app_errors.ErrOIDCLinkNeedsSecondFactor
	}
	// Links the identity and, because the IdP email matches, stamps
	// EmailVerifiedAt. Later sign-ins with this same provider account resolve
	// by (provider, subject) and need no password.
	if err := s.LinkOIDC(ctx, usr.ID, identity); err != nil {
		return nil, err
	}
	// Re-read: LinkOIDC may have stamped the verification on its own copy.
	if fresh, ferr := s.userRepo.GetByID(ctx, usr.ID); ferr == nil {
		usr = fresh
	}
	return s.loginResultForUser(ctx, usr)
}

// stampEmailVerified persists proven mailbox ownership; failures are logged,
// not fatal (the proof can be re-established on the next explicit link).
func (s *AuthService) stampEmailVerified(ctx context.Context, usr *user.User) {
	// The one column, conditional on the address the IdP just matched — never
	// the whole row from this copy.
	if _, err := s.userRepo.MarkEmailVerified(ctx, usr.ID, usr.Email, time.Now()); err != nil {
		s.logger.Warn("failed to persist email verification", "user_id", usr.ID, "error", err)
		return
	}
	usr.MarkEmailVerified()
}

// ListIdentities returns a user's LIVE external identity links.
func (s *AuthService) ListIdentities(ctx context.Context, userID uuid.UUID) ([]external_identity.ExternalIdentity, error) {
	return s.extIdentityRepo.ListByUserID(ctx, userID)
}

// UnlinkOIDC detaches one federated identity from its owner.
//
// Without it a federated login is a permanent backdoor: an identity attached
// to an account could not be removed by any request at all — a password
// change, a passkey rotation, a TOTP enrolment and a full session revocation
// all left it signing in, and the only cure was editing the database by hand.
//
// It is a SOFT delete (see the repository): unlinking is the one action here
// that its victim cannot undo, because re-linking requires signing in.
//
// The guard counts the credentials that would REMAIN — password, passkeys and
// the user's other live identities. It deliberately does not count TOTP; the
// reasoning, and why counting it would have been a lockout rather than a
// nuisance, is in domain/user.SignInMethods.
func (s *AuthService) UnlinkOIDC(ctx context.Context, userID, identityID uuid.UUID) error {
	identities, err := s.extIdentityRepo.ListByUserID(ctx, userID)
	if err != nil {
		return err
	}
	found := false
	for i := range identities {
		if identities[i].ID == identityID {
			found = true
			break
		}
	}
	if !found {
		// Covers "no such identity", "belongs to someone else" and "already
		// revoked" with one answer, so the route cannot be used to probe which
		// identity ids exist on other accounts.
		return app_errors.ErrExternalIdentityNotFound
	}

	if s.passkeys == nil {
		// Fail closed. Constructed services always have this; a struct-literal
		// one in a test does not, and letting the guard pass silently there is
		// how an untested lockout reaches production.
		return app_errors.ErrExternalIdentityLastCredential
	}

	// The count-guard-revoke below is a check-then-act; hold the user's lock
	// across it so a concurrent removal (a passkey delete, another unlink)
	// cannot pass its own guard on a count this one invalidates.
	// The identities slice is re-read inside the lock so a concurrent revoke is
	// reflected in the count.
	guarded := func() error {
		usr, err := s.userRepo.GetByID(ctx, userID)
		if err != nil {
			return err
		}
		liveIdentities, err := s.extIdentityRepo.ListByUserID(ctx, userID)
		if err != nil {
			return err
		}
		passkeyCount, err := s.passkeys.CountByUserID(ctx, userID)
		if err != nil {
			return err
		}
		remaining := user.SignInMethods{
			HasPassword:        strings.TrimSpace(usr.HashedPassword) != "",
			Passkeys:           int(passkeyCount),
			ExternalIdentities: len(liveIdentities) - 1, // after this one is removed
		}
		if remaining.Count() == 0 {
			return app_errors.ErrExternalIdentityLastCredential
		}
		if err := s.extIdentityRepo.Revoke(ctx, userID, identityID); err != nil {
			return err
		}
		s.logger.Info("unlinked external identity", "user_id", userID, "identity_id", identityID)
		return nil
	}
	if s.credLock != nil {
		return s.credLock.WithUser(userID, guarded)
	}
	return guarded()
}

// provisionInvitedUser creates a passwordless user and accepts a pending
// invite for them through the member service's shared acceptance: the seat
// cap, the OU check, the single-use flip, the membership and role, and the
// audit event, exactly as the invite link does. On any failure the created
// user is rolled back.
func (s *AuthService) provisionInvitedUser(ctx context.Context, email, name string, invite *account_invite.AccountInvite) (*user.User, error) {
	if s.invites == nil {
		// Fail closed: accepting here without the shared checks is how an
		// account came to hold more members than its cap.
		s.logger.ErrorContext(ctx, "no invite acceptor is wired; refusing to onboard an invited address via OIDC",
			"account_id", invite.AccountID)
		return nil, app_errors.ErrInviteNotValid
	}
	newUser := user.NewUser(email, name, "") // passwordless: OIDC is the credential
	// The invite was accepted with an IdP-verified email — ownership proven.
	newUser.MarkEmailVerified()
	if err := s.userRepo.Create(ctx, newUser); err != nil {
		return nil, err
	}
	if err := s.invites.AcceptInviteRecord(ctx, newUser, invite); err != nil {
		s.memberRepo.Delete(ctx, invite.AccountID, newUser.ID)
		s.userRepo.Delete(ctx, newUser.ID)
		return nil, err
	}
	return newUser, nil
}

// identityFreeToProvision refuses to create anybody for an IdP account whose
// (provider, subject) pair is still held by a revoked link of another user:
// the link insert would conflict, leaving a user with no credential behind.
// Asked BEFORE anything is created, so the refusal leaves nothing to undo.
func (s *AuthService) identityFreeToProvision(ctx context.Context, identity services.OIDCIdentity) error {
	held, err := s.extIdentityRepo.RevokedExists(ctx, identity.Provider, identity.Subject)
	if err != nil {
		return fmt.Errorf("checking the identity before provisioning: %w", err)
	}
	if held {
		s.logger.Warn("refused to provision a user for an identity still held by another user's revoked link",
			"provider", identity.Provider)
		return app_errors.ErrExternalIdentityConflict
	}
	return nil
}

// undoProvisionedUser removes a user this sign-in just created, with its
// membership and role bindings, and the account too when the user was its
// new owner. Best effort: each step is logged, none stops the others.
func (s *AuthService) undoProvisionedUser(ctx context.Context, accountID, userID uuid.UUID, ownAccount bool) {
	if s.bindingRepo != nil {
		if err := s.bindingRepo.DeleteByAccountAndUser(ctx, accountID, userID); err != nil {
			s.logger.Error("undo: could not remove role bindings", "user_id", userID, "error", err)
		}
	}
	if err := s.memberRepo.Delete(ctx, accountID, userID); err != nil {
		s.logger.Error("undo: could not remove the membership", "user_id", userID, "error", err)
	}
	if err := s.userRepo.Delete(ctx, userID); err != nil {
		s.logger.Error("undo: could not remove the user", "user_id", userID, "error", err)
	}
	if ownAccount {
		if err := s.accountRepo.Delete(ctx, accountID); err != nil {
			s.logger.Error("undo: could not remove the account", "account_id", accountID, "error", err)
		}
	}
}

// LoginOIDC establishes a session from a verified OIDC identity. It links by
// (provider, subject) first, then by verified email; an unknown verified email
// is auto-provisioned only when autoProvision is set, otherwise rejected so
// enterprise users must be invited first.
func (s *AuthService) LoginOIDC(ctx context.Context, identity services.OIDCIdentity, autoProvision bool) (*services.LoginResult, error) {
	email := user.NormalizeEmail(identity.Email)
	if email == "" {
		return nil, app_errors.ErrOIDCEmailNotVerified
	}

	// 1. Existing link by (provider, subject) — the stable key.
	if link, err := s.extIdentityRepo.GetByProviderSubject(ctx, identity.Provider, identity.Subject); err == nil {
		usr, err := s.userRepo.GetByID(ctx, link.UserID)
		if err != nil {
			return nil, app_errors.ErrUserNotFound
		}
		return s.loginResultForUser(ctx, usr)
	}

	// 2. Existing user by email, and no link for this (provider, subject).
	// A sign-in NEVER attaches a new identity to an existing account on its
	// own, whether or not the account's address is verified. The identity
	// provider vouches for the mailbox, and the mailbox is not the account:
	// whoever can read it (or can get an identity minted for it) would
	// otherwise sign in with no password and none of the factors the owner
	// enrolled, and keep a sign-in method that outlives a password change
	// and a revocation of every session. Linking needs proof of the account
	// itself:
	//   - an account with a password is offered the inline "prove it with
	//     this account's password" step (LinkOIDCWithPassword), which also
	//     refuses an account whose password alone is not a sign-in (an
	//     enrolled factor) and sends that owner to the settings link;
	//   - an account without a password has nothing to prove on the spot;
	//     its owner signs in the way they already can and links the
	//     provider from account settings, behind step-up.
	// A link the user deliberately revoked lands here too (step 1 cannot see
	// a revoked row), so an unlink is not undone by the next sign-in.
	if usr, err := s.userRepo.GetByEmail(ctx, email); err == nil {
		if strings.TrimSpace(usr.HashedPassword) == "" {
			s.logger.Warn("refused OIDC sign-in matching an existing passwordless account; link from settings",
				"user_id", usr.ID, "provider", identity.Provider)
			return nil, app_errors.ErrOIDCAccountExists
		}
		s.logger.Warn("refused implicit OIDC link to an existing local account; password proof required",
			"user_id", usr.ID, "provider", identity.Provider)
		return nil, app_errors.ErrOIDCAccountLinkRequired
	}

	// 3. No user yet, but a pending invite for this verified email — provision
	// a passwordless user and accept the invite (membership + role). This is
	// the invite-driven onboarding: the admin invites an email, the person
	// signs in with Google, no password ever set.
	if invite, err := s.inviteRepo.GetPendingByEmail(ctx, email); err == nil {
		if err := s.identityFreeToProvision(ctx, identity); err != nil {
			return nil, err
		}
		usr, err := s.provisionInvitedUser(ctx, email, identity.Name, invite)
		if err != nil {
			return nil, err
		}
		link := external_identity.New(usr.ID, identity.Provider, identity.Subject, email)
		if err := s.extIdentityRepo.Create(ctx, link); err != nil {
			// Without the link the new user has no way to sign in; undo it
			// rather than leave a credential-less member holding a seat.
			s.logger.Error("could not link the identity of a user just onboarded by invite; undoing the user",
				"user_id", usr.ID, "account_id", invite.AccountID, "error", err)
			s.undoProvisionedUser(ctx, invite.AccountID, usr.ID, false)
			return nil, err
		}
		s.logger.Info("provisioned invited user via OIDC", "user_id", usr.ID, "account_id", invite.AccountID)
		return s.loginResultForUser(ctx, usr)
	}

	// 4. Unknown identity, no invite.
	if !autoProvision {
		return nil, app_errors.ErrOIDCNoAccount
	}
	accountName := identity.Name
	if accountName == "" {
		accountName = email
	}
	// Passwordless owner (OIDC is the credential). The IdP asserted a
	// verified email, so mailbox ownership is proven from day one. The
	// invite gate for this path is the handler's (it holds the cookie and
	// decided autoProvision); only the ceiling is asked here.
	if err := s.identityFreeToProvision(ctx, identity); err != nil {
		return nil, err
	}
	newUser, err := s.provisionNewOwner(ctx, email, identity.Name, accountName, "", s.admitUnderCeiling)
	if err != nil {
		return nil, err
	}
	s.stampEmailVerified(ctx, newUser)
	link := external_identity.New(newUser.ID, identity.Provider, identity.Subject, email)
	if err := s.extIdentityRepo.Create(ctx, link); err != nil {
		s.logger.Error("could not link the identity of a just-provisioned owner; undoing the owner and account",
			"user_id", newUser.ID, "error", err)
		if accounts, lerr := s.memberRepo.ListByUserID(ctx, newUser.ID); lerr == nil && len(accounts) == 1 {
			s.undoProvisionedUser(ctx, accounts[0].AccountID, newUser.ID, true)
		}
		return nil, err
	}
	s.logger.Info("auto-provisioned user via OIDC", "user_id", newUser.ID, "provider", identity.Provider)
	return s.loginResultForUser(ctx, newUser)
}

// ValidateToken validates a JWT token and returns claims
func (s *AuthService) ValidateToken(tokenString string) (*services.TokenClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &services.TokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrTokenSignatureInvalid
		}
		return []byte(s.jwtConfig.SecretKey), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))

	if err != nil {
		return nil, app_errors.ErrTokenInvalid
	}

	if !token.Valid {
		return nil, app_errors.ErrTokenInvalid
	}

	claims, ok := token.Claims.(*services.TokenClaims)
	if !ok {
		return nil, app_errors.ErrTokenInvalid
	}

	// Check if token is expired
	if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
		return nil, app_errors.ErrTokenExpired
	}

	return claims, nil
}

// ValidateAccessToken validates a JWT token and requires it to be an access
// token. Non-HTTP entry points (WebSocket, terminal) must use this instead of
// ValidateToken so long-lived refresh tokens (and account-less pre-auth
// tokens) cannot authenticate them; the HTTP middleware performs the
// equivalent token_type check itself.
func (s *AuthService) ValidateAccessToken(tokenString string) (*services.TokenClaims, error) {
	claims, err := s.ValidateToken(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != services.TokenTypeAccess {
		return nil, app_errors.ErrTokenInvalid
	}
	return claims, nil
}

// RefreshToken generates new tokens using a refresh token. The account stays
// pinned to the refresh token's account claim (never re-read from the user
// row); the membership must still be active and the token must postdate the
// user's last password change.
func (s *AuthService) RefreshToken(refreshToken string) (*services.TokenPair, error) {
	// Validate refresh token
	claims, err := s.ValidateToken(refreshToken)
	if err != nil {
		return nil, err
	}

	// Ensure it's a refresh token
	if claims.TokenType != services.TokenTypeRefresh {
		return nil, app_errors.ErrTokenInvalid
	}

	// Get user from database
	usr, err := s.userRepo.GetByID(context.Background(), claims.UserID)
	if err != nil {
		// A user that is gone ends the session; a lookup that failed is a
		// server fault, which the client retries keeping its tokens.
		if errors.Is(err, app_errors.ErrUserNotFound) {
			s.logger.Info("user not found for refresh", "user_id", claims.UserID)
			return nil, app_errors.ErrUserNotFound
		}
		s.logger.Error("could not read the user for a refresh", "user_id", claims.UserID, "error", err)
		return nil, fmt.Errorf("reading the user for a refresh: %w", err)
	}

	// A credential change invalidates refresh tokens issued before it
	// (advisory here; Rotate decides under the lock).
	if ticketEpochMoved(usr, claims) {
		s.logger.Info("refresh token predates a credential change", "user_id", usr.ID)
		return nil, app_errors.ErrTokenInvalid
	}

	// A kicked or disabled member loses access at the next refresh.
	if err := s.requireActiveMembership(context.Background(), claims.AccountID, usr.ID); err != nil {
		s.logger.Info("refresh rejected: membership gone or disabled", "user_id", usr.ID, "account_id", claims.AccountID)
		return nil, err
	}

	// Redeem the token. Single-use is enforced by the store, so presenting one
	// twice is not merely rejected — it means two parties hold the same secret,
	// and the entire family descended from that login is revoked. Forcing the
	// legitimate user to sign in again is the right price for evicting a thief.
	// Claim and successor are ONE transition under the user's row lock:
	// the successor is minted first, then the repository
	// claims the presented token, re-checks the family and the credential
	// epoch (the checks above this line are advisory; the ones under the
	// lock decide), and inserts the successor — or refuses all of it. Presenting a
	// token twice is not merely rejected: two parties hold the same secret,
	// and the entire family descended from that login is revoked.
	pair, record, err := s.mintTokenPair(usr, claims.AccountID, claims.FamilyID)
	if err != nil {
		return nil, err
	}
	issuedAt := time.Time{}
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	stored, err := s.refreshTokens.Rotate(context.Background(), refresh_token.Hash(refreshToken), issuedAt, claims.CredentialGeneration, record)
	if err != nil {
		if errors.Is(err, app_errors.ErrRefreshTokenReused) && stored != nil {
			s.logger.Warn("refresh token replay detected; revoking the session family",
				"user_id", usr.ID, "family_id", stored.FamilyID)
			if rerr := s.refreshTokens.RevokeFamily(context.Background(), stored.FamilyID); rerr != nil {
				s.logger.Error("failed to revoke refresh token family", "error", rerr, "family_id", stored.FamilyID)
			}
			return nil, app_errors.ErrTokenInvalid
		}
		return nil, err
	}
	return pair, nil
}

// RefreshTokenFromDevice is RefreshToken for a request whose client is known,
// with the rotation grace of refresh_grace.go: a second presentation of the
// same token by the same client within the window receives the successor the
// first one minted, as long as that session is still live, instead of
// revoking the family. Every other replay is RefreshToken's, unchanged.
func (s *AuthService) RefreshTokenFromDevice(refreshToken string, device refresh_token.Device) (*services.TokenPair, error) {
	hash := refresh_token.Hash(refreshToken)
	entry, leader := s.refreshGrace.begin(hash, device)
	if !leader {
		if pair := s.refreshGrace.successorFor(entry, device); pair != nil {
			if err := s.successorStillLive(pair); err != nil {
				return nil, err
			}
			s.logger.Info("refresh token presented again by the client that just rotated it; answering with its successor")
			return pair, nil
		}
		return s.RefreshToken(refreshToken)
	}
	pair, err := s.RefreshToken(refreshToken)
	s.refreshGrace.finish(hash, entry, pair, err)
	return pair, err
}

// successorStillLive applies the live-session test to a successor about to be
// handed out a second time: a logout, a "sign out everywhere" or a credential
// change since the rotation ends the grace with the session.
func (s *AuthService) successorStillLive(pair *services.TokenPair) error {
	claims, err := s.ValidateAccessToken(pair.AccessToken)
	if err != nil {
		return app_errors.ErrTokenInvalid
	}
	issuedAt := time.Time{}
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	return s.RequireLiveSession(context.Background(), claims.UserID, services.SessionProof{
		FamilyID:             claims.FamilyID,
		IssuedAt:             issuedAt,
		CredentialGeneration: claims.CredentialGeneration,
	})
}

// RevokeSession invalidates a refresh token and every successor rotated out of
// it — the server-side half of logging out.
func (s *AuthService) RevokeSession(ctx context.Context, refreshToken string) error {
	claims, err := s.ValidateToken(refreshToken)
	if err != nil || claims.TokenType != services.TokenTypeRefresh {
		// Logging out with a bad token is not an error worth reporting: the
		// session the caller wanted gone is, either way, not usable.
		return nil
	}
	stored, err := s.refreshTokens.Claim(ctx, refresh_token.Hash(refreshToken))
	if stored == nil {
		return nil
	}
	if err != nil && !errors.Is(err, app_errors.ErrRefreshTokenReused) {
		return err
	}
	return s.refreshTokens.RevokeFamily(ctx, stored.FamilyID)
}

// RevokeAllSessions kills every session a user holds — the established ones
// AND the logins still in progress. A pending two-factor or account-selection
// challenge is a credential earned with the password; left standing it would
// mint a fresh, refreshable family after the revocation, which is the one
// moment a revocation is meant to be total. The pending ones go first:
// they are in memory and cannot
// fail, so the revocation the caller relies on is never reported done while
// they still stand.
func (s *AuthService) RevokeAllSessions(ctx context.Context, userID uuid.UUID) error {
	s.revokePendingLogins(userID)
	return s.refreshTokens.RevokeAllForUser(ctx, userID)
}

// revokePendingLogins ends the user's in-flight login challenges.
func (s *AuthService) revokePendingLogins(userID uuid.UUID) {
	s.twoFactorTickets.revokeUser(userID)
	s.preAuthTickets.revokeUser(userID)
}

// GenerateTokens issues a token pair for a FRESH login, starting a new session
// family.
func (s *AuthService) GenerateTokens(usr *user.User, accountID uuid.UUID) (*services.TokenPair, error) {
	return s.generateTokens(usr, accountID, uuid.Nil)
}

// generateTokens mints the pair and records the refresh credential.
// familyID is uuid.Nil for a fresh login; a rotation passes the existing family
// so the whole chain stays revocable as one unit.
func (s *AuthService) generateTokens(usr *user.User, accountID, familyID uuid.UUID) (*services.TokenPair, error) {
	freshLogin := familyID == uuid.Nil
	pair, record, err := s.mintTokenPair(usr, accountID, familyID)
	if err != nil {
		return nil, err
	}
	// Issue, never Create: the row lock re-reads the generation this pair
	// was minted with — a password verified before an administrative reset
	// must not store a session after it — and, for a switch into an
	// existing family, re-checks that the family is still whole, so a logout
	// that took the lock first is final. ErrTokenInvalid passes through as
	// itself: it is the
	// "sign in again" refusal the callers already map.
	if err := s.refreshTokens.Issue(context.Background(), record, usr.CredentialGeneration, usr.PasswordChangedAt); err != nil {
		if errors.Is(err, app_errors.ErrTokenInvalid) {
			s.logger.Info("refusing to record a session: credentials or family changed since verification",
				"user_id", usr.ID, "family_id", record.FamilyID)
			return nil, err
		}
		return nil, fmt.Errorf("failed to record refresh token: %w", err)
	}
	if freshLogin {
		s.enforceSessionCap(context.Background(), usr.ID)
	}
	return pair, nil
}

// mintTokenPair signs an access/refresh pair and builds the refresh record
// WITHOUT storing it, so a rotation can hand the record to the repository's
// atomic Rotate and a login to Create. A nil family starts a new session.
func (s *AuthService) mintTokenPair(usr *user.User, accountID, familyID uuid.UUID) (*services.TokenPair, *refresh_token.RefreshToken, error) {
	now := time.Now()
	if familyID == uuid.Nil {
		familyID = uuid.New()
	}

	accessClaims := &services.TokenClaims{
		UserID:    usr.ID,
		AccountID: accountID,
		Email:     usr.Email,
		TokenType: services.TokenTypeAccess,
		FamilyID:  familyID,
		// The credential generation rides on both tokens, so the row-lock
		// predicates (Rotate, CreateWhileSessionLive) and requireLiveSession
		// can compare it with the row (a column that is written but read by
		// nothing protects nobody).
		CredentialGeneration: usr.CredentialGeneration,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.jwtConfig.Issuer,
			Subject:   usr.ID.String(),
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.jwtConfig.AccessTokenTTL)),
		},
	}

	// A `jti` on every token, not for revocation but for UNIQUENESS: `iat`
	// has second precision, so a refresh token minted in the same second as
	// its predecessor — the same user, account and family — was the same
	// JWT byte for byte, and its stored hash collided with the row it was
	// meant to succeed (unique index on token_hash: a 500 on /auth/refresh
	// within a second of login). Latent before Rotate too; Rotate rolls the
	// claim back on the failed insert, so the presented token stayed usable
	// and a retry a second later worked.
	refreshClaims := &services.TokenClaims{
		UserID:    usr.ID,
		AccountID: accountID,
		Email:     usr.Email,
		TokenType:            services.TokenTypeRefresh,
		FamilyID:             familyID,
		CredentialGeneration: usr.CredentialGeneration,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.jwtConfig.Issuer,
			Subject:   usr.ID.String(),
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.jwtConfig.RefreshTokenTTL)),
		},
	}

	accessTokenString, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString([]byte(s.jwtConfig.SecretKey))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign access token: %w", err)
	}
	refreshTokenString, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString([]byte(s.jwtConfig.SecretKey))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	record := refresh_token.New(usr.ID, accountID, familyID, refreshTokenString,
		now.Add(s.jwtConfig.RefreshTokenTTL))
	return &services.TokenPair{
		AccessToken:  accessTokenString,
		RefreshToken: refreshTokenString,
		ExpiresIn:    int64(s.jwtConfig.AccessTokenTTL.Seconds()),
		TokenType:    "Bearer",
	}, record, nil
}

// The two pending-login tickets carry the credential generation like every
// other token minted for the user, so their consumers can apply
// credentialEpochMoved in full.
func (s *AuthService) generateTwoFactorToken(usr *user.User) (string, error) {
	now := time.Now()
	claims := &services.TokenClaims{
		UserID:               usr.ID,
		Email:                usr.Email,
		TokenType:            services.TokenTypeTwoFactor,
		CredentialGeneration: usr.CredentialGeneration,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.jwtConfig.Issuer,
			Subject:   usr.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(preAuthTokenTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(s.jwtConfig.SecretKey))
	if err != nil {
		return "", fmt.Errorf("failed to sign two-factor token: %w", err)
	}
	return signed, nil
}

func (s *AuthService) generatePreAuthToken(usr *user.User) (string, error) {
	now := time.Now()
	claims := &services.TokenClaims{
		UserID:               usr.ID,
		Email:                usr.Email,
		TokenType:            services.TokenTypePreAuth,
		CredentialGeneration: usr.CredentialGeneration,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.jwtConfig.Issuer,
			Subject:   usr.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(preAuthTokenTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(s.jwtConfig.SecretKey))
	if err != nil {
		return "", fmt.Errorf("failed to sign pre-auth token: %w", err)
	}
	return signed, nil
}
