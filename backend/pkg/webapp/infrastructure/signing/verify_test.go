package signing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
)

func testKey(t *testing.T, name string) (*ecdsa.PrivateKey, TrustedKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParsePublicKeyPEM(name, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	return priv, k
}

// sign mimics the hardware signer: ASN.1-DER ECDSA over SHA-384.
func sign(t *testing.T, k *ecdsa.PrivateKey, data []byte) []byte {
	t.Helper()
	d := sha512.Sum384(data)
	sig, err := ecdsa.SignASN1(rand.Reader, k, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestVerifyDetachedMirrorsTheAgent(t *testing.T) {
	p1, k1 := testKey(t, "one.pem")
	p2, k2 := testKey(t, "two.pem")
	other, _ := testKey(t, "other.pem")
	trust := []TrustedKey{k1, k2}
	data := []byte("package bytes")

	if got, err := VerifyDetached(trust, data, sign(t, p1, data)); err != nil || got.Name != "one.pem" {
		t.Fatalf("first key: got %q, %v", got.Name, err)
	}
	if got, err := VerifyDetached(trust, data, sign(t, p2, data)); err != nil || got.Name != "two.pem" {
		t.Fatalf("second key (multi-trust): got %q, %v", got.Name, err)
	}
	if _, err := VerifyDetached(trust, data, sign(t, other, data)); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("untrusted key: want ErrUntrusted, got %v", err)
	}
	tampered := append(append([]byte{}, data...), 'x')
	if _, err := VerifyDetached(trust, tampered, sign(t, p1, data)); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("tampered data: want ErrUntrusted, got %v", err)
	}
	if _, err := VerifyDetached(trust, data, nil); err == nil {
		t.Fatal("an empty signature must be refused")
	}
	if _, err := VerifyDetached(nil, data, sign(t, p1, data)); err == nil {
		t.Fatal("an empty key set must verify nothing")
	}
}

// TestEmbeddedTrustedKeysParse guards the go:embed set the import verifies
// against: at least one key, every one P-384, no duplicate.
func TestEmbeddedTrustedKeysParse(t *testing.T) {
	keys, err := TrustedKeys()
	if err != nil {
		t.Fatalf("embedded keys: %v", err)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if k.Key.Curve != elliptic.P384() {
			t.Errorf("%s is not P-384", k.Name)
		}
		if seen[k.Fingerprint] {
			t.Errorf("%s duplicates another key", k.Name)
		}
		seen[k.Fingerprint] = true
	}
}

// A key on another curve must be refused even when the signature under it is
// mathematically valid: the scheme is ECDSA P-384, as on the agent.
func TestVerifyRefusesANonP384Key(t *testing.T) {
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&p256.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if _, err := ParsePublicKeyPEM("p256.pem", pemBytes); !errors.Is(err, ErrNotP384) {
		t.Fatalf("parse a P-256 key: want ErrNotP384, got %v", err)
	}

	data := []byte("orchestrator-agent.deb contents")
	sig := sign(t, p256, data) // valid ASN.1 ECDSA over SHA-384, on P-256
	digest := sha512.Sum384(data)
	if !ecdsa.VerifyASN1(&p256.PublicKey, digest[:], sig) {
		t.Fatal("precondition: the P-256 signature must be valid under its own key")
	}
	key := TrustedKey{Name: "p256.pem", Key: &p256.PublicKey}
	if _, err := VerifyDetached([]TrustedKey{key}, data, sig); !errors.Is(err, ErrNotP384) {
		t.Fatalf("verify under a P-256 key: want ErrNotP384, got %v", err)
	}
}
