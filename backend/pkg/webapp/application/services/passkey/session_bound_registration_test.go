package passkey

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/passkey"
)

// Why these tests exist. A passkey is a complete way to sign
// in, and registration used to store it through an unconditional insert that
// knew nothing about the session that asked: the step-up middleware had proved
// the session live, and a sign-out or a recovery landing between that check
// and the insert still stored the credential. FinishRegistration now carries
// the caller's SessionProof into the repository, which re-checks it under the
// user's row lock (proven against Postgres in
// credential_transitions_integration_test.go). Here the REAL ceremony runs —
// the same software authenticator the login tests use, producing a "none"
// attestation — against a repository that records what it was handed.

// sessionPasskeyRepo records the proof CreateWhileSessionLive receives and
// can play a session that died before the insert.
type sessionPasskeyRepo struct {
	memoryPasskeyRepo
	stored      *passkey.Passkey
	lastProof   *services.SessionProof
	sessionDead bool
	// plainCreates counts inserts that bypassed the session boundary.
	plainCreates int
}

func (r *sessionPasskeyRepo) Create(_ context.Context, p *passkey.Passkey) error {
	r.plainCreates++
	r.stored = p
	return nil
}

func (r *sessionPasskeyRepo) CreateWhileSessionLive(_ context.Context, p *passkey.Passkey, familyID uuid.UUID, issuedAt time.Time, generation int64) error {
	r.lastProof = &services.SessionProof{FamilyID: familyID, IssuedAt: issuedAt, CredentialGeneration: generation}
	if familyID == uuid.Nil || issuedAt.IsZero() || r.sessionDead {
		return app_errors.ErrTokenInvalid
	}
	r.stored = p
	return nil
}

// registrationService is a.service(t) with the recording repository. The
// authenticator's existing credential is deliberately NOT pre-registered, so
// the ceremony registers it fresh.
func (a *authenticator) registrationService() (*Service, *sessionPasskeyRepo) {
	repo := &sessionPasskeyRepo{}
	return &Service{
		repo:       repo,
		userRepo:   &oneUserRepo{usr: a.usr},
		ceremonies: newCeremonyStore(ceremonyTTL),
		logger:     slog.Default(),
	}, repo
}

// attest answers a registration challenge the way a platform authenticator
// does: a "none" attestation over authenticator data that carries the new
// credential and its COSE public key, user present and user verified.
func (a *authenticator) attest(t *testing.T, challenge protocol.URLEncodedBase64) *http.Request {
	t.Helper()
	clientData, err := json.Marshal(map[string]string{
		"type":      "webauthn.create",
		"challenge": challenge.String(),
		"origin":    "https://" + testHost,
	})
	if err != nil {
		t.Fatal(err)
	}
	rpIDHash := sha256.Sum256([]byte(testHost))
	flags := byte(protocol.FlagUserPresent) | byte(protocol.FlagUserVerified) | byte(protocol.FlagAttestedCredentialData)
	authData := append([]byte{}, rpIDHash[:]...)
	authData = append(authData, flags)
	authData = binary.BigEndian.AppendUint32(authData, 0) // sign count
	authData = append(authData, make([]byte, 16)...)      // AAGUID
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(a.credID)))
	authData = append(authData, a.credID...)
	authData = append(authData, a.cosePublicKey(t)...)

	attestation, err := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": authData,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"id":    b64url(a.credID),
		"rawId": b64url(a.credID),
		"type":  "public-key",
		"response": map[string]string{
			"attestationObject": b64url(attestation),
			"clientDataJSON":    b64url(clientData),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://"+testHost+"/api/v1/users/me/passkeys/register/finish", bytes.NewReader(body))
	req.Host = testHost
	req.Header.Set("Content-Type", "application/json")
	return req
}

func registrationProof() services.SessionProof {
	return services.SessionProof{FamilyID: uuid.New(), IssuedAt: time.Now().Add(-time.Minute), CredentialGeneration: 3}
}

func TestRegistrationStoresTheCredentialUnderTheCallersSession(t *testing.T) {
	a := newAuthenticator(t)
	svc, repo := a.registrationService()
	creation, ticket, err := svc.BeginRegistration(context.Background(), beginRequest(), a.usr.ID)
	if err != nil {
		t.Fatal(err)
	}
	proof := registrationProof()
	record, err := svc.FinishRegistration(context.Background(), a.attest(t, creation.Response.Challenge), a.usr.ID, ticket, "laptop", proof)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	if repo.stored == nil || !bytes.Equal(repo.stored.CredentialID, a.credID) || record.Name != "laptop" {
		t.Fatalf("stored %+v, want the registered credential", repo.stored)
	}
	if repo.plainCreates != 0 {
		t.Fatal("the credential was stored through the unguarded insert")
	}
	if got := repo.lastProof; got == nil || got.FamilyID != proof.FamilyID || !got.IssuedAt.Equal(proof.IssuedAt) || got.CredentialGeneration != proof.CredentialGeneration {
		t.Fatalf("the repository was handed %+v, want the caller's proof %+v", got, proof)
	}
}

func TestRegistrationRefusesWithoutASessionProof(t *testing.T) {
	a := newAuthenticator(t)
	svc, repo := a.registrationService()
	creation, ticket, err := svc.BeginRegistration(context.Background(), beginRequest(), a.usr.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.FinishRegistration(context.Background(), a.attest(t, creation.Response.Challenge), a.usr.ID, ticket, "", services.SessionProof{})
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("FinishRegistration without a proof = %v, want ErrTokenInvalid", err)
	}
	if repo.stored != nil || repo.lastProof != nil {
		t.Fatal("a registration with no session behind it reached the store")
	}
	// Refused BEFORE the ceremony ticket was spent: the same ticket still
	// completes once a proof is present.
	if _, err := svc.FinishRegistration(context.Background(), a.attest(t, creation.Response.Challenge), a.usr.ID, ticket, "", registrationProof()); err != nil {
		t.Fatalf("the ceremony ticket was spent by the refusal: %v", err)
	}
}

func TestRegistrationStoresNothingWhenTheSessionEnded(t *testing.T) {
	a := newAuthenticator(t)
	svc, repo := a.registrationService()
	creation, ticket, err := svc.BeginRegistration(context.Background(), beginRequest(), a.usr.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo.sessionDead = true
	_, err = svc.FinishRegistration(context.Background(), a.attest(t, creation.Response.Challenge), a.usr.ID, ticket, "", registrationProof())
	if !errors.Is(err, app_errors.ErrTokenInvalid) {
		t.Fatalf("FinishRegistration from a dead session = %v, want ErrTokenInvalid", err)
	}
	if repo.stored != nil {
		t.Fatal("a dead session stored a passkey")
	}
}

// The attestation fixture itself: a tampered challenge must still be refused
// by the library, or the tests above prove nothing about the ceremony.
func TestRegistrationFixtureIsAcceptedOnlyForItsOwnChallenge(t *testing.T) {
	a := newAuthenticator(t)
	svc, _ := a.registrationService()
	if _, ticket, err := svc.BeginRegistration(context.Background(), beginRequest(), a.usr.ID); err == nil {
		other := make([]byte, 32)
		if _, err := rand.Read(other); err != nil {
			t.Fatal(err)
		}
		_, err := svc.FinishRegistration(context.Background(), a.attest(t, other), a.usr.ID, ticket, "", registrationProof())
		if !errors.Is(err, app_errors.ErrPasskeyRegistrationFailed) {
			t.Fatalf("an attestation over a foreign challenge = %v, want ErrPasskeyRegistrationFailed", err)
		}
	} else {
		t.Fatal(err)
	}
}
