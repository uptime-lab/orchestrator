package sql

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/external_identity"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/passkey"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/two_factor"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Why these tests exist. Every one of them is about what happens BETWEEN two statements
// that used to be separate: a whole-row user write from a stale copy after a
// password reset, a refresh successor inserted after its family was revoked,
// an OIDC identity attached after the session that asked for it died, a
// second TOTP confirmation over the first. Fakes cannot show any of it; these
// run against a real Postgres (BACKEND_TEST_DSN) and are skipped otherwise.

type transitionsFixture struct {
	db       *gorm.DB
	users    *UserRepository
	tokens   *RefreshTokenRepository
	identity *ExternalIdentityRepository
	factors  *TwoFactorRepository
	passkeys *PasskeyRepository
}

func newTransitionsFixture(t *testing.T) *transitionsFixture {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), TranslateError: true})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	// The package opens one pool per fixture and never closed any; the
	// contention tests below hold twenty connections at once, and with the
	// idle pairs every earlier fixture left behind the CI Postgres answered
	// "too many clients already" (max_connections 100) to the tests that ran
	// after this file. Bound the pool and close it with the test.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	sqlDB.SetMaxOpenConns(24)
	sqlDB.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, table := range []string{"refresh_tokens", "external_identities", "two_factors", "passkeys", "users"} {
		if err := db.Exec("DROP TABLE IF EXISTS " + table + " CASCADE").Error; err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	f := &transitionsFixture{db: db, users: NewUserRepository(db), tokens: NewRefreshTokenRepository(db),
		identity: NewExternalIdentityRepository(db), factors: NewTwoFactorRepository(db), passkeys: NewPasskeyRepository(db)}
	for _, setup := range []func() error{f.users.Setup, f.tokens.Setup, f.identity.Setup, f.factors.Setup, f.passkeys.Setup} {
		if err := setup(); err != nil {
			t.Fatalf("Setup: %v", err)
		}
	}
	return f
}

func (f *transitionsFixture) user(t *testing.T) *user.User {
	t.Helper()
	u := user.NewUser(uuid.NewString()+"@example.com", "Owner", "hash-v1")
	if err := f.users.Create(context.Background(), u); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	return u
}

func (f *transitionsFixture) token(t *testing.T, u *user.User, family uuid.UUID) *refresh_token.RefreshToken {
	t.Helper()
	rec := refresh_token.New(u.ID, uuid.New(), family, uuid.NewString(), time.Now().Add(time.Hour))
	if err := f.tokens.Create(context.Background(), rec); err != nil {
		t.Fatalf("Create token: %v", err)
	}
	return rec
}

func (f *transitionsFixture) liveTokens(t *testing.T, family uuid.UUID) (unrevoked, total int64) {
	t.Helper()
	if err := f.db.Model(&RefreshTokenModel{}).Where("family_id = ? AND revoked_at IS NULL", family).Count(&unrevoked).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&RefreshTokenModel{}).Where("family_id = ?", family).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	return unrevoked, total
}

// A metadata write from a copy read BEFORE a password change
// no longer carries the old hash back.
func TestBetaAcknowledgementCannotRestoreAStalePassword(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	stale, err := f.users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := time.Now()
	if err := f.users.SetPassword(ctx, u.ID, "hash-v2", changed); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if err := f.users.AcknowledgeBetaNotice(ctx, stale.ID, user.BetaNoticeVersion, time.Now()); err != nil {
		t.Fatalf("AcknowledgeBetaNotice: %v", err)
	}
	after, err := f.users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.HashedPassword != "hash-v2" || after.PasswordChangedAt == nil || after.CredentialGeneration != 2 {
		t.Fatalf("the acknowledgement rolled the credentials back: %+v", after)
	}
	if after.BetaNoticeAckAt == nil {
		t.Fatal("the acknowledgement itself was not written")
	}
	// The control: the whole-row Update from the stale copy DOES roll back —
	// which is why no service calls it for anything but tests any more.
	if err := f.users.Update(ctx, stale); err != nil {
		t.Fatal(err)
	}
	rolled, _ := f.users.GetByID(ctx, u.ID)
	if rolled.HashedPassword != "hash-v1" {
		t.Fatal("control: the whole-row write was expected to restore the stale hash")
	}
}

func TestMarkEmailVerifiedIsConditionalOnTheAddress(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	if _, err := f.users.UpdateProfile(ctx, u.ID, nil, strPtr("moved@example.com")); err != nil {
		t.Fatal(err)
	}
	written, err := f.users.MarkEmailVerified(ctx, u.ID, u.Email, time.Now())
	if err != nil || written {
		t.Fatalf("a stale verification was written for the OLD address: written=%v err=%v", written, err)
	}
	written, err = f.users.MarkEmailVerified(ctx, u.ID, "MOVED@example.com", time.Now())
	if err != nil || !written {
		t.Fatalf("the current address was not verified: written=%v err=%v", written, err)
	}
	after, _ := f.users.GetByID(ctx, u.ID)
	if after.CredentialGeneration != 2 {
		t.Fatalf("an email change must advance the credential generation from 1 to 2, got %d", after.CredentialGeneration)
	}
}

// The old shape — claim, then insert the successor as a
// separate statement — let a revocation land in between and leave an
// unrevoked successor. Shown first as the control, deterministically; then
// Rotate under contention with the revocation, where the invariant must hold
// whichever order the lock hands out.
func TestRotationCannotRecreateARevokedFamily(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)

	// Control: the pre-fix interleaving.
	family := uuid.New()
	old := f.token(t, u, family)
	if _, err := f.tokens.Claim(ctx, old.TokenHash); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := f.tokens.RevokeAllForUser(ctx, u.ID); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	successor := refresh_token.New(u.ID, old.AccountID, family, uuid.NewString(), time.Now().Add(time.Hour))
	if err := f.tokens.Create(ctx, successor); err != nil {
		t.Fatal(err)
	}
	if unrevoked, _ := f.liveTokens(t, family); unrevoked != 1 {
		t.Fatalf("control: expected the old shape to leave one unrevoked successor, saw %d", unrevoked)
	}

	// The fix, under contention: the user's row is held by an outside
	// transaction so that Rotate and the revocation both queue on it, and
	// released with them both waiting. Twenty rounds, either order.
	for round := 0; round < 20; round++ {
		family := uuid.New()
		old := f.token(t, u, family)
		successor := refresh_token.New(u.ID, old.AccountID, family, uuid.NewString(), time.Now().Add(time.Hour))

		holder := f.db.Begin()
		if _, err := lockUserRow(holder, u.ID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var rotateErr, revokeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, rotateErr = f.tokens.Rotate(ctx, old.TokenHash, time.Now().Add(-time.Minute), 0, successor)
		}()
		go func() { defer wg.Done(); revokeErr = f.tokens.RevokeAllForUser(ctx, u.ID) }()
		time.Sleep(50 * time.Millisecond) // both are queued on the lock now
		if err := holder.Rollback().Error; err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		if revokeErr != nil && !errors.Is(revokeErr, app_errors.ErrTokenInvalid) {
			t.Fatalf("round %d: revocation: %v", round, revokeErr)
		}
		unrevoked, total := f.liveTokens(t, family)
		switch {
		case rotateErr == nil && (total != 2 || unrevoked != 0):
			t.Fatalf("round %d: rotation won, then revocation ran — expected 2 rows all revoked, saw %d unrevoked of %d", round, unrevoked, total)
		case errors.Is(rotateErr, app_errors.ErrTokenInvalid) && (total != 1 || unrevoked != 0):
			t.Fatalf("round %d: revocation won — expected the rotation refused with no successor, saw %d rows, %d unrevoked", round, total, unrevoked)
		case rotateErr != nil && !errors.Is(rotateErr, app_errors.ErrTokenInvalid):
			t.Fatalf("round %d: Rotate: %v", round, rotateErr)
		}
	}
}

func TestRotationRefusesATokenPredatingThePasswordChangeUnderTheLock(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	family := uuid.New()
	old := f.token(t, u, family)
	issued := time.Now().Add(-time.Minute)
	if err := f.users.SetPassword(ctx, u.ID, "hash-v2", time.Now()); err != nil {
		t.Fatal(err)
	}
	successor := refresh_token.New(u.ID, old.AccountID, family, uuid.NewString(), time.Now().Add(time.Hour))
	if _, err := f.tokens.Rotate(ctx, old.TokenHash, issued, 0, successor); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("Rotate after a password change = %v, want ErrTokenInvalid", err)
	}
	if _, total := f.liveTokens(t, family); total != 1 {
		t.Fatalf("a successor was inserted for a dead credential epoch: %d rows", total)
	}
}

func TestRotationReportsAReplayWithTheFamily(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	family := uuid.New()
	old := f.token(t, u, family)
	first := refresh_token.New(u.ID, old.AccountID, family, uuid.NewString(), time.Now().Add(time.Hour))
	if _, err := f.tokens.Rotate(ctx, old.TokenHash, time.Now().Add(-time.Minute), 0, first); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	second := refresh_token.New(u.ID, old.AccountID, family, uuid.NewString(), time.Now().Add(time.Hour))
	row, err := f.tokens.Rotate(ctx, old.TokenHash, time.Now().Add(-time.Minute), 0, second)
	if !errors.Is(err, app_errors.ErrRefreshTokenReused) || row == nil || row.FamilyID != family {
		t.Fatalf("replay = (%v, %v), want ErrRefreshTokenReused with the family", row, err)
	}
	if _, total := f.liveTokens(t, family); total != 2 {
		t.Fatalf("a replay inserted a successor: %d rows", total)
	}
}

// The attachment and the live-session test are one
// transaction on the user's row lock.
func TestIdentityAttachmentRefusesADeadSession(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	family := uuid.New()
	f.token(t, u, family)
	issued := time.Now().Add(-time.Minute)

	live := external_identity.New(u.ID, "google", "sub-live", "a@example.com")
	if err := f.identity.CreateWhileSessionLive(ctx, live, family, issued, 0); err != nil {
		t.Fatalf("a live session was refused: %v", err)
	}
	if err := f.tokens.RevokeAllForUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	dead := external_identity.New(u.ID, "google", "sub-dead", "a@example.com")
	if err := f.identity.CreateWhileSessionLive(ctx, dead, family, issued, 0); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("attachment after 'sign out everywhere' = %v, want ErrTokenInvalid", err)
	}
	if _, err := f.identity.GetByProviderSubject(ctx, "google", "sub-dead"); err == nil {
		t.Fatal("the identity was persisted for a revoked session")
	}

	// A fresh family, but an access token minted before a password change.
	family2 := uuid.New()
	f.token(t, u, family2)
	if err := f.users.SetPassword(ctx, u.ID, "hash-v2", time.Now()); err != nil {
		t.Fatal(err)
	}
	stale := external_identity.New(u.ID, "google", "sub-stale", "a@example.com")
	if err := f.identity.CreateWhileSessionLive(ctx, stale, family2, issued, 0); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("attachment from a pre-reset token = %v, want ErrTokenInvalid", err)
	}
}

func TestIdentityAttachmentUnderContentionWithRevocation(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	for round := 0; round < 20; round++ {
		family := uuid.New()
		f.token(t, u, family)
		link := external_identity.New(u.ID, "google", uuid.NewString(), "a@example.com")
		holder := f.db.Begin()
		if _, err := lockUserRow(holder, u.ID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var linkErr, revokeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			linkErr = f.identity.CreateWhileSessionLive(ctx, link, family, time.Now().Add(-time.Minute), 0)
		}()
		go func() { defer wg.Done(); revokeErr = f.tokens.RevokeAllForUser(ctx, u.ID) }()
		time.Sleep(50 * time.Millisecond)
		_ = holder.Rollback()
		wg.Wait()
		if revokeErr != nil {
			t.Fatal(revokeErr)
		}
		_, found := f.identity.GetByProviderSubject(ctx, link.Provider, link.Subject)
		persisted := found == nil
		switch {
		case linkErr == nil && !persisted:
			t.Fatalf("round %d: attachment reported success but nothing is stored", round)
		case errors.Is(linkErr, app_errors.ErrTokenInvalid) && persisted:
			t.Fatalf("round %d: attachment refused yet persisted", round)
		case linkErr != nil && !errors.Is(linkErr, app_errors.ErrTokenInvalid):
			t.Fatalf("round %d: %v", round, linkErr)
		}
		// Either order is legal; what is NOT is an attachment that the
		// revocation never saw. When the attachment won, the family it rode
		// is revoked now — the attachment stands, as the report allows, and
		// the revocation reported completion only after it existed. And
		// AFTER the round, the family is dead, so a further attachment on it
		// must be refused whichever order the round took.
		late := external_identity.New(u.ID, "google", uuid.NewString(), "a@example.com")
		if err := f.identity.CreateWhileSessionLive(ctx, late, family, time.Now().Add(-time.Minute), 0); !errors.Is(err, app_errors.ErrTokenInvalid) {
			t.Fatalf("round %d: an attachment after the revocation completed = %v, want ErrTokenInvalid", round, err)
		}
	}
}

// A replay RACING the legitimate rotation — the realistic theft shape — must
// be classified as a replay so the family is revoked, not as a plain invalid
// token (classifying from a snapshot read before the lock would do exactly
// that).
func TestConcurrentPresentationOfOneTokenIsAReplay(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	for round := 0; round < 10; round++ {
		family := uuid.New()
		old := f.token(t, u, family)
		holder := f.db.Begin()
		if _, err := lockUserRow(holder, u.ID); err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			successor := refresh_token.New(u.ID, old.AccountID, family, uuid.NewString(), time.Now().Add(time.Hour))
			go func() {
				_, err := f.tokens.Rotate(ctx, old.TokenHash, time.Now().Add(-time.Minute), 0, successor)
				results <- err
			}()
		}
		time.Sleep(50 * time.Millisecond)
		_ = holder.Rollback()
		var ok, replay int
		for i := 0; i < 2; i++ {
			switch err := <-results; {
			case err == nil:
				ok++
			case errors.Is(err, app_errors.ErrRefreshTokenReused):
				replay++
			default:
				t.Fatalf("round %d: unexpected %v", round, err)
			}
		}
		if ok != 1 || replay != 1 {
			t.Fatalf("round %d: %d succeeded, %d classified as replay; want exactly one of each", round, ok, replay)
		}
	}
}

// The credential generation is a predicate, not a counter nobody reads: a
// token minted before a sign-in email change is refused for rotation and for
// an identity attachment, although PasswordChangedAt did not move.
func TestCredentialGenerationRefusesTokensMintedBeforeAnEmailChange(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	family := uuid.New()
	old := f.token(t, u, family)
	issued := time.Now().Add(-time.Minute)
	// A fresh user is at generation 1, and a token minted now carries 1.
	const mintedAt = int64(1)
	if u.CredentialGeneration != mintedAt {
		t.Fatalf("a new user starts at generation 1, got %d", u.CredentialGeneration)
	}
	successor := refresh_token.New(u.ID, old.AccountID, family, uuid.NewString(), time.Now().Add(time.Hour))
	if _, err := f.tokens.Rotate(ctx, old.TokenHash, issued, mintedAt, successor); err != nil {
		t.Fatalf("a token of the current generation was refused: %v", err)
	}
	// The sign-in email changes: generation 2. The token still says 1.
	if _, err := f.users.UpdateProfile(ctx, u.ID, nil, strPtr("moved@example.com")); err != nil {
		t.Fatal(err)
	}
	next := refresh_token.New(u.ID, old.AccountID, family, uuid.NewString(), time.Now().Add(time.Hour))
	if _, err := f.tokens.Rotate(ctx, successor.TokenHash, issued, mintedAt, next); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a token minted before the email change rotated: %v", err)
	}
	link := external_identity.New(u.ID, "google", "sub-gen", "a@example.com")
	if err := f.identity.CreateWhileSessionLive(ctx, link, family, issued, mintedAt); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("an attachment from a token minted before the email change = %v, want ErrTokenInvalid", err)
	}
	// A token from before the claim (0) is still tolerated, by design.
	family2 := uuid.New()
	old2 := f.token(t, u, family2)
	legacy := refresh_token.New(u.ID, old2.AccountID, family2, uuid.NewString(), time.Now().Add(time.Hour))
	if _, err := f.tokens.Rotate(ctx, old2.TokenHash, issued, 0, legacy); err != nil {
		t.Fatalf("a pre-claim token (generation 0) must still rotate: %v", err)
	}
}

// Rows created before the column existed are lifted to generation 1 by
// Setup, so a 0 on a token means one thing only.
func TestSetupLiftsGenerationZeroRowsToOne(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	if err := f.db.Exec(`UPDATE users SET credential_generation = 0 WHERE id = ?`, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.users.Setup(); err != nil {
		t.Fatal(err)
	}
	after, err := f.users.GetByID(ctx, u.ID)
	if err != nil || after.CredentialGeneration != 1 {
		t.Fatalf("generation after Setup = %d, %v; want 1", after.CredentialGeneration, err)
	}
}

// The pending/enabled transitions are single statements.
func TestTwoFactorTransitionsAreConditional(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	now := time.Now()
	pending := &two_factor.TwoFactor{UserID: u.ID, SecretCiphertext: "secret-1", CreatedAt: now, UpdatedAt: now}
	if err := f.factors.CreatePending(ctx, pending); err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	// A second enrolment while still pending replaces the secret.
	replaced := &two_factor.TwoFactor{UserID: u.ID, SecretCiphertext: "secret-2", CreatedAt: now, UpdatedAt: now}
	if err := f.factors.CreatePending(ctx, replaced); err != nil {
		t.Fatalf("CreatePending over a pending factor: %v", err)
	}
	// Confirming the FIRST secret now fails: it is not the pending one.
	stale := *pending
	stale.Confirm([]string{"h1"})
	if err := f.factors.ConfirmPending(ctx, &stale, "secret-1"); !errors.Is(err, app_errors.ErrTwoFactorEnrollmentChanged) {
		t.Fatalf("confirming a replaced secret = %v, want ErrTwoFactorEnrollmentChanged", err)
	}
	current := *replaced
	current.Confirm([]string{"h2"})
	if err := f.factors.ConfirmPending(ctx, &current, "secret-2"); err != nil {
		t.Fatalf("ConfirmPending: %v", err)
	}
	if err := f.factors.ConfirmPending(ctx, &current, "secret-2"); !errors.Is(err, app_errors.ErrTwoFactorAlreadyEnabled) {
		t.Fatalf("a second confirmation = %v, want a refusal", err)
	}
	// And no enrolment can replace an enabled factor.
	again := &two_factor.TwoFactor{UserID: u.ID, SecretCiphertext: "secret-3", CreatedAt: now, UpdatedAt: now}
	if err := f.factors.CreatePending(ctx, again); !errors.Is(err, app_errors.ErrTwoFactorAlreadyEnabled) {
		t.Fatalf("CreatePending over an enabled factor = %v, want a refusal", err)
	}
	stored, err := f.factors.GetByUserID(ctx, u.ID)
	if err != nil || !stored.Enabled() || stored.SecretCiphertext != "secret-2" {
		t.Fatalf("stored factor = %+v, %v", stored, err)
	}
}

// ---------------------------------------------------------------------------
// Same shape as above: what happens BETWEEN two statements, against a
// real Postgres, with the pre-fix interleaving kept as the control.
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }

// A name-only profile write that read its snapshot
// BEFORE a concurrent email change and commits AFTER it leaves the new
// address, its unverified state and the advanced generation intact — the
// statement carries no email at all, and the transition is decided against
// the stored value.
func TestNameOnlyProfileWriteCannotRestoreAStaleEmail(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	if _, err := f.users.MarkEmailVerified(ctx, u.ID, u.Email, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The name-only request reads its snapshot first...
	stale, err := f.users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// ...another request moves the sign-in address...
	change, err := f.users.UpdateProfile(ctx, u.ID, nil, strPtr("Moved@Example.com"))
	if err != nil || !change.EmailChanged || change.PreviousEmail != u.Email {
		t.Fatalf("email change = %+v, %v; want EmailChanged with the previous address", change, err)
	}
	// ...and the name-only request commits after it, from its snapshot.
	change, err = f.users.UpdateProfile(ctx, stale.ID, strPtr("Renamed"), nil)
	if err != nil || change.EmailChanged {
		t.Fatalf("name-only write = %+v, %v; want no email transition", change, err)
	}
	after, err := f.users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Email != "moved@example.com" || after.EmailVerifiedAt != nil || after.CredentialGeneration != 2 || after.Name != "Renamed" {
		t.Fatalf("the name-only write disturbed the email transition: %+v", after)
	}
	// A case-only spelling of the same address is not a change: nothing
	// clears, nothing advances.
	change, err = f.users.UpdateProfile(ctx, u.ID, nil, strPtr("MOVED@example.com"))
	if err != nil || change.EmailChanged {
		t.Fatalf("case-only change = %+v, %v; want no transition", change, err)
	}
	if again, _ := f.users.GetByID(ctx, u.ID); again.CredentialGeneration != 2 || again.Email != "moved@example.com" {
		t.Fatalf("a case-only change moved the generation or the address: %+v", again)
	}
	// An unknown row is not found, not silently zero rows.
	if _, err := f.users.UpdateProfile(ctx, uuid.New(), strPtr("x"), nil); !errors.Is(err, app_errors.ErrUserNotFound) {
		t.Fatalf("unknown user = %v, want ErrUserNotFound", err)
	}
	// The control: the pre-fix shape — both columns written from the caller's
	// snapshot, "unchanged" taken from the caller — restores the old address
	// with the verification and the generation describing a different
	// transition. That is the statement the repository no longer runs.
	if err := f.db.Exec(`UPDATE users SET name = ?, email = ? WHERE id = ?`, stale.Name, stale.Email, stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	rolled, _ := f.users.GetByID(ctx, u.ID)
	if rolled.Email != u.Email || rolled.CredentialGeneration != 2 {
		t.Fatalf("control: the snapshot write was expected to roll the address back under generation 2, got %+v", rolled)
	}
}

// The password write carries the grant's address in its own
// WHERE, so an email change that commits after the service's check and
// before the write refuses the reset and leaves the hash alone.
func TestPasswordResetWriteIsRefusedAfterAConcurrentEmailChange(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	grantAddress := u.Email // what the service's check compared against
	if _, err := f.users.UpdateProfile(ctx, u.ID, nil, strPtr("moved@example.com")); err != nil {
		t.Fatal(err)
	}
	if err := f.users.SetPasswordIfEmail(ctx, u.ID, "hash-v2", grantAddress, time.Now()); !errors.Is(err, app_errors.ErrPasswordResetStale) {
		t.Fatalf("reset write after the address moved = %v, want ErrPasswordResetStale", err)
	}
	after, _ := f.users.GetByID(ctx, u.ID)
	if after.HashedPassword != "hash-v1" || after.PasswordChangedAt != nil || after.CredentialGeneration != 2 {
		t.Fatalf("a stale grant replaced the password: %+v", after)
	}
	// The current address, in any case, is accepted and advances the generation.
	if err := f.users.SetPasswordIfEmail(ctx, u.ID, "hash-v2", "MOVED@example.com", time.Now()); err != nil {
		t.Fatalf("reset write for the current address: %v", err)
	}
	if after, _ = f.users.GetByID(ctx, u.ID); after.HashedPassword != "hash-v2" || after.PasswordChangedAt == nil || after.CredentialGeneration != 3 {
		t.Fatalf("the accepted reset did not land as one transition: %+v", after)
	}
	if err := f.users.SetPasswordIfEmail(ctx, uuid.New(), "hash", "nobody@example.com", time.Now()); !errors.Is(err, app_errors.ErrUserNotFound) {
		t.Fatalf("unknown user = %v, want ErrUserNotFound", err)
	}

	// Under contention: the email change and the reset write queue on the
	// row; whichever order the lock hands out, the hash changes only if the
	// address was still the grant's at the moment of the write.
	for round := 0; round < 20; round++ {
		v := f.user(t)
		grant := v.Email
		holder := f.db.Begin()
		if _, err := lockUserRow(holder, v.ID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var resetErr, moveErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			resetErr = f.users.SetPasswordIfEmail(ctx, v.ID, "hash-v2", grant, time.Now())
		}()
		go func() {
			defer wg.Done()
			_, moveErr = f.users.UpdateProfile(ctx, v.ID, nil, strPtr(uuid.NewString()+"@moved.example.com"))
		}()
		time.Sleep(50 * time.Millisecond)
		_ = holder.Rollback()
		wg.Wait()
		if moveErr != nil {
			t.Fatalf("round %d: email change: %v", round, moveErr)
		}
		got, _ := f.users.GetByID(ctx, v.ID)
		switch {
		case resetErr == nil && (got.HashedPassword != "hash-v2" || got.CredentialGeneration != 3):
			t.Fatalf("round %d: reset won — expected the new hash at generation 3, got %+v", round, got)
		case errors.Is(resetErr, app_errors.ErrPasswordResetStale) && (got.HashedPassword != "hash-v1" || got.CredentialGeneration != 2):
			t.Fatalf("round %d: email change won — expected the old hash at generation 2, got %+v", round, got)
		case resetErr != nil && !errors.Is(resetErr, app_errors.ErrPasswordResetStale):
			t.Fatalf("round %d: SetPasswordIfEmail: %v", round, resetErr)
		}
	}

	// The control: the unconditional SetPassword writes after an email change
	// regardless — which is why the reset no longer calls it.
	w := f.user(t)
	if _, err := f.users.UpdateProfile(ctx, w.ID, nil, strPtr("elsewhere@example.com")); err != nil {
		t.Fatal(err)
	}
	if err := f.users.SetPassword(ctx, w.ID, "hash-v2", time.Now()); err != nil {
		t.Fatalf("control: the unconditional write was expected to succeed: %v", err)
	}
}

// Issuing a sibling into an existing family (an
// account switch) races a revocation. Either the issuance commits first and
// the revocation then covers it, or the revocation wins and nothing is
// stored. Twenty rounds, either order.
func TestIssuanceIntoAFamilyUnderContentionWithRevocation(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)

	// Control: the pre-fix shape, a bare Create after the revocation.
	family := uuid.New()
	f.token(t, u, family)
	if err := f.tokens.RevokeAllForUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.tokens.Create(ctx, refresh_token.New(u.ID, uuid.New(), family, uuid.NewString(), time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if unrevoked, _ := f.liveTokens(t, family); unrevoked != 1 {
		t.Fatalf("control: expected the old shape to leave one unrevoked sibling, saw %d", unrevoked)
	}
	// And the guarded insert refuses the same family outright.
	if err := f.tokens.Issue(ctx, refresh_token.New(u.ID, uuid.New(), family, uuid.NewString(), time.Now().Add(time.Hour)), 0, u.PasswordChangedAt); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("Issue into a revoked family = %v, want ErrTokenInvalid", err)
	}
	// A family whose rows have all EXPIRED is not live either, although none
	// is revoked — the same definition lockLiveSession uses (the two
	// predicates must not differ).
	expired := uuid.New()
	if err := f.tokens.Create(ctx, refresh_token.New(u.ID, uuid.New(), expired, uuid.NewString(), time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := f.tokens.Issue(ctx, refresh_token.New(u.ID, uuid.New(), expired, uuid.NewString(), time.Now().Add(time.Hour)), 0, u.PasswordChangedAt); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("Issue into an expired family = %v, want ErrTokenInvalid", err)
	}

	for round := 0; round < 20; round++ {
		family := uuid.New()
		f.token(t, u, family)
		sibling := refresh_token.New(u.ID, uuid.New(), family, uuid.NewString(), time.Now().Add(time.Hour))
		holder := f.db.Begin()
		if _, err := lockUserRow(holder, u.ID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var issueErr, revokeErr error
		wg.Add(2)
		go func() { defer wg.Done(); issueErr = f.tokens.Issue(ctx, sibling, 0, u.PasswordChangedAt) }()
		go func() { defer wg.Done(); revokeErr = f.tokens.RevokeAllForUser(ctx, u.ID) }()
		time.Sleep(50 * time.Millisecond)
		_ = holder.Rollback()
		wg.Wait()
		if revokeErr != nil {
			t.Fatalf("round %d: revocation: %v", round, revokeErr)
		}
		unrevoked, total := f.liveTokens(t, family)
		switch {
		case issueErr == nil && (total != 2 || unrevoked != 0):
			t.Fatalf("round %d: issuance won, then revocation ran — expected 2 rows all revoked, saw %d unrevoked of %d", round, unrevoked, total)
		case errors.Is(issueErr, app_errors.ErrTokenInvalid) && (total != 1 || unrevoked != 0):
			t.Fatalf("round %d: revocation won — expected nothing stored, saw %d rows, %d unrevoked", round, total, unrevoked)
		case issueErr != nil && !errors.Is(issueErr, app_errors.ErrTokenInvalid):
			t.Fatalf("round %d: Issue: %v", round, issueErr)
		}
		late := refresh_token.New(u.ID, uuid.New(), family, uuid.NewString(), time.Now().Add(time.Hour))
		if err := f.tokens.Issue(ctx, late, 0, u.PasswordChangedAt); !errors.Is(err, app_errors.ErrTokenInvalid) {
			t.Fatalf("round %d: issuance after the revocation completed = %v, want ErrTokenInvalid", round, err)
		}
	}
}

// A reset that lands between the password check (which read
// the epoch the pair is minted from) and the insert makes Issue refuse under
// the lock. Both shapes of reset are replayed here as raw statements — an
// administrative reset that advances the generation (hash, timestamp,
// generation) and an older one (hash and timestamp only). They REPLAY the
// statement's shape; they run no operator tool's code.
func TestIssuanceRefusesACredentialEpochMovedByAnAdministrativeReset(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	minted, mintedAt := u.CredentialGeneration, u.PasswordChangedAt // the snapshot: generation 1, never changed
	tok := refresh_token.New(u.ID, uuid.New(), uuid.New(), uuid.NewString(), time.Now().Add(time.Hour))
	// The current admin statement's shape: hash, timestamp, generation.
	if err := f.db.Exec(`UPDATE users SET hashed_password = ?, password_changed_at = now(), credential_generation = credential_generation + 1, updated_at = now() WHERE id = ?`, "hash-admin", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.tokens.Issue(ctx, tok, minted, mintedAt); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("Issue after an administrative reset = %v, want ErrTokenInvalid", err)
	}
	if _, total := f.liveTokens(t, tok.FamilyID); total != 0 {
		t.Fatalf("a session was stored for a password that had been replaced: %d rows", total)
	}
	// A login that read the row AFTER the reset issues normally — the
	// snapshot's timestamp, read back through GORM, compares equal to the
	// column under the microsecond rounding.
	fresh, err := f.users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.PasswordChangedAt == nil {
		t.Fatal("fixture: the reset did not stamp password_changed_at")
	}
	if err := f.tokens.Issue(ctx, refresh_token.New(u.ID, uuid.New(), uuid.New(), uuid.NewString(), time.Now().Add(time.Hour)), fresh.CredentialGeneration, fresh.PasswordChangedAt); err != nil {
		t.Fatalf("a post-reset login was refused: %v", err)
	}
	// A pair minted without the claim (the deploy minute) is tolerated on the
	// generation, as in Rotate; the timestamp is still compared.
	if err := f.tokens.Issue(ctx, refresh_token.New(u.ID, uuid.New(), uuid.New(), uuid.NewString(), time.Now().Add(time.Hour)), 0, fresh.PasswordChangedAt); err != nil {
		t.Fatalf("a pre-claim issuance (generation 0) was refused: %v", err)
	}
	if err := f.tokens.Issue(ctx, refresh_token.New(uuid.New(), uuid.New(), uuid.New(), uuid.NewString(), time.Now().Add(time.Hour)), 1, nil); !errors.Is(err, app_errors.ErrUserNotFound) {
		t.Fatalf("Issue for an unknown user = %v, want ErrUserNotFound", err)
	}

	// The older admin statement's shape — hash and timestamp, no
	// generation — is refused as well: the snapshot's PasswordChangedAt (nil)
	// no longer matches the row's, so the guard does not depend on an
	// administrative reset advancing the generation. (Without the timestamp
	// arm this would be the control that PASSES.)
	v := f.user(t)
	if err := f.db.Exec(`UPDATE users SET hashed_password = ?, password_changed_at = now(), updated_at = now() WHERE id = ?`, "hash-admin", v.ID).Error; err != nil {
		t.Fatal(err)
	}
	stale := refresh_token.New(v.ID, uuid.New(), uuid.New(), uuid.NewString(), time.Now().Add(time.Hour))
	if err := f.tokens.Issue(ctx, stale, v.CredentialGeneration, v.PasswordChangedAt); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("Issue after a timestamp-only reset = %v, want ErrTokenInvalid", err)
	}
	if _, total := f.liveTokens(t, stale.FamilyID); total != 0 {
		t.Fatalf("a session was stored after a timestamp-only reset: %d rows", total)
	}
	// The control for THAT arm: the unguarded Create stores regardless —
	// which is why no login path calls it.
	if err := f.tokens.Create(ctx, stale); err != nil {
		t.Fatalf("control: the bare insert was expected to succeed: %v", err)
	}
	// And a second reset a moment later moves the timestamp again: a
	// snapshot read between the two is refused, one read after passes.
	between, _ := f.users.GetByID(ctx, v.ID)
	time.Sleep(2 * time.Millisecond)
	if err := f.db.Exec(`UPDATE users SET password_changed_at = now(), updated_at = now() WHERE id = ?`, v.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.tokens.Issue(ctx, refresh_token.New(v.ID, uuid.New(), uuid.New(), uuid.NewString(), time.Now().Add(time.Hour)), between.CredentialGeneration, between.PasswordChangedAt); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("Issue from a snapshot between two resets = %v, want ErrTokenInvalid", err)
	}
	after, _ := f.users.GetByID(ctx, v.ID)
	if err := f.tokens.Issue(ctx, refresh_token.New(v.ID, uuid.New(), uuid.New(), uuid.NewString(), time.Now().Add(time.Hour)), after.CredentialGeneration, after.PasswordChangedAt); err != nil {
		t.Fatalf("Issue from the current snapshot was refused: %v", err)
	}
}

// A passkey insert and a factor confirmation are refused once
// the session's family is revoked or the generation advanced, exactly as an
// identity attachment is; with the current proof both pass.
func TestPasskeyAndFactorWritesRefuseADeadSession(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	family := uuid.New()
	f.token(t, u, family)
	issued := time.Now().Add(-time.Minute)
	gen := u.CredentialGeneration
	now := time.Now()
	newKey := func(id string) *passkey.Passkey {
		return passkey.New(u.ID, []byte(id), []byte("pub"), nil, 0, false, false, id)
	}
	pendingFactor := func(secret string) *two_factor.TwoFactor {
		p := &two_factor.TwoFactor{UserID: u.ID, SecretCiphertext: secret, CreatedAt: now, UpdatedAt: now}
		if err := f.factors.CreatePending(ctx, p); err != nil {
			t.Fatalf("CreatePending: %v", err)
		}
		c := *p
		c.Confirm([]string{"h-" + secret})
		return &c
	}
	factorEnabled := func() bool {
		stored, err := f.factors.GetByUserID(ctx, u.ID)
		return err == nil && stored.Enabled()
	}

	// Live session: both writes land.
	if err := f.passkeys.CreateWhileSessionLive(ctx, newKey("cred-live"), family, issued, gen); err != nil {
		t.Fatalf("a live session was refused a passkey: %v", err)
	}
	if err := f.factors.ConfirmPendingWhileSessionLive(ctx, pendingFactor("s-live"), "s-live", family, issued, gen); err != nil {
		t.Fatalf("a live session was refused a confirmation: %v", err)
	}
	if !factorEnabled() {
		t.Fatal("the factor was not enabled")
	}

	// Revoked family: both refused, nothing stored, factor stays pending.
	if err := f.factors.Delete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	dead := pendingFactor("s-dead")
	if err := f.tokens.RevokeAllForUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.passkeys.CreateWhileSessionLive(ctx, newKey("cred-dead"), family, issued, gen); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("passkey after 'sign out everywhere' = %v, want ErrTokenInvalid", err)
	}
	if err := f.factors.ConfirmPendingWhileSessionLive(ctx, dead, "s-dead", family, issued, gen); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("confirmation after 'sign out everywhere' = %v, want ErrTokenInvalid", err)
	}
	if n, _ := f.passkeys.CountByUserID(ctx, u.ID); n != 1 {
		t.Fatalf("a passkey was stored for a revoked session: %d keys", n)
	}
	if factorEnabled() {
		t.Fatal("a revoked session enabled the factor")
	}

	// A fresh live family, but the generation advanced by an email change:
	// a proof carrying the old generation is refused, the current one passes.
	family2 := uuid.New()
	f.token(t, u, family2)
	if _, err := f.users.UpdateProfile(ctx, u.ID, nil, strPtr("moved@example.com")); err != nil {
		t.Fatal(err)
	}
	if err := f.passkeys.CreateWhileSessionLive(ctx, newKey("cred-stale"), family2, issued, gen); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("passkey from a pre-email-change token = %v, want ErrTokenInvalid", err)
	}
	if err := f.factors.ConfirmPendingWhileSessionLive(ctx, dead, "s-dead", family2, issued, gen); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("confirmation from a pre-email-change token = %v, want ErrTokenInvalid", err)
	}
	if err := f.passkeys.CreateWhileSessionLive(ctx, newKey("cred-current"), family2, issued, gen+1); err != nil {
		t.Fatalf("passkey with the current generation refused: %v", err)
	}
	if err := f.factors.ConfirmPendingWhileSessionLive(ctx, dead, "s-dead", family2, issued, gen+1); err != nil {
		t.Fatalf("confirmation with the current generation refused: %v", err)
	}
	if !factorEnabled() {
		t.Fatal("the factor was not enabled under the current generation")
	}
	// No proof at all is refused, not treated as any session.
	if err := f.passkeys.CreateWhileSessionLive(ctx, newKey("cred-noproof"), uuid.Nil, issued, gen+1); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("passkey with no family = %v, want ErrTokenInvalid", err)
	}

	// The controls: the unguarded writes succeed after the revocation — which
	// is why no service calls them any more.
	if err := f.tokens.RevokeAllForUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.passkeys.Create(ctx, newKey("cred-control")); err != nil {
		t.Fatalf("control: the bare insert was expected to succeed: %v", err)
	}
	if err := f.factors.Delete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.factors.ConfirmPending(ctx, pendingFactor("s-control"), "s-control"); err != nil {
		t.Fatalf("control: the bare confirmation was expected to succeed: %v", err)
	}
}

func TestPasskeyInsertUnderContentionWithRevocation(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	for round := 0; round < 20; round++ {
		family := uuid.New()
		f.token(t, u, family)
		key := passkey.New(u.ID, []byte(uuid.NewString()), []byte("pub"), nil, 0, false, false, "k")
		holder := f.db.Begin()
		if _, err := lockUserRow(holder, u.ID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var createErr, revokeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			createErr = f.passkeys.CreateWhileSessionLive(ctx, key, family, time.Now().Add(-time.Minute), 0)
		}()
		go func() { defer wg.Done(); revokeErr = f.tokens.RevokeAllForUser(ctx, u.ID) }()
		time.Sleep(50 * time.Millisecond)
		_ = holder.Rollback()
		wg.Wait()
		if revokeErr != nil {
			t.Fatal(revokeErr)
		}
		_, lookup := f.passkeys.GetByCredentialID(ctx, key.CredentialID)
		persisted := lookup == nil
		switch {
		case createErr == nil && !persisted:
			t.Fatalf("round %d: insert reported success but nothing is stored", round)
		case errors.Is(createErr, app_errors.ErrTokenInvalid) && persisted:
			t.Fatalf("round %d: insert refused yet persisted", round)
		case createErr != nil && !errors.Is(createErr, app_errors.ErrTokenInvalid):
			t.Fatalf("round %d: %v", round, createErr)
		}
		late := passkey.New(u.ID, []byte(uuid.NewString()), []byte("pub"), nil, 0, false, false, "late")
		if err := f.passkeys.CreateWhileSessionLive(ctx, late, family, time.Now().Add(-time.Minute), 0); !errors.Is(err, app_errors.ErrTokenInvalid) {
			t.Fatalf("round %d: an insert after the revocation completed = %v, want ErrTokenInvalid", round, err)
		}
	}
}

// The password change and the
// profile update sit behind the same step-up guard as the passkey and the
// factor, and now persist under the same session boundary — refused once the
// family is revoked or the generation moved, with nothing written.
func TestPasswordAndProfileWritesRefuseADeadSession(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	u := f.user(t)
	family := uuid.New()
	f.token(t, u, family)
	issued := time.Now().Add(-time.Minute)
	gen := u.CredentialGeneration

	// Live session: both land, each advancing the generation. The profile
	// change goes FIRST: a password change moves PasswordChangedAt, after
	// which a token issued a minute ago is no longer live — against a real
	// Postgres the reverse order is refused exactly as the invariant says it
	// must be.
	change, err := f.users.UpdateProfileWhileSessionLive(ctx, u.ID, strPtr("Renamed"), strPtr("moved@example.com"), family, issued, gen)
	if err != nil || !change.EmailChanged {
		t.Fatalf("a live session was refused a profile change: %+v, %v", change, err)
	}
	gen++
	if err := f.users.SetPasswordWhileSessionLive(ctx, u.ID, "hash-v2", time.Now(), family, issued, gen); err != nil {
		t.Fatalf("a live session was refused a password change: %v", err)
	}
	gen++
	after, _ := f.users.GetByID(ctx, u.ID)
	if after.HashedPassword != "hash-v2" || after.Email != "moved@example.com" || after.Name != "Renamed" || after.CredentialGeneration != gen {
		t.Fatalf("live writes did not land as expected: %+v", after)
	}

	// A proof carrying the generation from before the email change is
	// refused — for both writes, nothing changes.
	if err := f.users.SetPasswordWhileSessionLive(ctx, u.ID, "hash-v3", time.Now(), family, issued, gen-1); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("password change from a pre-change token = %v, want ErrTokenInvalid", err)
	}
	if _, err := f.users.UpdateProfileWhileSessionLive(ctx, u.ID, strPtr("Again"), nil, family, issued, gen-1); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("profile change from a pre-change token = %v, want ErrTokenInvalid", err)
	}
	// A token minted before the password change above (its own stamp) is
	// refused by the timestamp arm even with the current generation.
	if err := f.users.SetPasswordWhileSessionLive(ctx, u.ID, "hash-v3", time.Now(), family, time.Now().Add(-time.Hour), gen); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("password change from a token predating the password change = %v, want ErrTokenInvalid", err)
	}

	// Revoked family: both refused.
	if err := f.tokens.RevokeAllForUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.users.SetPasswordWhileSessionLive(ctx, u.ID, "hash-v3", time.Now(), family, time.Now(), gen); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("password change after 'sign out everywhere' = %v, want ErrTokenInvalid", err)
	}
	if _, err := f.users.UpdateProfileWhileSessionLive(ctx, u.ID, strPtr("Again"), strPtr("again@example.com"), family, time.Now(), gen); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("profile change after 'sign out everywhere' = %v, want ErrTokenInvalid", err)
	}
	// No proof at all is refused, not treated as any session.
	if err := f.users.SetPasswordWhileSessionLive(ctx, u.ID, "hash-v3", time.Now(), uuid.Nil, time.Now(), gen); !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("password change with no family = %v, want ErrTokenInvalid", err)
	}
	unchanged, _ := f.users.GetByID(ctx, u.ID)
	if unchanged.HashedPassword != "hash-v2" || unchanged.Email != "moved@example.com" || unchanged.Name != "Renamed" || unchanged.CredentialGeneration != gen {
		t.Fatalf("a refused write changed the row: %+v", unchanged)
	}

	// The controls: the unbound statements write after the revocation —
	// SetPassword is what the password RESET (a mailbox grant, not a session)
	// still uses on purpose, and UpdateProfile is for fixtures.
	if err := f.users.SetPassword(ctx, u.ID, "hash-reset", time.Now()); err != nil {
		t.Fatalf("control: the unbound password write was expected to succeed: %v", err)
	}
	if _, err := f.users.UpdateProfile(ctx, u.ID, strPtr("Fixture"), nil); err != nil {
		t.Fatalf("control: the unbound profile write was expected to succeed: %v", err)
	}
}

func TestPasswordChangeUnderContentionWithRevocation(t *testing.T) {
	f := newTransitionsFixture(t)
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		u := f.user(t)
		family := uuid.New()
		f.token(t, u, family)
		hash := "hash-" + uuid.NewString()
		holder := f.db.Begin()
		if _, err := lockUserRow(holder, u.ID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var writeErr, revokeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			writeErr = f.users.SetPasswordWhileSessionLive(ctx, u.ID, hash, time.Now(), family, time.Now().Add(-time.Minute), u.CredentialGeneration)
		}()
		go func() { defer wg.Done(); revokeErr = f.tokens.RevokeAllForUser(ctx, u.ID) }()
		time.Sleep(50 * time.Millisecond)
		_ = holder.Rollback()
		wg.Wait()
		if revokeErr != nil {
			t.Fatal(revokeErr)
		}
		got, _ := f.users.GetByID(ctx, u.ID)
		switch {
		case writeErr == nil && got.HashedPassword != hash:
			t.Fatalf("round %d: the write reported success but the hash is %q", round, got.HashedPassword)
		case errors.Is(writeErr, app_errors.ErrTokenInvalid) && got.HashedPassword != "hash-v1":
			t.Fatalf("round %d: the write was refused yet the hash changed", round)
		case writeErr != nil && !errors.Is(writeErr, app_errors.ErrTokenInvalid):
			t.Fatalf("round %d: %v", round, writeErr)
		}
		// After the round the family is dead whichever order it took.
		if err := f.users.SetPasswordWhileSessionLive(ctx, u.ID, "late", time.Now(), family, time.Now(), got.CredentialGeneration); !errors.Is(err, app_errors.ErrTokenInvalid) {
			t.Fatalf("round %d: a write after the revocation completed = %v, want ErrTokenInvalid", round, err)
		}
	}
}
