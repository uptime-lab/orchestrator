package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_password_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/user_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// Why these tests exist. The
// password change and the profile update sit behind the same step-up guard
// as passkey registration and TOTP confirmation, and they used to persist
// through statements that knew nothing about the session that asked — the
// guard proved it live, and a sign-out or a recovery landing between the
// guard and the write still changed how the identity signs in. Both now carry
// the caller's SessionProof into the repository, which re-checks it on the
// user's row lock (proven against Postgres in
// credential_transitions_integration_test.go). Here: the proof that reaches
// the repository is the caller's, an absent proof is refused before anything
// is read, a dead session writes nothing — and the partial-update arguments
// reach the repository as the caller sent them, an omitted field as nil.

// liveSession is the proof the JWT middleware derives from a live access token.
func liveSession() services.SessionProof {
	return services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now().Add(-time.Minute), CredentialGeneration: 1}
}

func passwordCmd(id uuid.UUID, password string) user_password_update.UserUpdatePasswordCommand {
	return user_password_update.UserUpdatePasswordCommand{
		ID:          id,
		AuthContext: commands.NewAuthContext(uuid.New(), id),
		Password:    password,
	}
}

func TestUpdateMeForwardsTheCallersSessionProof(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, repo := newTestService(u)
	proof := services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now().Add(-2 * time.Minute), CredentialGeneration: 4}
	if _, err := svc.UpdateMe(context.Background(), updateCmd(u, "new@example.com"), proof); err != nil {
		t.Fatal(err)
	}
	got := repo.lastProfileWrite
	if got == nil || got.proof.familyID != proof.FamilyID || !got.proof.issuedAt.Equal(proof.IssuedAt) || got.proof.generation != proof.CredentialGeneration {
		t.Fatalf("the repository was handed %+v, want the caller's proof %+v", got, proof)
	}
}

func TestUpdatePasswordForwardsTheCallersSessionProof(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, repo := newTestService(u)
	proof := services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now().Add(-2 * time.Minute), CredentialGeneration: 4}
	if _, err := svc.UpdatePassword(context.Background(), passwordCmd(u.ID, "a-long-enough-password"), proof); err != nil {
		t.Fatal(err)
	}
	got := repo.lastPasswordProof
	if got == nil || got.familyID != proof.FamilyID || !got.issuedAt.Equal(proof.IssuedAt) || got.generation != proof.CredentialGeneration {
		t.Fatalf("the repository was handed %+v, want the caller's proof %+v", got, proof)
	}
}

func TestUpdateMeAndUpdatePasswordRefuseWithoutASessionProof(t *testing.T) {
	for name, proof := range map[string]services.SessionProof{
		"no family":     {IssuedAt: time.Now()},
		"no issue time": {FamilyID: uuid.New()},
		"empty":         {},
	} {
		t.Run(name, func(t *testing.T) {
			u := verifiedUser("owner@example.com")
			u.HashedPassword = "hash-v1"
			svc, repo := newTestService(u)

			_, err := svc.UpdateMe(context.Background(), updateCmd(u, "new@example.com"), proof)
			if !errors.Is(err, app_errors.ErrTokenInvalid) {
				t.Fatalf("UpdateMe without a proof = %v, want ErrTokenInvalid", err)
			}
			_, err = svc.UpdatePassword(context.Background(), passwordCmd(u.ID, "a-long-enough-password"), proof)
			if !errors.Is(err, app_errors.ErrTokenInvalid) {
				t.Fatalf("UpdatePassword without a proof = %v, want ErrTokenInvalid", err)
			}
			stored := repo.users[u.ID]
			if stored.Email != "owner@example.com" || stored.HashedPassword != "hash-v1" || stored.EmailVerifiedAt == nil {
				t.Fatalf("a write landed with no session behind it: %+v", stored)
			}
			if repo.lastProfileWrite != nil || repo.lastPasswordProof != nil {
				t.Fatal("the missing proof reached the repository instead of being refused first")
			}
		})
	}
}

func TestUpdateMeAndUpdatePasswordWriteNothingWhenTheSessionEnded(t *testing.T) {
	u := verifiedUser("owner@example.com")
	u.HashedPassword = "hash-v1"
	svc, repo := newTestService(u)
	// The session dies between the middleware's check and the write: the
	// repository's boundary refuses.
	repo.sessionDead = true

	_, err := svc.UpdateMe(context.Background(), updateCmd(u, "new@example.com"), liveSession())
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("UpdateMe from a dead session = %v, want ErrTokenInvalid", err)
	}
	_, err = svc.UpdatePassword(context.Background(), passwordCmd(u.ID, "a-long-enough-password"), liveSession())
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("UpdatePassword from a dead session = %v, want ErrTokenInvalid", err)
	}
	stored := repo.users[u.ID]
	if stored.Email != "owner@example.com" || stored.HashedPassword != "hash-v1" || stored.EmailVerifiedAt == nil || stored.PasswordChangedAt != nil {
		t.Fatalf("a dead session changed how the identity signs in: %+v", stored)
	}

	// The control: the same requests once the session is live again.
	repo.sessionDead = false
	if _, err := svc.UpdateMe(context.Background(), updateCmd(u, "new@example.com"), liveSession()); err != nil {
		t.Fatalf("control: a live session was refused: %v", err)
	}
	if _, err := svc.UpdatePassword(context.Background(), passwordCmd(u.ID, "a-long-enough-password"), liveSession()); err != nil {
		t.Fatalf("control: a live session was refused: %v", err)
	}
	if stored := repo.users[u.ID]; stored.Email != "new@example.com" || stored.HashedPassword == "hash-v1" {
		t.Fatalf("control: the live writes did not land: %+v", stored)
	}
}

// An omitted field must reach the repository as nil — the partial write is
// the repository's COALESCE, not the service filling in its snapshot. A
// future "fill it in to be safe" edit reintroduces the snapshot write-back
// at the service and is caught here rather than only by the pointer type.
func TestUpdateMePassesOmittedFieldsAsNil(t *testing.T) {
	u := verifiedUser("owner@example.com")
	svc, repo := newTestService(u)

	name := "Renamed"
	if _, err := svc.UpdateMe(context.Background(), user_update.UserUpdateCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
		Name:        &name,
	}, liveSession()); err != nil {
		t.Fatal(err)
	}
	got := repo.lastProfileWrite
	if got == nil || got.email != nil || got.name == nil || *got.name != "Renamed" {
		t.Fatalf("name-only update reached the repository as name=%v email=%v; want the email nil", deref(got.name), deref(got.email))
	}

	email := "New@Example.com"
	if _, err := svc.UpdateMe(context.Background(), user_update.UserUpdateCommand{
		ID:          u.ID,
		AuthContext: commands.NewAuthContext(uuid.New(), u.ID),
		Email:       &email,
	}, liveSession()); err != nil {
		t.Fatal(err)
	}
	got = repo.lastProfileWrite
	if got == nil || got.name != nil || got.email == nil || *got.email != "new@example.com" {
		t.Fatalf("email-only update reached the repository as name=%v email=%v; want the name nil and the email normalized", deref(got.name), deref(got.email))
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
