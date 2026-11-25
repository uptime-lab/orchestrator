package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// Why these tests exist. The insert
// that starts a session was a bare Create. A password verified BEFORE an
// administrative reset could therefore store a session AFTER it — the reset
// did not move the generation and nothing re-read it — and an account switch
// that had just proven its family live could store an unrevoked sibling after
// a logout ended that family. Every login path now records its refresh
// credential through Issue, which re-reads the generation and the family
// under the user's row lock. The SQL half is proven in
// credential_transitions_integration_test.go; here the fake applies the same
// checks so the SERVICE side — which path calls what, and that a refusal
// leaves nothing behind — is what is exercised.

func TestLoginStoresNoSessionWhenTheCredentialMovedAfterVerification(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, false)
	store := f.svc.refreshTokens.(*fakeRefreshStore)
	// The password check read generation 1; the administrative reset that
	// lands before the insert moves the row to 2.
	store.generations[f.usr.ID] = f.usr.CredentialGeneration + 1

	_, err := f.svc.Login(f.usr.Email, f.password, "")
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a login that spanned a credential reset = %v, want ErrTokenInvalid", err)
	}
	if len(store.byHash) != 0 {
		t.Fatalf("a session was stored for a credential that had been replaced: %d records", len(store.byHash))
	}
}

func TestLoginIssuesWhenTheGenerationIsUnchanged(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, false)
	store := f.svc.refreshTokens.(*fakeRefreshStore)
	store.generations[f.usr.ID] = f.usr.CredentialGeneration
	result, err := f.svc.Login(f.usr.Email, f.password, "")
	if err != nil || result.Tokens == nil {
		t.Fatalf("control: an unchanged generation refused the login: %v, %+v", err, result)
	}
}

func TestSwitchAccountStoresNothingIntoAFamilyRevokedBeforeTheInsert(t *testing.T) {
	f := newSwitchFixture(t)
	family := f.proof.FamilyID
	// The liveness check passes; the logout lands right before the insert —
	// the interleaving the row lock serialises in SQL.
	f.store.beforeIssue = func() {
		if err := f.store.RevokeFamily(context.Background(), family); err != nil {
			t.Fatal(err)
		}
	}
	before := len(f.store.byHash)

	_, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), f.proof)
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a switch whose family was revoked before the insert = %v, want ErrTokenInvalid", err)
	}
	f.assertNothingMinted(t, before)
	for _, rec := range f.store.byHash {
		if rec.FamilyID == family && rec.RevokedAt == nil {
			t.Fatal("an unrevoked sibling was stored into a revoked family")
		}
	}
}

// The row's generation moves BETWEEN the switch's liveness check (which
// passed against the snapshot) and the insert: Issue refuses under the lock
// and nothing is stored (the switch path never exercised the generation arm
// while the fixture sat at generation 0).
func TestSwitchAccountStoresNothingWhenTheGenerationMovedBeforeTheInsert(t *testing.T) {
	f := newSwitchFixture(t)
	f.store.beforeIssue = func() { f.store.generations[f.usr.ID] = f.usr.CredentialGeneration + 1 }
	before := len(f.store.byHash)

	_, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), f.proof)
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a switch whose generation moved before the insert = %v, want ErrTokenInvalid", err)
	}
	f.assertNothingMinted(t, before)
}

// A reset that moved ONLY password_changed_at — what an administrative reset
// that predates the generation advance writes — is refused by Issue too: the
// snapshot's timestamp no longer matches the row's, so the backend closes the
// window alone, without relying on every writer advancing the generation.
func TestLoginStoresNoSessionWhenOnlyThePasswordTimestampMovedAfterVerification(t *testing.T) {
	f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, false)
	store := f.svc.refreshTokens.(*fakeRefreshStore)
	store.generations[f.usr.ID] = f.usr.CredentialGeneration // unchanged
	reset := time.Now()
	store.passwordChanged[f.usr.ID] = &reset // the row; the snapshot says nil

	_, err := f.svc.Login(f.usr.Email, f.password, "")
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("a login that spanned a timestamp-only reset = %v, want ErrTokenInvalid", err)
	}
	if len(store.byHash) != 0 {
		t.Fatalf("a session was stored for a password that had been replaced: %d records", len(store.byHash))
	}
	// The control: a snapshot that already carries the row's timestamp issues.
	f.usr.PasswordChangedAt = &reset
	if result, err := f.svc.Login(f.usr.Email, f.password, ""); err != nil || result.Tokens == nil {
		t.Fatalf("control: a matching timestamp refused the login: %v, %+v", err, result)
	}
}

// No login path may record a refresh credential through the unguarded
// insert: a new path that did would silently reopen both races.
func TestEveryLoginPathIssuesThroughTheGuardedInsert(t *testing.T) {
	t.Run("password login", func(t *testing.T) {
		f := newMandatoryFixture(t, passkeyCount{n: 0}, noTwoFactor{}, false)
		f.login(t)
		if n := f.svc.refreshTokens.(*fakeRefreshStore).plainCreates; n != 0 {
			t.Fatalf("%d refresh records were stored outside Issue", n)
		}
	})
	t.Run("two-factor completion", func(t *testing.T) {
		f := newMandatoryFixture(t, passkeyCount{n: 0}, totpEnrolled{}, false)
		challenge := f.login(t)
		if _, err := f.svc.CompleteTwoFactor(context.Background(), challenge.TwoFactorToken, "000000"); err != nil {
			t.Fatal(err)
		}
		if n := f.svc.refreshTokens.(*fakeRefreshStore).plainCreates; n != 0 {
			t.Fatalf("%d refresh records were stored outside Issue", n)
		}
	})
	t.Run("account selection", func(t *testing.T) {
		f, challenge := newSelectFixture(t)
		if _, _, err := f.svc.SelectAccount(context.Background(), challenge.PreToken, f.accounts[0].ID); err != nil {
			t.Fatal(err)
		}
		if n := f.svc.refreshTokens.(*fakeRefreshStore).plainCreates; n != 0 {
			t.Fatalf("%d refresh records were stored outside Issue", n)
		}
	})
	t.Run("passkey login", func(t *testing.T) {
		f := newMandatoryFixture(t, passkeyCount{n: 1}, noTwoFactor{}, false)
		if _, err := f.svc.CompletePasskeyLogin(f.usr.ID); err != nil {
			t.Fatal(err)
		}
		if n := f.svc.refreshTokens.(*fakeRefreshStore).plainCreates; n != 0 {
			t.Fatalf("%d refresh records were stored outside Issue", n)
		}
	})
	t.Run("account switch", func(t *testing.T) {
		f := newSwitchFixture(t)
		f.store.plainCreates = 0 // the fixture's own login
		if _, err := f.svc.SwitchAccount(context.Background(), f.usr.ID, uuid.New(), f.proof); err != nil {
			t.Fatal(err)
		}
		if n := f.store.plainCreates; n != 0 {
			t.Fatalf("%d refresh records were stored outside Issue", n)
		}
	})
}
