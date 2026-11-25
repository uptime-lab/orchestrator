package sql

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The revive-or-conflict upsert is expressed in SQL and sits on the path of
// EVERY OIDC login — the silent email-match link calls Create — so a wrong
// statement would not fail a corner case, it would stop people signing in.
//
// It cannot be checked by a fake. `ON CONFLICT (provider, subject) DO UPDATE
// ... WHERE user_id = ?` has one shape that compiles, runs, and is catastrophic:
// GORM's clause.OnConflict has two WHERE slots, and putting the ownership
// predicate on the conflict target instead of the DO UPDATE would silently
// overwrite ANOTHER user's identity row — handing them the attacker's account.
// Only a real database answers which one was built.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" \
//	  go test ./pkg/webapp/infrastructure/database/sql/ -run TestExternalIdentity
func externalIdentityTestRepo(t *testing.T) *ExternalIdentityRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS external_identities CASCADE`).Error; err != nil {
		t.Fatalf("schema: %v", err)
	}
	repo := NewExternalIdentityRepository(db)
	// Setup() is the production migration, so the test exercises the real
	// schema including the column this feature added.
	if err := repo.Setup(); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return repo
}

func newLink(userID uuid.UUID, subject, email string) *external_identity.ExternalIdentity {
	return &external_identity.ExternalIdentity{
		ID:        uuid.New(),
		UserID:    userID,
		Provider:  "https://accounts.google.com",
		Subject:   subject,
		Email:     email,
		CreatedAt: time.Now(),
	}
}

// A revoked identity must disappear from every read, and re-linking it must
// REVIVE the same row rather than silently doing nothing.
func TestExternalIdentityRevokeThenRelink(t *testing.T) {
	repo := externalIdentityTestRepo(t)
	ctx := context.Background()
	userID := uuid.New()

	link := newLink(userID, "sub-revive", "owner@example.com")
	if err := repo.Create(ctx, link); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Live before revocation.
	if _, err := repo.GetByProviderSubject(ctx, link.Provider, link.Subject); err != nil {
		t.Fatalf("expected the fresh link to resolve, got %v", err)
	}

	if err := repo.Revoke(ctx, userID, link.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Invisible to the login lookup — this is what makes an unlink take effect.
	if _, err := repo.GetByProviderSubject(ctx, link.Provider, link.Subject); !errors.Is(err, app_errors.ErrExternalIdentityNotFound) {
		t.Fatalf("a revoked identity must not resolve, got %v", err)
	}
	// Invisible to the credential count — otherwise a user could unlink their
	// way down to an unreachable account one identity at a time.
	live, err := repo.ListByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("ListByUserID: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("a revoked identity must not be listed, got %d", len(live))
	}
	// But distinguishable from "never linked", which is what stops LoginOIDC's
	// email fallback re-creating it silently.
	revoked, err := repo.RevokedExists(ctx, link.Provider, link.Subject)
	if err != nil {
		t.Fatalf("RevokedExists: %v", err)
	}
	if !revoked {
		t.Fatal("a deliberate unlink must be distinguishable from never having linked")
	}

	// Re-linking revives the row. Under the old ON CONFLICT DO NOTHING this
	// inserted nothing, returned no error, and left the user unable to sign in
	// while being told it had worked.
	again := newLink(userID, "sub-revive", "owner-new@example.com")
	if err := repo.Create(ctx, again); err != nil {
		t.Fatalf("re-link: %v", err)
	}
	got, err := repo.GetByProviderSubject(ctx, link.Provider, link.Subject)
	if err != nil {
		t.Fatalf("expected the revived link to resolve, got %v", err)
	}
	if got.ID != link.ID {
		t.Fatalf("expected the ORIGINAL row revived (%s), got a different one (%s)", link.ID, got.ID)
	}
	if got.Email != "owner-new@example.com" {
		t.Fatalf("expected the email refreshed on revive, got %q", got.Email)
	}
	if again2, _ := repo.ListByUserID(ctx, userID); len(again2) != 1 {
		t.Fatalf("expected exactly one live link after revive, got %d", len(again2))
	}
}

// The IdP account belongs to somebody else: the upsert must REFUSE, not
// reassign. This is the case where a misplaced WHERE would hand one user's
// account to another.
func TestExternalIdentityCreateRefusesAnotherUsersIdentity(t *testing.T) {
	repo := externalIdentityTestRepo(t)
	ctx := context.Background()
	owner := uuid.New()
	stranger := uuid.New()

	original := newLink(owner, "sub-contested", "owner@example.com")
	if err := repo.Create(ctx, original); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err := repo.Create(ctx, newLink(stranger, "sub-contested", "stranger@example.com"))
	if !errors.Is(err, app_errors.ErrExternalIdentityConflict) {
		t.Fatalf("expected ErrExternalIdentityConflict, got %v", err)
	}

	// And the row must be untouched — still the owner's, still their email.
	got, err := repo.GetByProviderSubject(ctx, original.Provider, original.Subject)
	if err != nil {
		t.Fatalf("GetByProviderSubject: %v", err)
	}
	if got.UserID != owner {
		t.Fatalf("the identity was REASSIGNED to another user — expected %s, got %s", owner, got.UserID)
	}
	if got.Email != "owner@example.com" {
		t.Fatalf("the stranger's email overwrote the owner's: %q", got.Email)
	}
	if links, _ := repo.ListByUserID(ctx, stranger); len(links) != 0 {
		t.Fatalf("the stranger must hold no link, got %d", len(links))
	}
}

// A conflict against a REVOKED row that belongs to someone else must also be
// refused. Revocation makes the row invisible to reads while it still occupies
// the unique pair, which is exactly where an "it is not there, so insert" bug
// would live.
func TestExternalIdentityCreateRefusesAnothersRevokedIdentity(t *testing.T) {
	repo := externalIdentityTestRepo(t)
	ctx := context.Background()
	owner := uuid.New()
	stranger := uuid.New()

	original := newLink(owner, "sub-revoked-contested", "owner@example.com")
	if err := repo.Create(ctx, original); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Revoke(ctx, owner, original.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	err := repo.Create(ctx, newLink(stranger, "sub-revoked-contested", "stranger@example.com"))
	if !errors.Is(err, app_errors.ErrExternalIdentityConflict) {
		t.Fatalf("expected ErrExternalIdentityConflict, got %v", err)
	}
	if links, _ := repo.ListByUserID(ctx, stranger); len(links) != 0 {
		t.Fatalf("the stranger must hold no link, got %d", len(links))
	}
}

// Revoke is scoped to its owner, and repeating it reports not-found rather than
// silently re-stamping a newer revocation time over the original.
func TestExternalIdentityRevokeScopeAndIdempotence(t *testing.T) {
	repo := externalIdentityTestRepo(t)
	ctx := context.Background()
	owner := uuid.New()
	stranger := uuid.New()

	link := newLink(owner, "sub-scope", "owner@example.com")
	if err := repo.Create(ctx, link); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Revoke(ctx, stranger, link.ID); !errors.Is(err, app_errors.ErrExternalIdentityNotFound) {
		t.Fatalf("another user must not be able to revoke this link, got %v", err)
	}
	if _, err := repo.GetByProviderSubject(ctx, link.Provider, link.Subject); err != nil {
		t.Fatalf("the link must survive a foreign revoke attempt, got %v", err)
	}

	if err := repo.Revoke(ctx, owner, link.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := repo.Revoke(ctx, owner, link.ID); !errors.Is(err, app_errors.ErrExternalIdentityNotFound) {
		t.Fatalf("a second revoke must report not-found, got %v", err)
	}
}
