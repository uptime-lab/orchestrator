package entitlement

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

// The production key is ECDSA P-384 on a PIV token (its firmware has no
// Ed25519); these tests stand in a software key of the same type.

func p384Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pubPEM(t *testing.T, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func TestECDSAP384SignThenVerifyRoundTrips(t *testing.T) {
	priv := p384Key(t)
	form, err := Sign(sampleDoc(), priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.HasPrefix(form, FormPrefix+".") {
		t.Fatalf("form %q does not carry the prefix", form)
	}
	// Loaded from PEM, as the embedded store loads it.
	keys, err := ParsePublicKeysPEM(pubPEM(t, &priv.PublicKey))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, err := VerifyWith(form, keys)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if *got != sampleDoc() {
		t.Errorf("round trip changed the document: %+v", *got)
	}
}

// The signature is ecdsa.SignASN1 over SHA-384 of the prefixed message, the
// scheme the agent artifacts use: a signature made that way by hand verifies.
func TestECDSASignatureIsASN1OverSHA384(t *testing.T) {
	priv := p384Key(t)
	payload, err := Encode(sampleDoc())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signMessage(priv, signingInput(payload))
	if err != nil {
		t.Fatal(err)
	}
	form := FormPrefix + "." + b64.EncodeToString(payload) + "." + b64.EncodeToString(sig)
	if _, err := VerifyWith(form, []crypto.PublicKey{&priv.PublicKey}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(sig) > maxECDSASignatureSize || sig[0] != 0x30 {
		t.Errorf("signature is %d bytes starting %#x, want DER of at most %d", len(sig), sig[0], maxECDSASignatureSize)
	}
}

func TestECDSATamperedPayloadAndSignatureAreRefused(t *testing.T) {
	priv := p384Key(t)
	form, _ := Sign(sampleDoc(), priv)
	parts := strings.Split(form, ".")
	keys := []crypto.PublicKey{&priv.PublicKey}

	raised := sampleDoc()
	raised.DeviceCap = 100000
	payload, _ := Encode(raised)
	forged := parts[0] + "." + b64.EncodeToString(payload) + "." + parts[2]
	if _, err := VerifyWith(forged, keys); !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("tampered payload: err = %v, want ErrSignatureInvalid", err)
	}

	sig, _ := b64.DecodeString(parts[2])
	sig[len(sig)-1] ^= 0x01
	broken := parts[0] + "." + parts[1] + "." + b64.EncodeToString(sig)
	if _, err := VerifyWith(broken, keys); !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("tampered signature: err = %v, want ErrSignatureInvalid", err)
	}
}

func TestECDSAFileSignedByAnotherKeyIsRefused(t *testing.T) {
	form, _ := Sign(sampleDoc(), p384Key(t))
	other := p384Key(t)
	if _, err := VerifyWith(form, []crypto.PublicKey{&other.PublicKey}); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("err = %v, want ErrSignatureInvalid", err)
	}
	// Nor does an Ed25519 key stand in for it.
	edPub, _ := testKey(t)
	if _, err := VerifyWith(form, []crypto.PublicKey{edPub}); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("err = %v, want ErrSignatureInvalid", err)
	}
}

func TestKeysOfOtherTypesAreRefusedAtLoad(t *testing.T) {
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, pub := range map[string]any{"P-256": &p256.PublicKey, "RSA": &rsaKey.PublicKey} {
		if _, err := ParsePublicKeysPEM(pubPEM(t, pub)); err == nil {
			t.Errorf("%s key accepted", name)
		}
	}
	// Signing with one is refused too: the signer must not issue what no
	// install could verify.
	if _, err := Sign(sampleDoc(), p256); err == nil {
		t.Error("a P-256 key signed a file")
	}
}

// A trust store holding one key of each type verifies files from both, and
// one bad file among them drops the whole set with that file named.
func TestMixedKeyDirectory(t *testing.T) {
	edPub, edPriv := testKey(t)
	ec := p384Key(t)
	dir := fstest.MapFS{
		"keys/a-ed25519.pem": {Data: pubPEM(t, edPub)},
		"keys/b-p384.pem":    {Data: pubPEM(t, &ec.PublicKey)},
	}
	keys, err := loadKeyDir(dir, "keys")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("loaded %d keys, want 2", len(keys))
	}
	if _, ok := keys[0].(ed25519.PublicKey); !ok {
		t.Errorf("keys[0] is %T", keys[0])
	}
	for name, signer := range map[string]crypto.Signer{"ed25519": edPriv, "p384": ec} {
		form, err := Sign(sampleDoc(), signer)
		if err != nil {
			t.Fatalf("%s sign: %v", name, err)
		}
		if _, err := VerifyWith(form, keys); err != nil {
			t.Errorf("%s file refused by the mixed set: %v", name, err)
		}
	}

	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	dir["keys/c-p256.pem"] = &fstest.MapFile{Data: pubPEM(t, &p256.PublicKey)}
	keys, err = loadKeyDir(dir, "keys")
	if err == nil || keys != nil {
		t.Fatalf("a P-256 file was tolerated: keys=%d err=%v", len(keys), err)
	}
	if !strings.Contains(err.Error(), "c-p256.pem") {
		t.Errorf("error does not name the file: %v", err)
	}
}

// A non-Ed25519 key has a fingerprint of its own; it is never mistaken for
// the development key.
func TestECDSAKeyIsNotTheDevelopmentKey(t *testing.T) {
	ec := p384Key(t)
	if fp := KeyFingerprint(&ec.PublicKey); fp == "" || fp == DevelopmentKeyFingerprint {
		t.Fatalf("fingerprint = %q", fp)
	}
	if containsDevelopmentKey([]crypto.PublicKey{&ec.PublicKey}) {
		t.Error("a P-384 key was taken for the development key")
	}
}
