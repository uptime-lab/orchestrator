package services

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_register"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	JWTClaimUserID    = "user_id"
	JWTClaimAccountID = "account_id"
)

// Token types carried in the token_type claim. Access tokens are pinned to
// one account; pre-auth tokens carry no account and are accepted exclusively
// by the select-account endpoint.
const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
	TokenTypePreAuth = "pre_auth"
	// TokenTypeTwoFactor is the short-lived ticket handed out after a correct
	// password when a second factor is still owed. It grants nothing on its
	// own — only CompleteTwoFactor accepts it.
	TokenTypeTwoFactor = "two_factor"
)

// TokenClaims represents the JWT claims. Roles are intentionally NOT in the
// token — they live in role bindings and are resolved per request.
type TokenClaims struct {
	UserID    uuid.UUID `json:"user_id"`
	AccountID uuid.UUID `json:"account_id"` // Nil for pre_auth tokens
	Email     string    `json:"email"`
	TokenType string    `json:"token_type"` // "access", "refresh" or "pre_auth"
	// FamilyID names the session (login) this token belongs to. Carried on the
	// ACCESS token too, so a caller can be told which entry in its own session
	// list is the one it is talking to us with — and so logout can revoke
	// exactly that session. Empty on tokens issued before this existed and on
	// pre-auth tokens, which are not a session yet.
	FamilyID uuid.UUID `json:"family_id,omitempty"`
	// CredentialGeneration is users.credential_generation at mint time: the
	// count of credential transitions (password writes, sign-in email
	// changes) the identity had been through. A predicate compares it with
	// the row's current value, so a token minted before an email change is
	// refused exactly as one minted before a password change; 0 (a token
	// from before the claim existed) is tolerated until it refreshes and
	// earns one.
	CredentialGeneration int64 `json:"cg,omitempty"`
	jwt.RegisteredClaims
}

// TokenPair represents a pair of access and refresh tokens
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// RegisterResult is what a sign-up produced. Either a session (Tokens and
// User), or — for an open registration — VerificationPending and nothing
// else: the account cannot be used before its address is confirmed, and the
// answer is the same for an address that already had an account, so the
// open form does not tell a stranger which addresses are registered.
type RegisterResult struct {
	Tokens              *TokenPair
	User                *user.User
	VerificationPending bool
}

// SessionProof is what the caller's ACCESS token says about the session it
// belongs to, handed to any operation that mints credentials for an already
// signed-in user. Such an operation must not outlive the session it was asked
// from: the access token is stateless and keeps verifying until its TTL after
// the user signed out or changed the password, and minting a fresh, unrevoked
// family off it would turn that residual window into a session that survives
// every revocation the user just performed.
type SessionProof struct {
	// FamilyID is the session the access token names. uuid.Nil means unknown
	// and is refused, never treated as any session.
	FamilyID uuid.UUID
	// IssuedAt is the access token's iat, compared against the user's last
	// password change exactly as a refresh token's is.
	IssuedAt time.Time
	// CredentialGeneration is the token's `cg` claim; 0 means the token
	// predates the claim and only the IssuedAt test applies.
	CredentialGeneration int64
}

// AccountOption is one account a multi-account user may select at login.
type AccountOption struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// RequireTwoFactor lets the client render the account's security policy
	// (and explain a forced enrollment) without a second round trip.
	RequireTwoFactor bool `json:"require_two_factor"`
}

// LoginResult is a discriminated union: exactly one of Tokens (single-account
// user, logged in immediately) or PreToken+Accounts (selection required) is
// set.
type LoginResult struct {
	Tokens   *TokenPair
	User     *user.User
	PreToken string
	Accounts []AccountOption
	// TwoFactorToken is set when the password was correct but a second factor
	// is still required. Tokens and PreToken are then both empty: nothing is
	// issued until the factor is verified.
	TwoFactorToken string
	// TwoFactorMethods lists how the challenge can be completed: TOTP (a code
	// or recovery code through CompleteTwoFactor) and/or a passkey (the
	// ordinary passkey login, which is itself two factors). Set only alongside
	// TwoFactorToken. Advisory for the client's UI; the server enforces each
	// method on its own path regardless of what was advertised.
	TwoFactorMethods []string
}

// Second-factor methods a login challenge can be completed with.
const (
	TwoFactorMethodTOTP    = "totp"
	TwoFactorMethodPasskey = "passkey"
)

// OIDCIdentity is a verified external identity handed to the auth service by
// the OIDC layer (the trustworthy subset of a verified id_token).
type OIDCIdentity struct {
	Provider string
	Subject  string
	Email    string
	Name     string
}

// RegistrationState is how the sign-up form is admitted at an instant.
type RegistrationState string

const (
	// RegistrationFirstOwner: the instance has no account yet; the first
	// sign-up is admitted without an invite code and owns the install.
	RegistrationFirstOwner RegistrationState = "first_owner"
	// RegistrationInviteCode: a new organisation registers with the
	// operator's invite code.
	RegistrationInviteCode RegistrationState = "invite_code"
	// RegistrationClosed: no sign-up is admitted (no invite code configured,
	// or the account ceiling is reached); people join by invitation.
	RegistrationClosed RegistrationState = "closed"
	// RegistrationOpen: the operator opened self-registration
	// (SERVER_OPEN_REGISTRATION); a new organisation registers without a
	// code, up to the account ceiling.
	RegistrationOpen RegistrationState = "open"
)

// AuthService defines the interface for authentication operations
type AuthService interface {
	// Login checks a password sign-in. clientIP is the request's trusted
	// client address: the per-address brake keys on it, so a stranger's
	// failures from elsewhere do not hold the owner's sign-in shut.
	Login(email, password, clientIP string) (*LoginResult, error)
	// RegistrationState reports how the sign-up form would be admitted right
	// now, so the tokenless sign-up page can say so before anyone types. It
	// is the same decision the registration itself makes, and it is advice:
	// the registration decides again under its own lock.
	RegistrationState(ctx context.Context) (RegistrationState, error)
	// LoginOIDC establishes a session from a verified OIDC identity.
	LoginOIDC(ctx context.Context, identity OIDCIdentity, autoProvision bool) (*LoginResult, error)
	// LinkOIDC attaches a verified external identity to an existing user.
	LinkOIDC(ctx context.Context, userID uuid.UUID, identity OIDCIdentity) error
	// LinkOIDCFromSession is LinkOIDC for the account-settings link flow: the
	// identity is attached only while the session that started the link is
	// still live, decided under the user's row lock together with the
	// insert, so a revocation cannot slip between the check and the
	// persistence. ErrTokenInvalid when the session is gone.
	LinkOIDCFromSession(ctx context.Context, userID uuid.UUID, identity OIDCIdentity, session SessionProof) error
	// LinkOIDCWithPassword links a verified external identity to the local
	// account that shares its email, proving control of that account with its
	// password, and returns a login result. Resolves the "account already
	// exists" conflict without a separate sign-in. clientIP as for Login.
	LinkOIDCWithPassword(ctx context.Context, identity OIDCIdentity, password, clientIP string) (*LoginResult, error)
	// ListIdentities returns a user's LIVE external identity links.
	ListIdentities(ctx context.Context, userID uuid.UUID) ([]external_identity.ExternalIdentity, error)
	// UnlinkOIDC detaches one federated identity from its owner, refusing when
	// it is the user's last way of signing in. Soft delete — an operator can
	// restore it, its owner cannot.
	UnlinkOIDC(ctx context.Context, userID, identityID uuid.UUID) error
	// Register creates an account through the sign-up form. An open
	// registration answers VerificationPending with no session, whether or
	// not the address already had an account.
	Register(cmd user_register.RegisterCommand) (*RegisterResult, error)
	// RequireSettledAccount is the open-signup gate: nil when the account may
	// be used, app_errors.ErrEmailVerificationRequired while it is an open
	// sign-up whose owner has not confirmed their address, and the ceiling's
	// RegistrationClosedError when confirming it would carry the instance
	// past SERVER_MAX_ACCOUNTS.
	RequireSettledAccount(ctx context.Context, accountID, userID uuid.UUID) error
	ValidateToken(token string) (*TokenClaims, error)
	// ValidateAccessToken additionally requires token_type == "access", so
	// refresh and pre-auth tokens cannot authenticate WebSocket/terminal
	// handshakes.
	ValidateAccessToken(token string) (*TokenClaims, error)
	RefreshToken(refreshToken string) (*TokenPair, error)
	// RefreshTokenFromDevice is RefreshToken for a request whose client is
	// known: a second presentation of a token by the client that rotated it
	// a moment ago (two tabs refreshing together) receives the same
	// successor instead of revoking the session. Any other replay revokes
	// the family as RefreshToken does.
	RefreshTokenFromDevice(refreshToken string, device refresh_token.Device) (*TokenPair, error)
	// SelectAccount exchanges a pre-auth token for an account-pinned pair.
	SelectAccount(ctx context.Context, preToken string, accountID uuid.UUID) (*TokenPair, *user.User, error)
	// SwitchAccount mints a pair for another account of an authenticated user,
	// inside the caller's own session family, which must still be live.
	SwitchAccount(ctx context.Context, userID, accountID uuid.UUID, session SessionProof) (*TokenPair, error)
	// RequireLiveSession is the session test SwitchAccount applies, exposed
	// for a long-lived capability opened with an access token (the device
	// terminal): the family named by the proof must still be live and the
	// token must postdate the user's last password change. Fails closed.
	RequireLiveSession(ctx context.Context, userID uuid.UUID, session SessionProof) error
	// ListAccounts returns the accounts a user can select (active memberships).
	ListAccounts(ctx context.Context, userID uuid.UUID) ([]AccountOption, error)
	GenerateTokens(user *user.User, accountID uuid.UUID) (*TokenPair, error)
	// ListSessions returns the user's live logins, marking the one making the
	// request. currentFamilyID comes from the caller's access-token claims.
	ListSessions(ctx context.Context, userID, currentFamilyID uuid.UUID) ([]refresh_token.Session, error)
	// RevokeSessionFamily signs one session out, scoped to its owner.
	RevokeSessionFamily(ctx context.Context, userID, familyID uuid.UUID) error
	// RevokeOtherSessions signs out everywhere except the caller's own session.
	RevokeOtherSessions(ctx context.Context, userID, keepFamilyID uuid.UUID) error
	// RecordSessionDevice attaches the request's User-Agent and IP to a freshly
	// issued refresh token. Called by the HTTP layer, which is the only place
	// that knows them.
	RecordSessionDevice(ctx context.Context, refreshToken string, device refresh_token.Device)
	// NoteFailedLogin records one rejected credential attempt from a client
	// address, so sustained guessing raises an operator anomaly. Called by the
	// HTTP layer for the same reason as the line above: the address is only
	// known there.
	NoteFailedLogin(ctx context.Context, sourceIP string)
	// RevokeSession invalidates a refresh token and every successor rotated
	// out of it (logout).
	RevokeSession(ctx context.Context, refreshToken string) error
	// RevokeAllSessions kills every session a user holds.
	RevokeAllSessions(ctx context.Context, userID uuid.UUID) error
	// CompleteTwoFactor exchanges a two-factor ticket plus a code for the
	// login result the password step withheld.
	CompleteTwoFactor(ctx context.Context, twoFactorToken, code string) (*LoginResult, error)
	// CompletePasskeyLogin issues a session for an identity a WebAuthn
	// ceremony already verified.
	CompletePasskeyLogin(userID uuid.UUID) (*LoginResult, error)
}

// PasswordHasher defines the interface for password hashing operations
type PasswordHasher interface {
	HashPassword(password string) (string, error)
	ComparePasswords(hashedPassword, plainPassword string) error
}

// BCryptPasswordHasher implements PasswordHasher using bcrypt
type BCryptPasswordHasher struct {
	cost int
}

// NewBCryptPasswordHasher creates a new BCryptPasswordHasher
func NewBCryptPasswordHasher(cost int) *BCryptPasswordHasher {
	if cost <= 0 {
		cost = 12 // Default bcrypt cost
	}
	return &BCryptPasswordHasher{cost: cost}
}

// HashPassword hashes a password using bcrypt
func (h *BCryptPasswordHasher) HashPassword(password string) (string, error) {
	hashedBytes, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", err
	}
	return string(hashedBytes), nil
}

// ComparePasswords compares a hashed password with a plain password
func (h *BCryptPasswordHasher) ComparePasswords(hashedPassword, plainPassword string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(plainPassword))
}
