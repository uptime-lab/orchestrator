// Package reauth implements step-up authentication: a short-lived proof that
// the person at the keyboard is the account owner, required before changing how
// the account is signed into.
//
// Why it exists: a session alone was enough to register a passkey or enroll a
// second factor. An attacker holding a stolen session could add their OWN
// authenticator, and because a password reset does not remove enrolled factors,
// the victim's recovery would not evict them — they would persist through the
// very action taken to expel them.
package reauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

const (
	// ticketTTL is how long one confirmation stays good. Long enough to cover a
	// whole settings visit — a passkey ceremony is two requests, TOTP enrollment
	// is scan-then-confirm — short enough that an unattended tab does not stay
	// privileged.
	ticketTTL = 5 * time.Minute

	// maxFailures / failureWindow bound password guessing against a known user.
	// TOTP guessing is bounded by the two-factor service's own per-user counter;
	// this one exists for the password path, which otherwise has only the
	// per-IP HTTP limiter that a distributed attacker sidesteps.
	maxFailures   = 10
	failureWindow = 15 * time.Minute
)

// The dependencies below are declared as the NARROW slices this service
// actually uses rather than the full repository ports. A guard in front of
// credential changes has to be exercised against every branch — no credential,
// each credential, wrong credential, expired ticket — and depending on the
// whole repository surface would mean stubbing dozens of unrelated methods to
// test one policy decision.

// UserReader reads the identity being confirmed.
type UserReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (*user.User, error)
}

// PasskeyCounter reports whether the user holds any security key.
type PasskeyCounter interface {
	CountByUserID(ctx context.Context, userID uuid.UUID) (int64, error)
}

// PasswordComparer verifies a plaintext password against a stored hash.
type PasswordComparer interface {
	ComparePasswords(hashedPassword, plainPassword string) error
}

// IdentityCounter reports whether the user has an external (OIDC) identity to
// re-authenticate against. Optional dependency: an install with no IdP has no
// federated users to guard.
type IdentityCounter interface {
	ListIdentities(ctx context.Context, userID uuid.UUID) ([]external_identity.ExternalIdentity, error)
}

// TwoFactorVerifier is the subset of the two-factor service this needs: whether
// a user has a confirmed factor, and whether a code is currently valid. Verify
// accepts a recovery code too, and carries its own per-user rate limit.
type TwoFactorVerifier interface {
	// HasConfirmedFactor keeps the read error so AvailableMethods can propagate it
	// rather than report a credentialed user as method-less during a DB blip.
	HasConfirmedFactor(ctx context.Context, userID uuid.UUID) (bool, error)
	Verify(ctx context.Context, userID uuid.UUID, code string) error
}

// SessionVerifier proves that the caller's SESSION is still live: its family
// unrevoked and its access token minted after the user's last password change.
// The auth service implements it (RequireLiveSession).
//
// Why a step-up needs it: the access token is stateless and
// keeps verifying until its TTL after a sign-out, a "sign out everywhere" or a
// password reset. A ticket bound only to the USER therefore outlived every
// revocation the user performed — an attacker holding a pre-recovery token and
// ticket could change the freshly recovered password, and the same guard
// fronts passkey registration, TOTP enrolment and the email. So a ticket is
// issued only to a live session, bound to that session's family, and the
// session is re-proven every time the ticket is presented.
type SessionVerifier interface {
	RequireLiveSession(ctx context.Context, userID uuid.UUID, session services.SessionProof) error
}

// Service mints and validates re-authentication tickets.
type Service struct {
	userRepo    UserReader
	passkeyRepo PasskeyCounter
	twoFactor   TwoFactorVerifier
	hasher      PasswordComparer
	// identities enables the IdP step-up. nil when OIDC is not configured, in
	// which case a federated user cannot exist either.
	identities IdentityCounter
	// sessions proves the caller's session is live at issue and at use. Wired
	// after construction because the auth service is built later; a missing
	// verifier FAILS CLOSED (every step-up refused), never open.
	sessions SessionVerifier
	tickets  *ticketStore
	failures *failureCounter
	logger   *slog.Logger
}

func NewService(
	userRepo UserReader,
	passkeyRepo PasskeyCounter,
	twoFactor TwoFactorVerifier,
	hasher PasswordComparer,
	logger *slog.Logger,
) *Service {
	if userRepo == nil {
		panic("reauth.Service requires a user repository")
	}
	if passkeyRepo == nil {
		panic("reauth.Service requires a passkey repository")
	}
	if twoFactor == nil {
		panic("reauth.Service requires a two-factor verifier")
	}
	if hasher == nil {
		panic("reauth.Service requires a password comparer")
	}
	return &Service{
		userRepo:    userRepo,
		passkeyRepo: passkeyRepo,
		twoFactor:   twoFactor,
		hasher:      hasher,
		tickets:     newTicketStore(ticketTTL),
		failures:    newFailureCounter(maxFailures, failureWindow),
		logger:      logger.With("service", "reauth"),
	}
}

// SetIdentityCounter enables step-up through the identity provider. Wired after
// construction because OIDC may not be configured at all.
func (s *Service) SetIdentityCounter(identities IdentityCounter) {
	s.identities = identities
}

// SetSessionVerifier wires the live-session proof. Not optional in production:
// without it every confirmation and every guarded route is refused.
func (s *Service) SetSessionVerifier(sessions SessionVerifier) {
	s.sessions = sessions
}

// errSessionVerifierMissing is the fail-closed answer when nothing can prove
// the session live. Surfaces as a refusal at the middleware, loudly in the log.
var errSessionVerifierMissing = errors.New("reauth: no session verifier is wired; refusing to confirm identity")

// requireLiveSession is asked before a ticket is issued and before one is
// honoured. It returns the auth service's own sentinels (ErrTokenInvalid for a
// dead session), so the HTTP layer can end the session rather than re-prompt.
func (s *Service) requireLiveSession(ctx context.Context, userID uuid.UUID, session services.SessionProof) error {
	if s.sessions == nil {
		return errSessionVerifierMissing
	}
	return s.sessions.RequireLiveSession(ctx, userID, session)
}

// Methods reports what the user can prove themselves with. The frontend needs
// this to know which prompt to show, and the guard needs it to decide whether
// re-authentication is possible at all.
type Methods struct {
	Password bool `json:"password"`
	TOTP     bool `json:"totp"`
	Passkey  bool `json:"passkey"`
	// OIDC means the user can be sent back to their identity provider to
	// re-authenticate. It is the ONLY method a federated user with no local
	// credential has, and without it that user was exempt from the guard
	// entirely.
	OIDC bool `json:"oidc"`
}

// Any reports whether the user holds ANY credential to re-authenticate with.
func (m Methods) Any() bool {
	return m.Password || m.TOTP || m.Passkey || m.OIDC
}

// AvailableMethods inspects what the user actually holds.
func (s *Service) AvailableMethods(ctx context.Context, userID uuid.UUID) (Methods, error) {
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return Methods{}, err
	}
	// Propagate every read error rather than swallowing it. A swallowed error
	// makes a credentialed user look method-less, so EnsureRecentAuth's "no way to
	// prove themselves" exemption wrongly waves through a passkey registration,
	// TOTP enrolment, email change or OIDC link during a DB blip — the exact
	// attack the step-up exists to stop. The middleware fails
	// closed on this error.
	hasFactor, err := s.twoFactor.HasConfirmedFactor(ctx, userID)
	if err != nil {
		return Methods{}, fmt.Errorf("checking two-factor status: %w", err)
	}
	methods := Methods{
		Password: strings.TrimSpace(usr.HashedPassword) != "",
		TOTP:     hasFactor,
	}
	count, err := s.passkeyRepo.CountByUserID(ctx, userID)
	if err != nil {
		return Methods{}, fmt.Errorf("counting passkeys: %w", err)
	}
	methods.Passkey = count > 0
	if s.identities != nil {
		ids, err := s.identities.ListIdentities(ctx, userID)
		if err != nil {
			return Methods{}, fmt.Errorf("listing identities: %w", err)
		}
		methods.OIDC = len(ids) > 0
	}
	return methods, nil
}

// EnsureRecentAuth is the guard in front of a credential change.
//
// A user who holds NO way to prove themselves at all is still let through: they
// are the ones adding their first authenticator, and demanding a proof they
// cannot make would lock them out of ever securing their account.
//
// "No way at all" used to include a federated user — signed in through an
// identity provider, no password, no factor, no passkey — which was the wrong
// reading: they CAN prove themselves, at the provider. That exemption let a
// stolen OIDC-only session enrol the attacker's own credential, and since a
// password reset does not remove enrolled factors, the victim's recovery would
// not have evicted them. Methods.OIDC closes it, so the
// exemption applies only to a user with neither a local credential nor a linked
// identity — an account nothing could authenticate as in the first place.
//
// The session is proven live FIRST, before the ticket is even looked at: a
// ticket is only ever as good as the session it was issued to, and a session
// that has since been signed out or outlived by a password change must not
// reach a credential change with or without one. The
// exemption for a credential-less user sits behind the same check.
func (s *Service) EnsureRecentAuth(ctx context.Context, userID uuid.UUID, ticket string, session services.SessionProof) error {
	if err := s.requireLiveSession(ctx, userID, session); err != nil {
		return err
	}
	if s.tickets.valid(ticket, userID, session.FamilyID) {
		return nil
	}
	methods, err := s.AvailableMethods(ctx, userID)
	if err != nil {
		return err
	}
	if !methods.Any() {
		return nil
	}
	return app_errors.ErrReauthRequired
}

// ConfirmPassword issues a ticket against the user's password.
func (s *Service) ConfirmPassword(ctx context.Context, userID uuid.UUID, password string, session services.SessionProof) (string, error) {
	if err := s.requireLiveSession(ctx, userID, session); err != nil {
		return "", err
	}
	if !s.failures.allow(userID) {
		return "", app_errors.ErrReauthTooManyAttempts
	}
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(usr.HashedPassword) == "" {
		return "", app_errors.ErrReauthUnavailable
	}
	if err := s.hasher.ComparePasswords(usr.HashedPassword, password); err != nil {
		s.failures.record(userID)
		s.logger.WarnContext(ctx, "re-authentication failed", "user_id", userID, "method", "password")
		return "", app_errors.ErrReauthInvalid
	}
	s.failures.clear(userID)
	return s.grant(userID, session.FamilyID)
}

// ConfirmTOTP issues a ticket against the user's second factor. A recovery code
// works too — Verify accepts and consumes one.
func (s *Service) ConfirmTOTP(ctx context.Context, userID uuid.UUID, code string, session services.SessionProof) (string, error) {
	if err := s.requireLiveSession(ctx, userID, session); err != nil {
		return "", err
	}
	if err := s.twoFactor.Verify(ctx, userID, code); err != nil {
		s.logger.WarnContext(ctx, "re-authentication failed", "user_id", userID, "method", "totp")
		return "", err
	}
	return s.grant(userID, session.FamilyID)
}

// Grant issues a ticket for a proof performed elsewhere — the passkey assertion,
// which is a full WebAuthn ceremony owned by the passkey service, or a fresh
// IdP authentication. The caller MUST have verified that the asserted
// credential belongs to this very user, and hands over the session the proof
// was made from: the ticket is bound to it and is issued only while it is live.
func (s *Service) Grant(ctx context.Context, userID uuid.UUID, session services.SessionProof) (string, error) {
	if err := s.requireLiveSession(ctx, userID, session); err != nil {
		return "", err
	}
	return s.grant(userID, session.FamilyID)
}

// errTicketMintFailed means the entropy source failed. Unreachable in practice,
// but issuing a predictable ticket instead would defeat the whole mechanism.
var errTicketMintFailed = errors.New("failed to mint a confirmation ticket")

func (s *Service) grant(userID, familyID uuid.UUID) (string, error) {
	ticket := s.tickets.put(userID, familyID)
	if ticket == "" {
		return "", errTicketMintFailed
	}
	return ticket, nil
}

// TTL exposes the lifetime so a response can tell the client when to re-prompt.
func (s *Service) TTL() time.Duration { return ticketTTL }

// ticketStore holds live confirmations.
//
// In-memory and deliberately so: a confirmation is a property of the moment,
// not of the account, and a backend restart dropping every ticket is the SAFE
// failure — everyone is asked to confirm again. Multi-use within its window, so
// one prompt covers a begin/finish ceremony pair instead of interrupting it.
//
// An entry is bound to the user AND the session family it was issued to. There
// is deliberately no per-user revocation here: a revoked or outlived session
// is refused by the live-session check in front of every use, which reads the
// authoritative state instead of relying on a second inventory being told.
type ticketStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]ticketEntry
}

type ticketEntry struct {
	userID    uuid.UUID
	familyID  uuid.UUID
	expiresAt time.Time
}

func newTicketStore(ttl time.Duration) *ticketStore {
	return &ticketStore{ttl: ttl, entries: make(map[string]ticketEntry)}
}

func (t *ticketStore) put(userID, familyID uuid.UUID) string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// A ticket we cannot randomize is not a ticket.
		return ""
	}
	ticket := hex.EncodeToString(raw)

	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, v := range t.entries {
		if now.After(v.expiresAt) {
			delete(t.entries, k)
		}
	}
	t.entries[ticket] = ticketEntry{userID: userID, familyID: familyID, expiresAt: now.Add(t.ttl)}
	return ticket
}

// valid reports whether the ticket is live AND belongs to this user AND to
// this session family. Binding to the user matters: without it a ticket minted
// by one session would elevate anybody who got hold of it. Binding to the
// family matters the same way one level down: the user's OTHER sessions —
// including one an attacker opened with a stolen password — must not be able
// to spend a confirmation the owner made at their own keyboard. An unknown
// family (uuid.Nil) matches nothing.
func (t *ticketStore) valid(ticket string, userID, familyID uuid.UUID) bool {
	if ticket == "" || familyID == uuid.Nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[ticket]
	if !ok {
		return false
	}
	if time.Now().After(entry.expiresAt) {
		delete(t.entries, ticket)
		return false
	}
	return entry.userID == userID && entry.familyID == familyID
}

// failureCounter bounds guessing per user.
type failureCounter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	entries map[uuid.UUID][]time.Time
}

func newFailureCounter(max int, window time.Duration) *failureCounter {
	return &failureCounter{max: max, window: window, entries: make(map[uuid.UUID][]time.Time)}
}

func (f *failureCounter) allow(userID uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recent(userID)) < f.max
}

func (f *failureCounter) record(userID uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[userID] = append(f.recent(userID), time.Now())
}

func (f *failureCounter) clear(userID uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, userID)
}

// recent prunes and returns the in-window failures. Caller holds the lock.
func (f *failureCounter) recent(userID uuid.UUID) []time.Time {
	cutoff := time.Now().Add(-f.window)
	kept := f.entries[userID][:0]
	for _, at := range f.entries[userID] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	f.entries[userID] = kept
	return kept
}
