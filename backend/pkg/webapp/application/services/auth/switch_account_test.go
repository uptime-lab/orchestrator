package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// Why these tests exist. POST /auth/switch-account once accepted an access token
// whose session had been signed out (or whose owner had changed the password
// and revoked everything) and minted a FRESH, unrevoked token family off it.
// An access token is stateless and keeps verifying to its TTL, so the residual
// window every revocation leaves became a session that survived the revocation.
// The switch now stays inside the caller's family and refuses a family that is
// not live, or a token that predates the password change.

// activeMemberRepo answers every membership question with "active": these
// tests are about the session gate, which sits after the membership one.
type activeMemberRepo struct {
	repository.AccountMemberRepository
}

func (activeMemberRepo) GetByAccountAndUser(_ context.Context, accountID, userID uuid.UUID) (*account_member.AccountMember, error) {
	return &account_member.AccountMember{AccountID: accountID, UserID: userID, Status: account_member.StatusActive}, nil
}

// unreadableSessions is a store whose session inventory cannot be read — the
// case the gate must fail CLOSED on.
type unreadableSessions struct{ *fakeRefreshStore }

func (unreadableSessions) ListSessions(context.Context, uuid.UUID) ([]refresh_token.Session, error) {
	return nil, errors.New("database unavailable")
}

type switchFixture struct {
	svc      *AuthService
	store    *fakeRefreshStore
	usr      *user.User
	original *services.TokenPair
	proof    services.SessionProof
}

// newSwitchFixture logs a user in and returns the proof the JWT middleware
// would derive from that login's access token.
func newSwitchFixture(t *testing.T) *switchFixture {
	t.Helper()
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	// A real row is at generation >= 1 and the proof carries the token's cg
	// claim; with both at zero the generation arm of Issue is never
	// exercised on this path.
	usr := &user.User{ID: uuid.New(), Email: "switch@example.com", CredentialGeneration: 1}
	svc.userRepo = &stubUserRepo{byID: usr}
	svc.memberRepo = activeMemberRepo{}
	store.generations[usr.ID] = usr.CredentialGeneration
	original, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.ValidateAccessToken(original.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.CredentialGeneration != 1 {
		t.Fatalf("fixture: the access token carries cg=%d, want 1", claims.CredentialGeneration)
	}
	return &switchFixture{
		svc:      svc,
		store:    store,
		usr:      usr,
		original: original,
		proof:    services.SessionProof{FamilyID: claims.FamilyID, IssuedAt: claims.IssuedAt.Time, CredentialGeneration: claims.CredentialGeneration},
	}
}

func (f *switchFixture) originalRecord() *refresh_token.RefreshToken {
	return f.store.byHash[refresh_token.Hash(f.original.RefreshToken)]
}

// assertNothingMinted is the property every refusal must hold: not merely an
// error, but no new refresh record either — a credential the store knows about
// is a credential that can be redeemed.
func (f *switchFixture) assertNothingMinted(t *testing.T, before int) {
	t.Helper()
	if got := len(f.store.byHash); got != before {
		t.Fatalf("refusal still recorded a refresh token: %d records, want %d", got, before)
	}
}

func TestSwitchAccountStaysInsideTheLiveSession(t *testing.T) {
	f := newSwitchFixture(t)
	target := uuid.New()

	switched, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, target, f.proof)
	if err != nil {
		t.Fatalf("a live session must be allowed to switch: %v", err)
	}
	minted := f.store.byHash[refresh_token.Hash(switched.RefreshToken)]
	if minted == nil {
		t.Fatal("the switched refresh token was not recorded")
	}
	if minted.FamilyID != f.originalRecord().FamilyID {
		t.Fatalf("switch started a new family %s; want the caller's own %s", minted.FamilyID, f.originalRecord().FamilyID)
	}
	if minted.AccountID != target {
		t.Fatalf("switched token pinned to %s, want %s", minted.AccountID, target)
	}
	claims, err := f.svc.ValidateAccessToken(switched.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.AccountID != target || claims.FamilyID != f.proof.FamilyID {
		t.Fatalf("switched access token names account %s family %s; want %s / %s", claims.AccountID, claims.FamilyID, target, f.proof.FamilyID)
	}
	if _, err := f.svc.RefreshToken(switched.RefreshToken); err != nil {
		t.Fatalf("switched refresh token must be redeemable: %v", err)
	}
}

// Signing out of the device after a switch must end BOTH accounts' tokens —
// that is what keeping the switch inside one family buys.
func TestSwitchAccountSignOutEndsTheSwitchedTokensToo(t *testing.T) {
	f := newSwitchFixture(t)
	switched, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), f.proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RevokeSession(context.Background(), f.original.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RefreshToken(switched.RefreshToken); err == nil {
		t.Fatal("the switched refresh token survived signing the session out")
	}
}

func TestSwitchAccountRefusesARevokedSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke func(f *switchFixture) error
	}{
		{"signed out of this session", func(f *switchFixture) error {
			return f.svc.RevokeSession(context.Background(), f.original.RefreshToken)
		}},
		{"signed out everywhere", func(f *switchFixture) error {
			return f.svc.RevokeAllSessions(context.Background(), f.usr.ID)
		}},
		{"signed out by session id", func(f *switchFixture) error {
			return f.svc.RevokeSessionFamily(context.Background(), f.usr.ID, f.proof.FamilyID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwitchFixture(t)
			if err := tc.revoke(f); err != nil {
				t.Fatal(err)
			}
			if f.originalRecord().RevokedAt == nil {
				t.Fatal("fixture did not revoke the original session")
			}
			before := len(f.store.byHash)

			pair, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), f.proof)
			if !errors.Is(err, app_errors.ErrTokenInvalid) {
				t.Fatalf("revoked session switched accounts: pair=%v err=%v", pair != nil, err)
			}
			f.assertNothingMinted(t, before)
		})
	}
}

// A password change is the other event a refresh is tested against; the
// access token gets the same test. Nothing is revoked here on purpose, so the
// family is still live and only the timestamp can refuse.
func TestSwitchAccountRefusesATokenPredatingThePasswordChange(t *testing.T) {
	f := newSwitchFixture(t)
	changed := f.proof.IssuedAt.Add(time.Second)
	f.usr.PasswordChangedAt = &changed
	before := len(f.store.byHash)

	pair, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), f.proof)
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("token issued before the password change switched accounts: pair=%v err=%v", pair != nil, err)
	}
	f.assertNothingMinted(t, before)
}

// A password change BEFORE the login is history, not a revocation.
func TestSwitchAccountAllowsATokenIssuedAfterThePasswordChange(t *testing.T) {
	f := newSwitchFixture(t)
	changed := f.proof.IssuedAt.Add(-time.Minute)
	f.usr.PasswordChangedAt = &changed
	if _, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), f.proof); err != nil {
		t.Fatalf("token issued after the password change must switch: %v", err)
	}
}

// An incomplete proof is refused, never treated as "some session": a token
// minted before family ids existed would otherwise match nothing and pass.
func TestSwitchAccountRefusesAnIncompleteProof(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proof func(full services.SessionProof) services.SessionProof
	}{
		{"no family", func(p services.SessionProof) services.SessionProof { p.FamilyID = uuid.Nil; return p }},
		{"no issued-at", func(p services.SessionProof) services.SessionProof { p.IssuedAt = time.Time{}; return p }},
		{"somebody else's family", func(p services.SessionProof) services.SessionProof { p.FamilyID = uuid.New(); return p }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSwitchFixture(t)
			before := len(f.store.byHash)
			_, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), tc.proof(f.proof))
			if !errors.Is(err, app_errors.ErrTokenInvalid) {
				t.Fatalf("incomplete proof was accepted: err=%v", err)
			}
			f.assertNothingMinted(t, before)
		})
	}
}

// The session inventory decides whether a credential is minted, so a read
// failure must refuse rather than assume the session is fine.
func TestSwitchAccountFailsClosedWhenSessionsCannotBeRead(t *testing.T) {
	f := newSwitchFixture(t)
	f.svc.refreshTokens = unreadableSessions{f.store}
	before := len(f.store.byHash)
	pair, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), f.proof)
	if err == nil || pair != nil {
		t.Fatalf("switch succeeded while the session inventory was unreadable: pair=%v err=%v", pair != nil, err)
	}
	f.assertNothingMinted(t, before)
}

// RequireLiveSession is the same gate exposed for the device terminal, which
// re-asks it on a timer with the proof off the access token that opened the
// shell. Three properties the timer depends on: a refresh that ROTATES the
// family keeps the original proof valid (the browser refreshing must not end
// the shell), a sign-out ends it, and a password change ends it.
func TestRequireLiveSessionSurvivesARefreshRotation(t *testing.T) {
	f := newSwitchFixture(t)
	if _, err := f.svc.RefreshToken(f.original.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RequireLiveSession(context.Background(), f.usr.ID, f.proof); err != nil {
		t.Fatalf("the original proof must stay live across a refresh rotation: %v", err)
	}
}

func TestRequireLiveSessionRefusesAfterSignOut(t *testing.T) {
	f := newSwitchFixture(t)
	if err := f.svc.RevokeSession(context.Background(), f.original.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RequireLiveSession(context.Background(), f.usr.ID, f.proof); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("signed-out session still live: err=%v", err)
	}
}

func TestRequireLiveSessionRefusesAfterAPasswordChange(t *testing.T) {
	f := newSwitchFixture(t)
	changed := f.proof.IssuedAt.Add(time.Second)
	f.usr.PasswordChangedAt = &changed
	if err := f.svc.RequireLiveSession(context.Background(), f.usr.ID, f.proof); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("token predating the password change still live: err=%v", err)
	}
}

// The session inventory decides whether a root shell stays open, so a read
// failure refuses rather than assumes.
func TestRequireLiveSessionFailsClosedWhenSessionsCannotBeRead(t *testing.T) {
	f := newSwitchFixture(t)
	f.svc.refreshTokens = unreadableSessions{f.store}
	if err := f.svc.RequireLiveSession(context.Background(), f.usr.ID, f.proof); err == nil {
		t.Fatal("liveness passed while the session inventory was unreadable")
	}
}
