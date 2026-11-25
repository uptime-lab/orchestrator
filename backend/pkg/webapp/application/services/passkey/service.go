// Package passkey implements WebAuthn registration and passwordless login.
package passkey

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/passkey"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

const (
	// displayName is what the authenticator shows when saving a passkey.
	displayName = "Orchestrator"
	// ceremonyTTL bounds how long a begin/finish pair may straddle. Generous
	// enough for a user to find a hardware key, short enough that an abandoned
	// challenge does not linger.
	ceremonyTTL = 5 * time.Minute
)

// Service owns the WebAuthn ceremonies and the credential store.
type Service struct {
	repo     repository.PasskeyRepository
	userRepo repository.UserRepository
	// rpIDOverride pins the Relying Party ID when configured. Empty means it is
	// derived from the request host — see relyingParty.
	rpIDOverride string
	// identities lets the last-credential guard see federated sign-ins.
	// OPTIONAL and set after construction: OIDC may be disabled entirely, and
	// absent means "count none", which can only make the guard stricter. Wiring
	// it as a required constructor argument would have made a passkey service
	// impossible to build without an OIDC repository it may never consult.
	identities identityLister
	// credLock serializes the last-credential guard against the other removal
	// paths (auth.UnlinkOIDC). OPTIONAL and set after construction: when absent
	// the guard still runs, it just is not protected against a concurrent
	// removal — so an unwired service behaves exactly as before.
	credLock   userLocker
	ceremonies *ceremonyStore
	logger     *slog.Logger
}

// userLocker serializes credential removals per user. Narrow interface so the
// service depends on the behaviour, not the credlock package.
type userLocker interface {
	WithUser(userID uuid.UUID, fn func() error) error
}

// SetCredentialLock wires the shared per-user lock. The SAME instance must be
// given to the auth service, or the two removal paths do not serialize.
func (s *Service) SetCredentialLock(l userLocker) {
	s.credLock = l
}

// identityLister is the one thing the last-credential guard needs: the user's
// LIVE federated links. Narrow on purpose — this service must not be able to
// create or revoke one.
type identityLister interface {
	ListByUserID(ctx context.Context, userID uuid.UUID) ([]external_identity.ExternalIdentity, error)
}

// SetIdentityLister lets a linked IdP account count as a way to sign in when
// deciding whether a passkey is the last credential.
func (s *Service) SetIdentityLister(l identityLister) {
	s.identities = l
}

func NewService(
	repo repository.PasskeyRepository,
	userRepo repository.UserRepository,
	rpIDOverride string,
	logger *slog.Logger,
) *Service {
	if repo == nil {
		panic("passkey.Service requires a passkey repository")
	}
	if userRepo == nil {
		panic("passkey.Service requires a user repository")
	}
	return &Service{
		repo:         repo,
		userRepo:     userRepo,
		rpIDOverride: strings.TrimSpace(rpIDOverride),
		ceremonies:   newCeremonyStore(ceremonyTTL),
		logger:       logger.With("service", "passkey"),
	}
}

// relyingParty builds the WebAuthn config for THIS request.
//
// The RP ID is a domain, and a passkey is bound to it forever — a credential
// registered under one RP ID cannot be used under another. A single binary
// serves any number of hostnames, so baking one
// in at build time is not an option; it is derived from the Host the browser
// actually used, which is by definition the origin the credential belongs to.
// An explicit override exists for deployments behind a proxy that rewrites Host.
func (s *Service) relyingParty(r *http.Request) (*webauthn.WebAuthn, error) {
	// r.Host ONLY — never X-Forwarded-Host. The origin check is the single
	// anti-phishing property WebAuthn has, and a forwarding header is supplied
	// by whoever is talking to us: honouring it would let a caller that reaches
	// the backend directly (anything on the docker network, an on-prem install
	// without a normalising proxy) name the relying party it is validated
	// against. Traefik preserves the original Host, and WEBAUTHN_RP_ID pins it
	// for any proxy that does not.
	host := r.Host
	// Strip the port: an RP ID is a bare domain, never host:port, even though
	// the ORIGIN that must match does include it.
	rpID := host
	if idx := strings.LastIndex(rpID, ":"); idx != -1 && !strings.Contains(rpID, "]") {
		rpID = rpID[:idx]
	}
	if s.rpIDOverride != "" {
		rpID = s.rpIDOverride
	}
	if rpID == "" {
		return nil, fmt.Errorf("cannot determine the WebAuthn relying party id from the request")
	}

	// Likewise derived, not taken from a header. Browsers refuse WebAuthn on
	// insecure origins except on localhost, so http is only ever right there —
	// which makes this a local-development affordance rather than a policy
	// decision an attacker could steer.
	scheme := "https"
	if isLoopbackHost(rpID) {
		scheme = "http"
	}

	return webauthn.New(&webauthn.Config{
		RPDisplayName: displayName,
		RPID:          rpID,
		RPOrigins:     []string{scheme + "://" + host},
		// User verification is REQUIRED for every ceremony this service runs,
		// and it must be said here, server-side. The whole product treats a
		// passkey login as two factors (possession + the PIN or biometric the
		// authenticator performed) and lets it skip TOTP and unlock step-up;
		// that is only true if the assertion's UV flag is checked. The library
		// checks it ONLY when the session says required
		// (webauthn/login.go: shouldVerifyUser := session.UserVerification ==
		// protocol.VerificationRequired) and its default is "preferred", under
		// which a roaming key with no PIN set signs a UP-only assertion that was
		// accepted as both MFA and reauthentication. This config
		// value seeds registration options and, through beginLogin, the login
		// session; BeginLogin below repeats it explicitly so neither path can
		// drift back to the default alone.
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
	})
}

// isLoopbackHost reports whether the host is a local development address.
func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// webauthnUser adapts our user + credentials to the library's interface.
type webauthnUser struct {
	user        *user.User
	credentials []webauthn.Credential
}

// WebAuthnID is the user handle the authenticator stores. The UUID bytes are
// stable and opaque — never the email, which can change and is not a secret the
// authenticator should hold.
func (u *webauthnUser) WebAuthnID() []byte { id := u.user.ID; return id[:] }

func (u *webauthnUser) WebAuthnName() string { return u.user.Email }

func (u *webauthnUser) WebAuthnDisplayName() string {
	if name := strings.TrimSpace(u.user.Name); name != "" {
		return name
	}
	return u.user.Email
}

func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *Service) loadWebAuthnUser(ctx context.Context, usr *user.User) (*webauthnUser, error) {
	stored, err := s.repo.ListByUserID(ctx, usr.ID)
	if err != nil {
		return nil, err
	}
	creds := make([]webauthn.Credential, 0, len(stored))
	for i := range stored {
		creds = append(creds, toLibraryCredential(&stored[i]))
	}
	return &webauthnUser{user: usr, credentials: creds}, nil
}

func toLibraryCredential(p *passkey.Passkey) webauthn.Credential {
	return webauthn.Credential{
		ID:        p.CredentialID,
		PublicKey: p.PublicKey,
		Authenticator: webauthn.Authenticator{
			AAGUID:    p.AAGUID,
			SignCount: p.SignCount,
		},
		Flags: webauthn.CredentialFlags{
			BackupEligible: p.BackupEligible,
			BackupState:    p.BackupState,
		},
	}
}

// BeginRegistration starts adding a passkey to the SIGNED-IN user's account.
//
// Resident keys are required: a credential the authenticator cannot surface on
// its own is useless for the usernameless login this exists to provide.
// Existing credentials are excluded so a user cannot register the same
// authenticator twice and then wonder which of two identical entries to delete.
func (s *Service) BeginRegistration(ctx context.Context, r *http.Request, userID uuid.UUID) (*protocol.CredentialCreation, string, error) {
	wa, err := s.relyingParty(r)
	if err != nil {
		return nil, "", err
	}
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	waUser, err := s.loadWebAuthnUser(ctx, usr)
	if err != nil {
		return nil, "", err
	}

	creation, session, err := wa.BeginRegistration(waUser,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(waUser.WebAuthnCredentials()).CredentialDescriptors()),
	)
	if err != nil {
		return nil, "", err
	}

	ticket := s.ceremonies.put(session)
	return creation, ticket, nil
}

// FinishRegistration verifies the attestation and stores the credential.
//
// session is the proof of the session that asked — the JWT middleware's
// family id, issue time and generation. The step-up guard proved that
// session live before the handler ran; the credential is stored under the
// same proof re-checked on the user's row lock, because a
// passkey is a complete way to sign in and a sign-out or a recovery that
// lands between the guard and the insert must end the registration the way
// it ends an OIDC link. A missing proof is refused before the ceremony ticket
// is spent: an absent session is not a live one, and this service must not
// be callable without it from a path that forgot to pass it.
func (s *Service) FinishRegistration(ctx context.Context, r *http.Request, userID uuid.UUID, ticket, name string, session services.SessionProof) (*passkey.Passkey, error) {
	if session.FamilyID == uuid.Nil || session.IssuedAt.IsZero() {
		s.logger.WarnContext(ctx, "passkey registration refused: no session proof", "user_id", userID)
		return nil, app_errors.ErrTokenInvalid
	}
	wa, err := s.relyingParty(r)
	if err != nil {
		return nil, err
	}
	ceremony, ok := s.ceremonies.take(ticket)
	if !ok {
		return nil, app_errors.ErrPasskeyCeremonyExpired
	}
	usr, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	waUser, err := s.loadWebAuthnUser(ctx, usr)
	if err != nil {
		return nil, err
	}

	credential, err := wa.FinishRegistration(waUser, *ceremony, r)
	if err != nil {
		s.logger.WarnContext(ctx, "passkey registration failed", "user_id", userID, "error", err)
		return nil, app_errors.ErrPasskeyRegistrationFailed
	}

	label := strings.TrimSpace(name)
	if label == "" {
		label = "Passkey"
	}
	record := passkey.New(
		userID,
		credential.ID,
		credential.PublicKey,
		credential.Authenticator.AAGUID,
		credential.Authenticator.SignCount,
		credential.Flags.BackupEligible,
		credential.Flags.BackupState,
		label,
	)
	// One transition: the live-session test and the insert share the user's
	// row lock inside the repository; ErrTokenInvalid means
	// the session ended first and nothing was stored.
	if err := s.repo.CreateWhileSessionLive(ctx, record, session.FamilyID, session.IssuedAt, session.CredentialGeneration); err != nil {
		if errors.Is(err, app_errors.ErrTokenInvalid) {
			s.logger.InfoContext(ctx, "passkey registration refused: the session ended before the credential was stored",
				"user_id", userID, "family_id", session.FamilyID)
		}
		return nil, err
	}
	s.logger.InfoContext(ctx, "passkey registered", "user_id", userID, "passkey_id", record.ID)
	return record, nil
}

// BeginLogin starts a usernameless authentication. No user is named — the
// authenticator resolves which account to offer, which is the whole point of a
// discoverable credential.
func (s *Service) BeginLogin(r *http.Request) (*protocol.CredentialAssertion, string, error) {
	wa, err := s.relyingParty(r)
	if err != nil {
		return nil, "", err
	}
	// UV required — see the relying-party config for why this is load-bearing.
	// Stated again here because a LoginOption could otherwise override the
	// config default without anyone noticing.
	assertion, session, err := wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		return nil, "", err
	}
	if session.UserVerification != protocol.VerificationRequired {
		// Cannot happen with the option above; guards a library change that
		// stops honouring it, which would silently accept UP-only assertions again.
		return nil, "", fmt.Errorf("passkey login ceremony did not record the user-verification requirement")
	}
	return assertion, s.ceremonies.put(session), nil
}

// FinishLogin verifies the assertion and returns the authenticated user.
func (s *Service) FinishLogin(ctx context.Context, r *http.Request, ticket string) (*user.User, error) {
	wa, err := s.relyingParty(r)
	if err != nil {
		return nil, err
	}
	session, ok := s.ceremonies.take(ticket)
	if !ok {
		return nil, app_errors.ErrPasskeyCeremonyExpired
	}

	var matched *passkey.Passkey

	// The handler resolves the credential the authenticator presented. The
	// userHandle is checked against the credential's owner: without that, a
	// valid assertion for credential A could be paired with a claim to be user
	// B's handle.
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		stored, err := s.repo.GetByCredentialID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		usr, err := s.userRepo.GetByID(ctx, stored.UserID)
		if err != nil {
			return nil, err
		}
		ownerID := usr.ID
		if !bytes.Equal(userHandle, ownerID[:]) {
			return nil, app_errors.ErrPasskeyLoginFailed
		}
		matched = stored
		return s.loadWebAuthnUser(ctx, usr)
	}

	credential, err := wa.FinishDiscoverableLogin(handler, *session, r)
	if err != nil {
		s.logger.WarnContext(ctx, "passkey login failed", "error", err)
		return nil, app_errors.ErrPasskeyLoginFailed
	}
	if matched == nil {
		return nil, app_errors.ErrPasskeyLoginFailed
	}

	// A signature counter that fails to advance is the specification's signal
	// of a cloned authenticator. Refuse the login and say so loudly: silently
	// accepting it would defeat the one anti-cloning mechanism WebAuthn has.
	// (Counters of 0 mean the authenticator does not implement one at all —
	// common for platform authenticators — and are not a warning.)
	if credential.Authenticator.CloneWarning {
		s.logger.ErrorContext(ctx, "passkey clone warning: signature counter did not advance",
			"user_id", matched.UserID, "passkey_id", matched.ID)
		return nil, app_errors.ErrPasskeyCloneWarning
	}

	matched.MarkUsed(credential.Authenticator.SignCount)
	matched.BackupState = credential.Flags.BackupState
	if err := s.repo.Update(ctx, matched); err != nil {
		s.logger.WarnContext(ctx, "failed to record passkey use", "error", err, "passkey_id", matched.ID)
	}

	usr, err := s.userRepo.GetByID(ctx, matched.UserID)
	if err != nil {
		return nil, app_errors.ErrPasskeyLoginFailed
	}
	s.logger.InfoContext(ctx, "passkey login", "user_id", usr.ID, "passkey_id", matched.ID)
	return usr, nil
}

// List returns the user's registered credentials.
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]passkey.Passkey, error) {
	return s.repo.ListByUserID(ctx, userID)
}

// Delete removes one credential.
//
// Refuses to remove a user's LAST way of signing in, which would leave the
// account unreachable — and `password_reset` deliberately refuses passwordless
// accounts, so there would be no self-service way back.
//
// The count goes through domain/user.SignInMethods, shared with the OIDC
// unlink guard. Until then the two asked the question differently and both were
// wrong: this one ignored federated identities entirely, so a passwordless user
// whose Google account was linked could not delete a passkey they could safely
// spare. Counting identities LOOSENS this guard on purpose — that user really
// does still have a way in.
//
// identities is optional and absent means "count none", which keeps the guard
// strictly conservative when the repository is not wired: the failure mode is a
// refused deletion, never a lockout.
func (s *Service) Delete(ctx context.Context, userID, passkeyID uuid.UUID) error {
	// The count-guard-delete below is a check-then-act; hold the user's lock
	// across it so a concurrent removal (another passkey, an OIDC unlink)
	// cannot pass its own guard on a count this one is about to invalidate.
	guarded := func() error {
		usr, err := s.userRepo.GetByID(ctx, userID)
		if err != nil {
			return err
		}
		// An id that is not one of the caller's passkeys is a miss, answered
		// before the last-credential guard: that guard counts "after this one
		// is removed", and for a passkey that does not exist it refused a
		// password user with "the only way you can sign in".
		owned, err := s.repo.ListByUserID(ctx, userID)
		if err != nil {
			return err
		}
		found := false
		for i := range owned {
			if owned[i].ID == passkeyID {
				found = true
				break
			}
		}
		if !found {
			return app_errors.ErrPasskeyNotFound
		}
		count, err := s.repo.CountByUserID(ctx, userID)
		if err != nil {
			return err
		}
		identityCount := 0
		if s.identities != nil {
			links, lerr := s.identities.ListByUserID(ctx, userID)
			if lerr != nil {
				return lerr
			}
			identityCount = len(links)
		}
		remaining := user.SignInMethods{
			HasPassword:        strings.TrimSpace(usr.HashedPassword) != "",
			Passkeys:           int(count) - 1, // after this one is removed
			ExternalIdentities: identityCount,
		}
		if remaining.Count() == 0 {
			return app_errors.ErrPasskeyLastCredential
		}
		return s.repo.Delete(ctx, userID, passkeyID)
	}
	if s.credLock != nil {
		return s.credLock.WithUser(userID, guarded)
	}
	return guarded()
}

// ceremonyStore holds in-flight WebAuthn challenges.
//
// Server-side and keyed by an opaque ticket, so the challenge and the expected
// user handle never pass through the client where they could be tampered with.
// In-memory is adequate and deliberate: a ceremony lives for minutes, and a
// backend restart mid-registration costs one retry.
type ceremonyStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]ceremonyEntry
}

type ceremonyEntry struct {
	session   *webauthn.SessionData
	expiresAt time.Time
}

func newCeremonyStore(ttl time.Duration) *ceremonyStore {
	return &ceremonyStore{ttl: ttl, entries: make(map[string]ceremonyEntry)}
}

func (c *ceremonyStore) put(session *webauthn.SessionData) string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// A ticket we cannot randomize is not a ticket; fail the ceremony
		// rather than issue a guessable one.
		return ""
	}
	ticket := hex.EncodeToString(raw)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Opportunistic sweep: ceremonies are rare enough not to need a job.
	now := time.Now()
	for k, v := range c.entries {
		if now.After(v.expiresAt) {
			delete(c.entries, k)
		}
	}
	c.entries[ticket] = ceremonyEntry{session: session, expiresAt: now.Add(c.ttl)}
	return ticket
}

// take consumes a ticket. Single-use: a challenge must never be replayable.
func (c *ceremonyStore) take(ticket string) (*webauthn.SessionData, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[ticket]
	delete(c.entries, ticket)
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.session, true
}
