package two_factor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
)

// Why these tests exist. Confirming an enrolment enables a
// durable factor, and it used to be persisted through a statement that knew
// nothing about the session that asked: the step-up middleware had proved the
// session live, and a sign-out or a recovery landing between that check and
// the write still enabled the factor. The service now carries the caller's
// SessionProof into the repository, which re-checks it under the user's row
// lock (proven against Postgres in credential_transitions_integration_test.go).
// Here: the proof that reaches the repository is the caller's, an absent proof
// is refused before anything is read, and a refused write leaves the factor
// pending and hands out no recovery codes.

// liveProof is the proof the JWT middleware derives from a live access token.
func liveProof() services.SessionProof {
	return services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now().Add(-time.Minute), CredentialGeneration: 1}
}

func TestConfirmForwardsTheCallersSessionProof(t *testing.T) {
	svc, repo, userID := newFixture()
	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	proof := services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now().Add(-2 * time.Minute), CredentialGeneration: 5}
	if _, err := svc.Confirm(context.Background(), userID, currentCode(t, enrollment.Secret), proof); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	got := repo.lastProof
	if got == nil || got.familyID != proof.FamilyID || !got.issuedAt.Equal(proof.IssuedAt) || got.generation != proof.CredentialGeneration {
		t.Fatalf("the repository was handed %+v, want the caller's proof %+v", got, proof)
	}
}

func TestConfirmRefusesWithoutASessionProof(t *testing.T) {
	for name, proof := range map[string]services.SessionProof{
		"no family":     {IssuedAt: time.Now()},
		"no issue time": {FamilyID: uuid.New()},
		"empty":         {},
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo, userID := newFixture()
			enrollment, err := svc.Enroll(context.Background(), userID)
			if err != nil {
				t.Fatal(err)
			}
			codes, err := svc.Confirm(context.Background(), userID, currentCode(t, enrollment.Secret), proof)
			if !errors.Is(err, app_errors.ErrTokenInvalid) {
				t.Fatalf("Confirm without a proof = %v, want ErrTokenInvalid", err)
			}
			if len(codes) != 0 || repo.factor.Enabled() {
				t.Fatal("a factor was enabled with no session behind the request")
			}
			if repo.lastProof != nil {
				t.Fatal("the missing proof reached the repository instead of being refused first")
			}
		})
	}
}

func TestConfirmLeavesTheFactorPendingWhenTheSessionEnded(t *testing.T) {
	svc, repo, userID := newFixture()
	enrollment, err := svc.Enroll(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	// The session dies between the middleware's check and the write: the
	// repository's boundary refuses.
	repo.sessionDead = true
	codes, err := svc.Confirm(context.Background(), userID, currentCode(t, enrollment.Secret), liveProof())
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("Confirm from a dead session = %v, want ErrTokenInvalid", err)
	}
	if len(codes) != 0 || repo.factor.Enabled() {
		t.Fatal("a dead session enabled the factor")
	}
	// The control: the same enrolment, once the session is live again.
	repo.sessionDead = false
	if _, err := svc.Confirm(context.Background(), userID, currentCode(t, enrollment.Secret), liveProof()); err != nil {
		t.Fatalf("control: a live session was refused: %v", err)
	}
	if !repo.factor.Enabled() {
		t.Fatal("control: the factor was not enabled")
	}
}
