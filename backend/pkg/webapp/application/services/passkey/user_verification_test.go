package passkey

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/passkey"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// Why these tests exist. A passkey login is treated as two
// factors — it skips TOTP and it unlocks the step-up used for credential
// changes — on the assumption that the authenticator verified the user with a
// PIN or biometric before signing. The library enforces that flag only when
// the ceremony was begun with user verification REQUIRED; with its default
// ("preferred") an assertion carrying UP=1, UV=0 — what a roaming key with no
// PIN produces — verified fine. These tests drive the REAL ceremony with a
// correctly signed assertion and flip only the UV bit. The reauth step-up
// calls the very same FinishLogin, so what is proven here holds for it too.

const testHost = "example.com"

// memoryPasskeyRepo holds one credential.
type memoryPasskeyRepo struct {
	repository.PasskeyRepository
	cred *passkey.Passkey
}

func (r *memoryPasskeyRepo) ListByUserID(_ context.Context, userID uuid.UUID) ([]passkey.Passkey, error) {
	if r.cred != nil && r.cred.UserID == userID {
		return []passkey.Passkey{*r.cred}, nil
	}
	return nil, nil
}

func (r *memoryPasskeyRepo) GetByCredentialID(_ context.Context, id []byte) (*passkey.Passkey, error) {
	if r.cred != nil && bytes.Equal(r.cred.CredentialID, id) {
		c := *r.cred
		return &c, nil
	}
	return nil, app_errors.ErrPasskeyNotFound
}

func (r *memoryPasskeyRepo) Update(_ context.Context, p *passkey.Passkey) error {
	r.cred = p
	return nil
}

type oneUserRepo struct {
	repository.UserRepository
	usr *user.User
}

func (r *oneUserRepo) GetByID(_ context.Context, id uuid.UUID) (*user.User, error) {
	if r.usr != nil && r.usr.ID == id {
		return r.usr, nil
	}
	return nil, app_errors.ErrUserNotFound
}

// authenticator is a software authenticator: one P-256 key registered as one
// credential, able to sign an assertion with whatever flags the test asks for.
type authenticator struct {
	key    *ecdsa.PrivateKey
	credID []byte
	usr    *user.User
}

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credID := make([]byte, 16)
	if _, err := rand.Read(credID); err != nil {
		t.Fatal(err)
	}
	return &authenticator{
		key:    key,
		credID: credID,
		usr:    &user.User{ID: uuid.New(), Email: "passkey@example.com", Name: "Passkey Holder"},
	}
}

// cosePublicKey encodes the key the way an authenticator reports it at
// registration — the form the service stores and the library parses.
func (a *authenticator) cosePublicKey(t *testing.T) []byte {
	t.Helper()
	pub := a.key.PublicKey
	encoded, err := cbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType:   int64(webauthncose.EllipticKey),
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: pub.X.FillBytes(make([]byte, 32)),
		YCoord: pub.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func (a *authenticator) service(t *testing.T) *Service {
	t.Helper()
	cred := passkey.New(a.usr.ID, a.credID, a.cosePublicKey(t), nil, 0, false, false, "test key")
	return &Service{
		repo:       &memoryPasskeyRepo{cred: cred},
		userRepo:   &oneUserRepo{usr: a.usr},
		ceremonies: newCeremonyStore(ceremonyTTL),
		logger:     slog.Default(),
	}
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// assert signs a login challenge. userVerified decides the UV bit; user
// presence is always set, as every real authenticator does.
func (a *authenticator) assert(t *testing.T, challenge protocol.URLEncodedBase64, userVerified bool) *http.Request {
	t.Helper()
	clientData, err := json.Marshal(map[string]string{
		"type":      "webauthn.get",
		"challenge": challenge.String(),
		"origin":    "https://" + testHost,
	})
	if err != nil {
		t.Fatal(err)
	}

	rpIDHash := sha256.Sum256([]byte(testHost))
	flags := byte(protocol.FlagUserPresent)
	if userVerified {
		flags |= byte(protocol.FlagUserVerified)
	}
	authData := append([]byte{}, rpIDHash[:]...)
	authData = append(authData, flags)
	authData = binary.BigEndian.AppendUint32(authData, 1) // sign count

	clientHash := sha256.Sum256(clientData)
	signed := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}

	userID := a.usr.ID
	body, err := json.Marshal(map[string]any{
		"id":    b64url(a.credID),
		"rawId": b64url(a.credID),
		"type":  "public-key",
		"response": map[string]string{
			"authenticatorData": b64url(authData),
			"clientDataJSON":    b64url(clientData),
			"signature":         b64url(sig),
			"userHandle":        b64url(userID[:]),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://"+testHost+"/auth/passkey/finish", bytes.NewReader(body))
	req.Host = testHost
	req.Header.Set("Content-Type", "application/json")
	return req
}

func beginRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "https://"+testHost+"/auth/passkey/begin", nil)
	req.Host = testHost
	return req
}

func TestLoginCeremonyRequiresUserVerification(t *testing.T) {
	svc := newAuthenticator(t).service(t)
	assertion, _, err := svc.BeginLogin(beginRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got := assertion.Response.UserVerification; got != protocol.VerificationRequired {
		t.Fatalf("login options advertise userVerification=%q, want %q", got, protocol.VerificationRequired)
	}
}

func TestRegistrationCeremonyRequiresUserVerification(t *testing.T) {
	a := newAuthenticator(t)
	svc := a.service(t)
	creation, _, err := svc.BeginRegistration(context.Background(), beginRequest(), a.usr.ID)
	if err != nil {
		t.Fatal(err)
	}
	sel := creation.Response.AuthenticatorSelection
	if sel.UserVerification != protocol.VerificationRequired {
		t.Fatalf("registration options advertise userVerification=%q, want %q", sel.UserVerification, protocol.VerificationRequired)
	}
	if sel.ResidentKey != protocol.ResidentKeyRequirementRequired {
		t.Fatalf("registration options advertise residentKey=%q, want required", sel.ResidentKey)
	}
}

func TestAnAssertionWithoutUserVerificationIsRejected(t *testing.T) {
	a := newAuthenticator(t)
	svc := a.service(t)
	assertion, ticket, err := svc.BeginLogin(beginRequest())
	if err != nil {
		t.Fatal(err)
	}
	// Correctly signed, user present, user NOT verified: what a roaming key
	// with no PIN produces. Must not open a session.
	_, err = svc.FinishLogin(context.Background(), a.assert(t, assertion.Response.Challenge, false), ticket)
	if !errors.Is(err, app_errors.ErrPasskeyLoginFailed) {
		t.Fatalf("UP-only assertion: err = %v, want ErrPasskeyLoginFailed", err)
	}
}

func TestAnAssertionWithUserVerificationIsAccepted(t *testing.T) {
	// The control: the same software authenticator, the same signature path,
	// with UV set — so the rejection above is the flag and not a broken fixture.
	a := newAuthenticator(t)
	svc := a.service(t)
	assertion, ticket, err := svc.BeginLogin(beginRequest())
	if err != nil {
		t.Fatal(err)
	}
	usr, err := svc.FinishLogin(context.Background(), a.assert(t, assertion.Response.Challenge, true), ticket)
	if err != nil {
		t.Fatalf("UV assertion refused: %v", err)
	}
	if usr.ID != a.usr.ID {
		t.Fatalf("asserted user %s, want %s", usr.ID, a.usr.ID)
	}
}
