package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mustSign signs msg the way sign does, for tests that build a form by hand.
func mustSign(t *testing.T, priv crypto.Signer, msg []byte) []byte {
	t.Helper()
	sig, err := signMessage(priv, msg)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func newP384(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// writeECKey writes a software P-384 key as SEC1 ("EC PRIVATE KEY", what
// openssl writes) or PKCS#8.
func writeECKey(t *testing.T, k *ecdsa.PrivateKey, pkcs8 bool) string {
	t.Helper()
	var block *pem.Block
	if pkcs8 {
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		block = &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	} else {
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		block = &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
	}
	p := filepath.Join(t.TempDir(), "p384.key")
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func p384SignOptions() signOptions {
	return signOptions{customer: "acme", deviceCap: 100, expires: "2027-01-01", kind: "subscription",
		graceDays: 30, now: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
}

// A software P-384 key goes through sign and verify exactly as the token's
// key will: SHA-384, ASN.1 DER, the same prefixed message.
func TestP384SignThenVerify(t *testing.T) {
	for _, pkcs8 := range []bool{false, true} {
		k := newP384(t)
		o := p384SignOptions()
		o.keyPath = writeECKey(t, k, pkcs8)
		form, err := sign(o)
		if err != nil {
			t.Fatalf("pkcs8=%v sign: %v", pkcs8, err)
		}
		pubPath := writeTestPub(t, &k.PublicKey)
		var out bytes.Buffer
		if err := verifyCmd([]string{"--pub", pubPath, "--require-active", "--min-cap", "50"}, strings.NewReader(form), &out, o.now); err != nil {
			t.Fatalf("pkcs8=%v verify: %v\n%s", pkcs8, err, out.String())
		}
		if !strings.Contains(out.String(), "OK: signature valid") {
			t.Errorf("verify output: %s", out.String())
		}
		parts := strings.Split(form, ".")
		sig, _ := b64.DecodeString(parts[2])
		payload, _ := b64.DecodeString(parts[1])
		digest := sha512.Sum384(append([]byte(signingPrefix), payload...))
		if !ecdsa.VerifyASN1(&k.PublicKey, digest[:], sig) {
			t.Error("the signature is not ASN.1 ECDSA over SHA-384 of the prefixed payload")
		}
	}
}

func TestP384TamperedAndWrongKeyAreRefused(t *testing.T) {
	k := newP384(t)
	o := p384SignOptions()
	o.keyPath = writeECKey(t, k, false)
	form, err := sign(o)
	if err != nil {
		t.Fatal(err)
	}
	keys := []crypto.PublicKey{&k.PublicKey}
	parts := strings.Split(form, ".")

	payload, _ := b64.DecodeString(parts[1])
	raised := bytes.Replace(payload, []byte(`"device_cap":100`), []byte(`"device_cap":900`), 1)
	if _, err := verify(parts[0]+"."+b64.EncodeToString(raised)+"."+parts[2], keys); err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Errorf("tampered payload: err = %v", err)
	}
	sig, _ := b64.DecodeString(parts[2])
	sig[len(sig)-1] ^= 1
	if _, err := verify(parts[0]+"."+parts[1]+"."+b64.EncodeToString(sig), keys); err == nil {
		t.Error("tampered signature verified")
	}
	other := newP384(t)
	if _, err := verify(form, []crypto.PublicKey{&other.PublicKey}); err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Errorf("wrong key: err = %v", err)
	}
}

// A P-256 or RSA key is refused wherever it appears: as a --pub file (which
// the server would refuse to load, trusting nothing), as a signing key, and
// as a token's key.
func TestKeysTheServerCannotLoadAreRefused(t *testing.T) {
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	for name, pub := range map[string]crypto.PublicKey{"P-256": &p256.PublicKey, "RSA": &rsaKey.PublicKey} {
		p := writeTestPub(t, pub)
		if _, err := loadKeyFiles([]string{p}); err == nil || !strings.Contains(err.Error(), "test.pem") {
			t.Errorf("%s --pub: err = %v, want a refusal naming the file", name, err)
		}
		if err := checkTokenPublicKey(0x82, pub); err == nil {
			t.Errorf("%s token key accepted", name)
		}
	}
	der, _ := x509.MarshalECPrivateKey(p256)
	kp := filepath.Join(t.TempDir(), "p256.key")
	_ = os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
	o := p384SignOptions()
	o.keyPath = kp
	if _, err := sign(o); err == nil || !strings.Contains(err.Error(), "P-384") {
		t.Errorf("P-256 signing key: err = %v", err)
	}
}

// A mixed trust set — one Ed25519 file, one P-384 file — verifies files from
// both, as the server's will during a rotation from the development key.
func TestMixedTrustSet(t *testing.T) {
	edPath, edPub := writeTestKey(t)
	ec := newP384(t)
	pubs := []string{"--pub", writeTestPub(t, edPub), "--pub", writeTestPub(t, &ec.PublicKey)}
	for name, keyPath := range map[string]string{"ed25519": edPath, "p384": writeECKey(t, ec, true)} {
		o := p384SignOptions()
		o.keyPath = keyPath
		form, err := sign(o)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := verifyCmd(pubs, strings.NewReader(form), io.Discard, o.now); err != nil {
			t.Errorf("%s file refused by the mixed set: %v", name, err)
		}
	}
}

// fakeToken stands in for a YubiKey slot: a software key behind tokenKey.
type fakeToken struct {
	crypto.Signer
	signs  int
	closed bool
}

func (f *fakeToken) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	f.signs++
	return f.Signer.Sign(r, digest, opts)
}
func (f *fakeToken) Close() error { f.closed = true; return nil }

func withFakeToken(t *testing.T, key crypto.Signer, check func(pivConfig) error) *fakeToken {
	t.Helper()
	tok := &fakeToken{Signer: key}
	prev := openTokenKey
	openTokenKey = func(cfg pivConfig) (tokenKey, error) {
		if check != nil {
			if err := check(cfg); err != nil {
				return nil, err
			}
		}
		return tok, nil
	}
	t.Cleanup(func() { openTokenKey = prev })
	return tok
}

func TestPIVSignThroughTheTokenInterface(t *testing.T) {
	k := newP384(t)
	var seen pivConfig
	tok := withFakeToken(t, k, func(c pivConfig) error { seen = c; return nil })
	o := p384SignOptions()
	o.pivSlot = "82"
	o.piv = pivConfig{serial: 42, pinEnv: "ENTL_PIN", touch: true}
	form, err := sign(o)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if seen.slot != 0x82 || seen.serial != 42 || seen.pinEnv != "ENTL_PIN" || !seen.touch {
		t.Errorf("token opened with %+v", seen)
	}
	if tok.signs != 1 || !tok.closed {
		t.Errorf("signs=%d closed=%v, want one signature and the card released", tok.signs, tok.closed)
	}
	if _, err := verify(form, []crypto.PublicKey{&k.PublicKey}); err != nil {
		t.Fatalf("token-signed file does not verify: %v", err)
	}
}

// Every refusal that needs no token comes before the token is opened, so a
// typo never costs a PIN entry or a touch.
func TestPIVRefusalsComeBeforeTheToken(t *testing.T) {
	opened := false
	withFakeToken(t, newP384(t), func(pivConfig) error { opened = true; return nil })
	cases := map[string]func(*signOptions){
		"past expiry":      func(o *signOptions) { o.expires = "2020-01-01" },
		"below free floor": func(o *signOptions) { o.deviceCap = 3 },
		"slot 9c":          func(o *signOptions) { o.pivSlot = "9c" },
		"standard slot":    func(o *signOptions) { o.pivSlot = "9a" },
		"not hex":          func(o *signOptions) { o.pivSlot = "zz" },
		"both key sources": func(o *signOptions) { o.keyPath = "x.key" },
	}
	for name, mutate := range cases {
		o := p384SignOptions()
		o.pivSlot = "82"
		mutate(&o)
		if _, err := sign(o); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
	if opened {
		t.Error("the token was opened for a request refused on its face")
	}
}

func TestPIVTokenWithAWrongKeyTypeIsRefused(t *testing.T) {
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tok := withFakeToken(t, p256, nil)
	o := p384SignOptions()
	o.pivSlot = "82"
	if _, err := sign(o); err == nil {
		t.Fatal("a P-256 token key signed a file")
	}
	if tok.signs != 0 {
		t.Error("the token was asked to sign with a key no install trusts")
	}
	dir := t.TempDir()
	if err := pubkeyCmd([]string{"--piv-slot", "82", "--out", filepath.Join(dir, "k.pem")}); err == nil {
		t.Error("pubkey exported a P-256 key")
	}
}

func TestPubkeyExportsWhatTheBackendEmbeds(t *testing.T) {
	k := newP384(t)
	tok := withFakeToken(t, k, nil)
	out := filepath.Join(t.TempDir(), "entitlement-signing-1.pem")
	if err := pubkeyCmd([]string{"--piv-slot", "82", "--out", out}); err != nil {
		t.Fatal(err)
	}
	if !tok.closed || tok.signs != 0 {
		t.Errorf("closed=%v signs=%d; export needs no signature", tok.closed, tok.signs)
	}
	data, _ := os.ReadFile(out)
	keys, err := parsePublicKeys(data)
	if err != nil || len(keys) != 1 || !sameKey(keys[0], &k.PublicKey) {
		t.Fatalf("exported key: %v", err)
	}
	if !strings.HasPrefix(string(data), "-----BEGIN PUBLIC KEY-----") {
		t.Errorf("not a PKIX PUBLIC KEY PEM: %s", data)
	}
	// Never over an existing file: the embedded key replaced by accident
	// changes what the next build trusts.
	if err := pubkeyCmd([]string{"--piv-slot", "82", "--out", out}); err == nil {
		t.Error("pubkey overwrote an existing file")
	}
}

func TestPINFromEnv(t *testing.T) {
	t.Setenv("ENTL_TEST_PIN", "")
	if _, err := pinFromEnv("ENTL_TEST_PIN")(); err == nil {
		t.Error("an empty PIN variable was accepted")
	}
	t.Setenv("ENTL_TEST_PIN", "123456")
	if pin, err := pinFromEnv("ENTL_TEST_PIN")(); err != nil || pin != "123456" {
		t.Errorf("pin = %q, err = %v", pin, err)
	}
}

func TestParsePIVSlot(t *testing.T) {
	for in, want := range map[string]uint32{"82": 0x82, "0x95": 0x95, "8A": 0x8a} {
		if got, err := parsePIVSlot(in); err != nil || got != want {
			t.Errorf("%q = %x, %v", in, got, err)
		}
	}
	for _, in := range []string{"9c", "9a", "81", "96", "", "x"} {
		if _, err := parsePIVSlot(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// The default build refuses --piv-slot by saying how to get one that can.
func TestDefaultBuildNamesTheTag(t *testing.T) {
	if !strings.Contains(errNoPIVSupport.Error(), "-tags piv") {
		t.Error(errNoPIVSupport)
	}
}
