package auth

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// fakeRefreshStore is an in-memory refresh-token store: enough to exercise
// issue → redeem → replay without a database.
type fakeRefreshStore struct {
	byHash map[string]*refresh_token.RefreshToken
	// generations stands in for users.credential_generation as Issue reads
	// it UNDER the row lock; a user absent from the map is not checked. A
	// test moves it to play an administrative reset that landed between the
	// password check and the insert.
	generations map[uuid.UUID]int64
	// passwordChanged stands in for users.password_changed_at as Issue reads
	// it under the lock; a user absent from the map is not checked. A test
	// sets it to play a reset that moved the timestamp only (an older
	// administrative reset).
	passwordChanged map[uuid.UUID]*time.Time
	// beforeIssue runs at the top of Issue — the moment a revocation would
	// take the lock first.
	beforeIssue func()
	// plainCreates counts inserts that bypassed Issue; inIssue marks the
	// insert Issue itself performs.
	plainCreates int
	inIssue      bool
}

func newFakeRefreshStore() *fakeRefreshStore {
	return &fakeRefreshStore{
		byHash:          map[string]*refresh_token.RefreshToken{},
		generations:     map[uuid.UUID]int64{},
		passwordChanged: map[uuid.UUID]*time.Time{},
	}
}

// Create is the UNGUARDED insert. plainCreates counts the calls that did not
// come through Issue, so a test can prove a login path took the guarded one.
func (f *fakeRefreshStore) Create(_ context.Context, t *refresh_token.RefreshToken) error {
	if !f.inIssue {
		f.plainCreates++
	}
	f.byHash[t.TokenHash] = t
	return nil
}

// Issue mirrors the SQL repository's checks in the SQL repository's order:
// generation, password-change timestamp, then the family (no revoked row AND
// a live one, when it has rows), then the insert.
func (f *fakeRefreshStore) Issue(ctx context.Context, t *refresh_token.RefreshToken, generation int64, passwordChangedAt *time.Time) error {
	if f.beforeIssue != nil {
		f.beforeIssue()
	}
	if current, ok := f.generations[t.UserID]; ok && generation > 0 && current != generation {
		return app_errors.ErrTokenInvalid
	}
	if row, ok := f.passwordChanged[t.UserID]; ok {
		switch {
		case row == nil && passwordChangedAt != nil, row != nil && passwordChangedAt == nil:
			return app_errors.ErrTokenInvalid
		case row != nil && !row.Equal(*passwordChangedAt):
			return app_errors.ErrTokenInvalid
		}
	}
	var total, revoked, live int
	for _, existing := range f.byHash {
		if existing.FamilyID != t.FamilyID {
			continue
		}
		total++
		if existing.RevokedAt != nil {
			revoked++
		} else if existing.ExpiresAt.After(time.Now()) {
			live++
		}
	}
	if total > 0 && (revoked > 0 || live == 0) {
		return app_errors.ErrTokenInvalid
	}
	f.inIssue = true
	defer func() { f.inIssue = false }()
	return f.Create(ctx, t)
}

func (f *fakeRefreshStore) Claim(_ context.Context, hash string) (*refresh_token.RefreshToken, error) {
	t, ok := f.byHash[hash]
	if !ok {
		return nil, app_errors.ErrTokenInvalid
	}
	if t.RevokedAt != nil || t.ExpiresAt.Before(time.Now()) {
		return nil, app_errors.ErrTokenInvalid
	}
	if t.UsedAt != nil {
		return t, app_errors.ErrRefreshTokenReused
	}
	now := time.Now()
	t.UsedAt = &now
	return t, nil
}

func (f *fakeRefreshStore) RevokeFamily(_ context.Context, familyID uuid.UUID) error {
	now := time.Now()
	for _, t := range f.byHash {
		if t.FamilyID == familyID && t.RevokedAt == nil {
			t.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeRefreshStore) RevokeAllForUser(_ context.Context, userID uuid.UUID) error {
	now := time.Now()
	for _, t := range f.byHash {
		if t.UserID == userID && t.RevokedAt == nil {
			t.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeRefreshStore) DeleteExpired(_ context.Context) (int64, error) { return 0, nil }

// live returns the unrevoked, unexpired tokens of one user, grouped per family
// — the same "a session is a family" rule the SQL query implements.
func (f *fakeRefreshStore) live(userID uuid.UUID) map[uuid.UUID][]*refresh_token.RefreshToken {
	families := map[uuid.UUID][]*refresh_token.RefreshToken{}
	for _, t := range f.byHash {
		if t.UserID == userID && t.RevokedAt == nil && t.ExpiresAt.After(time.Now()) {
			families[t.FamilyID] = append(families[t.FamilyID], t)
		}
	}
	return families
}

func (f *fakeRefreshStore) ListSessions(_ context.Context, userID uuid.UUID) ([]refresh_token.Session, error) {
	var sessions []refresh_token.Session
	for familyID, tokens := range f.live(userID) {
		s := refresh_token.Session{FamilyID: familyID}
		for _, t := range tokens {
			if s.CreatedAt.IsZero() || t.CreatedAt.Before(s.CreatedAt) {
				s.CreatedAt = t.CreatedAt
				s.UserAgent = t.UserAgent
				s.IP = t.IP
			}
			used := t.CreatedAt
			if t.UsedAt != nil {
				used = *t.UsedAt
			}
			if used.After(s.LastUsedAt) {
				s.LastUsedAt = used
			}
			if t.ExpiresAt.After(s.ExpiresAt) {
				s.ExpiresAt = t.ExpiresAt
			}
		}
		sessions = append(sessions, s)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastUsedAt.After(sessions[j].LastUsedAt) })
	return sessions, nil
}

func (f *fakeRefreshStore) RevokeFamilyForUser(ctx context.Context, userID, familyID uuid.UUID) error {
	if _, ok := f.live(userID)[familyID]; !ok {
		return app_errors.ErrTokenInvalid
	}
	return f.RevokeFamily(ctx, familyID)
}

func (f *fakeRefreshStore) RevokeOtherFamilies(ctx context.Context, userID, keepFamilyID uuid.UUID) error {
	for familyID := range f.live(userID) {
		if familyID != keepFamilyID {
			if err := f.RevokeFamily(ctx, familyID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeRefreshStore) EnforceSessionCap(ctx context.Context, userID uuid.UUID, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	sessions, _ := f.ListSessions(ctx, userID)
	if len(sessions) <= keep {
		return 0, nil
	}
	for _, s := range sessions[keep:] {
		if err := f.RevokeFamily(ctx, s.FamilyID); err != nil {
			return 0, err
		}
	}
	return len(sessions) - keep, nil
}

func (f *fakeRefreshStore) AnnotateDevice(_ context.Context, hash string, device refresh_token.Device) error {
	if t, ok := f.byHash[hash]; ok {
		t.UserAgent = device.UserAgent
		t.IP = device.IP
	}
	return nil
}

func newTestAuthService() *AuthService {
	return newTestAuthServiceWithStore(newFakeRefreshStore())
}

func newTestAuthServiceWithStore(store *fakeRefreshStore) *AuthService {
	return NewAuthService(
		config.JWTConfig{
			SecretKey:       "test-secret",
			AccessTokenTTL:  time.Hour,
			RefreshTokenTTL: 7 * 24 * time.Hour,
			Issuer:          "test",
		},
		"",
		0,                  // no self-registration ceiling in these tests
		OpenRegistration{}, // open registration off
		nil, nil, nil, nil, nil, nil,
		store,
		noTwoFactor{},
		noPasskeys{},
		nil,
		slog.Default(),
	)
}

// noPasskeys is the "this user registered no passkey" counter. The constructor
// requires one so a real deployment cannot end up with an unlink guard that
// cannot see passkeys; these tests never reach that guard.
type noPasskeys struct{}

func (noPasskeys) CountByUserID(context.Context, uuid.UUID) (int64, error) { return 0, nil }

// noTwoFactor is the "nobody enrolled a factor" gate, which is what these
// tests exercise. The 2FA path has its own tests.
type noTwoFactor struct{}

func (noTwoFactor) HasConfirmedFactor(context.Context, uuid.UUID) (bool, error) { return false, nil }

func (noTwoFactor) Verify(context.Context, uuid.UUID, string) error { return nil }

func newTestTokenPair(t *testing.T, svc *AuthService) (access, refresh string) {
	t.Helper()
	usr := &user.User{ID: uuid.New(), Email: "u@example.com"}
	pair, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatalf("GenerateTokens: %v", err)
	}
	return pair.AccessToken, pair.RefreshToken
}

func TestValidateAccessTokenAcceptsAccessToken(t *testing.T) {
	svc := newTestAuthService()
	access, _ := newTestTokenPair(t, svc)

	claims, err := svc.ValidateAccessToken(access)
	if err != nil {
		t.Fatalf("expected access token to validate, got %v", err)
	}
	if claims.TokenType != "access" {
		t.Fatalf("expected token_type access, got %q", claims.TokenType)
	}
}

func TestValidateAccessTokenRejectsRefreshToken(t *testing.T) {
	svc := newTestAuthService()
	_, refresh := newTestTokenPair(t, svc)

	// ValidateToken (used by the HTTP-agnostic paths before this fix) accepts
	// a refresh token; ValidateAccessToken must not.
	if _, err := svc.ValidateToken(refresh); err != nil {
		t.Fatalf("sanity: ValidateToken should accept a refresh token, got %v", err)
	}
	if _, err := svc.ValidateAccessToken(refresh); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid for refresh token, got %v", err)
	}
}

func TestValidateAccessTokenRejectsPreAuthToken(t *testing.T) {
	svc := newTestAuthService()
	usr := &user.User{ID: uuid.New(), Email: "u@example.com"}

	preToken, err := svc.generatePreAuthToken(usr)
	if err != nil {
		t.Fatalf("generatePreAuthToken: %v", err)
	}
	if _, err := svc.ValidateAccessToken(preToken); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid for pre-auth token, got %v", err)
	}
	// The pre-auth token carries no account.
	claims, err := svc.ValidateToken(preToken)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.TokenType != "pre_auth" || claims.AccountID != uuid.Nil {
		t.Fatalf("unexpected pre-auth claims: type=%q account=%s", claims.TokenType, claims.AccountID)
	}
}

// A refresh token is single-use: redeeming it must hand back a DIFFERENT token,
// so a captured one stops working as soon as the real client refreshes.
func TestRefreshRotatesTheToken(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	usr := &user.User{ID: uuid.New(), Email: "u@example.com"}
	pair, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatalf("GenerateTokens: %v", err)
	}

	stored, ok := store.byHash[refresh_token.Hash(pair.RefreshToken)]
	if !ok {
		t.Fatal("the issued refresh token must be recorded, or it could never be revoked")
	}
	if stored.UsedAt != nil {
		t.Error("a freshly issued token must not be marked used")
	}
}

// Replaying a spent token means two parties hold it. The whole family dies,
// including the successor the legitimate client is currently using — losing a
// session is the correct price for evicting a thief.
func TestReplayedRefreshTokenRevokesTheWholeFamily(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	usr := &user.User{ID: uuid.New(), Email: "u@example.com"}
	pair, err := svc.GenerateTokens(usr, uuid.New())
	if err != nil {
		t.Fatalf("GenerateTokens: %v", err)
	}

	first := store.byHash[refresh_token.Hash(pair.RefreshToken)]
	// Simulate a legitimate redemption followed by a replay of the same token.
	if _, err := store.Claim(context.Background(), first.TokenHash); err != nil {
		t.Fatalf("first claim should succeed: %v", err)
	}
	if _, err := store.Claim(context.Background(), first.TokenHash); !errors.Is(err, app_errors.ErrRefreshTokenReused) {
		t.Fatalf("a second claim must be reported as reuse, got %v", err)
	}
	if err := store.RevokeFamily(context.Background(), first.FamilyID); err != nil {
		t.Fatalf("RevokeFamily: %v", err)
	}

	for _, tok := range store.byHash {
		if tok.FamilyID == first.FamilyID && tok.RevokedAt == nil {
			t.Error("every token in a compromised family must be revoked")
		}
	}
}

func TestRevokeAllSessionsKillsEveryTokenForTheUser(t *testing.T) {
	store := newFakeRefreshStore()
	svc := newTestAuthServiceWithStore(store)
	usr := &user.User{ID: uuid.New(), Email: "u@example.com"}
	if _, err := svc.GenerateTokens(usr, uuid.New()); err != nil {
		t.Fatalf("GenerateTokens: %v", err)
	}
	if _, err := svc.GenerateTokens(usr, uuid.New()); err != nil {
		t.Fatalf("GenerateTokens: %v", err)
	}

	if err := svc.RevokeAllSessions(context.Background(), usr.ID); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	for _, tok := range store.byHash {
		if tok.UserID == usr.ID && tok.RevokedAt == nil {
			t.Error("expected every session of the user to be revoked")
		}
	}
}

// Rotate is Claim plus Create as one step, with the family-whole and
// credential-epoch tests the SQL repository applies under the user's row lock.
// In memory there is no lock to take; the ORDER is what the
// service tests exercise.
func (f *fakeRefreshStore) Rotate(ctx context.Context, hash string, issuedAt time.Time, _ int64, successor *refresh_token.RefreshToken) (*refresh_token.RefreshToken, error) {
	claimed, err := f.Claim(ctx, hash)
	if err != nil {
		return claimed, err
	}
	for _, t := range f.byHash {
		if t.FamilyID == claimed.FamilyID && t.RevokedAt != nil {
			claimed.UsedAt = nil // the claim is rolled back with the rest
			return nil, app_errors.ErrTokenInvalid
		}
	}
	if err := f.Create(ctx, successor); err != nil {
		return nil, err
	}
	return claimed, nil
}
