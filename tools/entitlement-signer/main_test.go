package main

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "test.key")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, pub
}

func baseOptions(keyPath string) signOptions {
	return signOptions{
		keyPath:   keyPath,
		customer:  "customer-1",
		deviceCap: 100,
		expires:   "2027-01-01",
		kind:      "subscription",
		graceDays: 30,
		note:      "test",
		now:       time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestSignThenVerifyRoundTrips(t *testing.T) {
	keyPath, pub := writeTestKey(t)
	form, err := sign(baseOptions(keyPath))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	doc, err := verify(form, []crypto.PublicKey{pub})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if doc.CustomerID != "customer-1" || doc.DeviceCap != 100 || doc.Kind != "subscription" || doc.GraceDays != 30 {
		t.Errorf("decoded %+v", doc)
	}
	// A bare expiry date covers the whole day.
	if want := time.Date(2027, 1, 1, 23, 59, 59, 0, time.UTC); !doc.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v", doc.ExpiresAt, want)
	}
	if !doc.IssuedAt.Equal(baseOptions(keyPath).now) {
		t.Errorf("issued_at = %v, want the signing instant", doc.IssuedAt)
	}
}

func TestTamperedFormIsRefused(t *testing.T) {
	keyPath, pub := writeTestKey(t)
	form, _ := sign(baseOptions(keyPath))
	parts := strings.Split(form, ".")
	// Flip one payload character; the base64 still decodes, the signature
	// no longer matches.
	payload := []byte(parts[1])
	if payload[10] == 'A' {
		payload[10] = 'B'
	} else {
		payload[10] = 'A'
	}
	forged := parts[0] + "." + string(payload) + "." + parts[2]
	if _, err := verify(forged, []crypto.PublicKey{pub}); err == nil || !strings.Contains(err.Error(), "BAD") {
		t.Fatalf("forged form verified: %v", err)
	}
}

func TestSignRefusesAnExpiryInThePast(t *testing.T) {
	keyPath, _ := writeTestKey(t)
	o := baseOptions(keyPath)
	o.expires = "2026-05-31"
	if _, err := sign(o); err == nil || !strings.Contains(err.Error(), "in the past") {
		t.Fatalf("err = %v, want a past-expiry refusal", err)
	}
}

func TestSignRefusesAnExpiryMoreThanFiveYearsOut(t *testing.T) {
	keyPath, _ := writeTestKey(t)
	o := baseOptions(keyPath)
	o.expires = "2031-07-01"
	if _, err := sign(o); err == nil || !strings.Contains(err.Error(), "five years") {
		t.Fatalf("err = %v, want a too-far refusal", err)
	}
	// Just inside five years is fine.
	o.expires = "2031-05-01"
	if _, err := sign(o); err != nil {
		t.Fatalf("five years minus a month refused: %v", err)
	}
}

// A far-future issue date would outrank every later file; the signer
// refuses one more than a day ahead, and verify refuses to call it valid.
func TestIssuedAtIsBoundedToADayAhead(t *testing.T) {
	keyPath, pub := writeTestKey(t)
	o := baseOptions(keyPath) // now 2026-06-01 12:00
	o.issuedAt = "2026-06-03"
	if _, err := sign(o); err == nil || !strings.Contains(err.Error(), "24 hours in the future") {
		t.Fatalf("err = %v, want the future-issue refusal", err)
	}
	o.issuedAt = "2026-06-02" // 12 hours ahead: an honest clock difference
	form, err := sign(o)
	if err != nil {
		t.Fatalf("12 hours ahead refused: %v", err)
	}
	pubPath := writeTestPub(t, pub)
	var out bytes.Buffer
	early := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	if err := verifyCmd([]string{"--pub", pubPath}, strings.NewReader(form), &out, early); err == nil ||
		!strings.Contains(err.Error(), "24 hours in the future") {
		t.Errorf("verify three days before the issue date: %v, want the refusal", err)
	}
}

func TestSignRefusesAnUnknownKind(t *testing.T) {
	keyPath, _ := writeTestKey(t)
	o := baseOptions(keyPath)
	o.kind = "perpetual"
	if _, err := sign(o); err == nil || !strings.Contains(err.Error(), "--kind") {
		t.Fatalf("err = %v, want a kind refusal", err)
	}
}

func TestVerifyCommandReportsTheState(t *testing.T) {
	keyPath, pub := writeTestKey(t)
	form, _ := sign(baseOptions(keyPath))
	pubDER, _ := x509.MarshalPKIXPublicKey(pub)
	pubPath := filepath.Join(t.TempDir(), "test.pem")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// Past expiry, inside the 30-day grace.
	now := time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC)
	if err := verifyCmd([]string{"--pub", pubPath}, strings.NewReader(form+"\n"), &out, now); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(out.String(), "state now: grace") {
		t.Errorf("output does not report the grace: %s", out.String())
	}
}

// A signature over terms the server refuses is not a valid file: verify runs
// the same document validation sign does, so a file produced elsewhere with,
// say, a decade of grace or a blank customer is refused, not printed as OK.
func TestVerifyRefusesASignedDocumentTheServerWouldRefuse(t *testing.T) {
	keyPath, pub := writeTestKey(t)
	priv, err := loadPrivateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signRaw := func(d document) string {
		payload, err := encode(d)
		if err != nil {
			t.Fatal(err)
		}
		return formPrefix + "." + b64.EncodeToString(payload) + "." + b64.EncodeToString(mustSign(t, priv, signingInput(payload)))
	}
	good := document{Version: version, CustomerID: "c", DeviceCap: 10, Kind: "subscription", GraceDays: 30,
		IssuedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	if _, err := verify(signRaw(good), []crypto.PublicKey{pub}); err != nil {
		t.Fatalf("a valid document was refused: %v", err)
	}
	for name, mutate := range map[string]func(*document){
		"grace 4000 days": func(d *document) { d.GraceDays = 4000 },
		"blank customer":  func(d *document) { d.CustomerID = " " },
		"unknown kind":    func(d *document) { d.Kind = "perpetual" },
		"expiry first":    func(d *document) { d.ExpiresAt = d.IssuedAt.Add(-time.Hour) },
	} {
		d := good
		mutate(&d)
		if _, err := verify(signRaw(d), []crypto.PublicKey{pub}); err == nil || !strings.Contains(err.Error(), "would refuse") {
			t.Errorf("%s: verify = %v, want the document refusal", name, err)
		}
	}
}

// keygen writes the private key 0600 and the public key readable, refuses
// to overwrite either, and produces a pair that signs and verifies.
func TestKeygenWritesAPairOnceWithTheRightModes(t *testing.T) {
	out := filepath.Join(t.TempDir(), "entitlement-signing-test")
	if err := keygenCmd([]string{"--out", out}); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	privInfo, err := os.Stat(out + ".key")
	if err != nil {
		t.Fatal(err)
	}
	if privInfo.Mode().Perm() != 0o600 {
		t.Errorf("private key mode = %o, want 600", privInfo.Mode().Perm())
	}
	pubInfo, err := os.Stat(out + ".pem")
	if err != nil {
		t.Fatal(err)
	}
	if m := pubInfo.Mode().Perm(); m&0o400 == 0 || m&0o022 != 0 {
		t.Errorf("public key mode = %o, want owner-readable and not group/world-writable (644 under the usual umask)", m)
	}

	priv, err := loadPrivateKey(out + ".key")
	if err != nil {
		t.Fatalf("the private key does not load: %v", err)
	}
	pemData, err := os.ReadFile(out + ".pem")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := parsePublicKeys(pemData)
	if err != nil || len(keys) != 1 || !sameKey(keys[0], priv.Public()) {
		t.Fatalf("the public key does not match the private one: %v", err)
	}

	before, _ := os.ReadFile(out + ".key")
	if err := keygenCmd([]string{"--out", out}); err == nil {
		t.Fatal("a second keygen over the same name succeeded")
	}
	after, _ := os.ReadFile(out + ".key")
	if !bytes.Equal(before, after) {
		t.Error("the refused second keygen changed the private key")
	}
}

// writeTestPub writes pub as a PEM file and returns its path.
func writeTestPub(t *testing.T, pub crypto.PublicKey) string {
	t.Helper()
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPath := filepath.Join(t.TempDir(), "test.pem")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	return pubPath
}

// The deploy preflight asserts by exit code, not by a line someone reads:
// the file must be active, and its cap must cover the install's admitted
// devices (0 is unlimited and covers any count).
func TestVerifyPreflightFlags(t *testing.T) {
	keyPath, pub := writeTestKey(t)
	pubPath := writeTestPub(t, pub)
	form, _ := sign(baseOptions(keyPath)) // cap 100, expires 2027-01-01
	active := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	inGrace := time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC)

	run := func(now time.Time, form string, flags ...string) error {
		var out bytes.Buffer
		return verifyCmd(append([]string{"--pub", pubPath}, flags...), strings.NewReader(form), &out, now)
	}
	if err := run(active, form, "--require-active", "--min-cap", "77"); err != nil {
		t.Errorf("an active file covering 77 devices failed the preflight: %v", err)
	}
	if err := run(inGrace, form, "--require-active"); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Errorf("a file in grace passed --require-active: %v", err)
	}
	if err := run(active, form, "--min-cap", "101"); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Errorf("a 100-device file passed --min-cap 101: %v", err)
	}

	o := baseOptions(keyPath)
	o.deviceCap = 0
	unlimited, _ := sign(o)
	if err := run(active, unlimited, "--min-cap", "100000"); err != nil {
		t.Errorf("an unlimited file failed --min-cap: %v", err)
	}
}

// The free cap is a floor the server never lets a file lower, so the signer
// never issues a positive cap below it: the refusal is typed, names the
// floor, and says that 0 is how to ask for unlimited.
func TestSignRefusesACapBelowTheFreeFloor(t *testing.T) {
	keyPath, _ := writeTestKey(t)
	for _, c := range []int{1, 10, 24} {
		o := baseOptions(keyPath)
		o.deviceCap = c
		form, err := sign(o)
		var below *capBelowFreeFloorError
		if !errors.As(err, &below) || below.Cap != c || form != "" {
			t.Fatalf("cap %d: sign = %q, %v; want the typed floor refusal", c, form, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, fmt.Sprint(freeDeviceCap)) || !strings.Contains(msg, "0 means unlimited") {
			t.Errorf("cap %d: refusal %q does not name the floor and the unlimited value", c, msg)
		}
	}
	for _, c := range []int{0, freeDeviceCap, freeDeviceCap + 1} {
		o := baseOptions(keyPath)
		o.deviceCap = c
		if _, err := sign(o); err != nil {
			t.Errorf("cap %d: sign refused: %v", c, err)
		}
	}
}

// A sub-floor file signed by anything other than this tool still verifies —
// the server accepts it and enforces the free cap — so verify reports it as a
// WARNING, not a failure, and the preflight measures the cap the server will
// actually enforce.
func TestVerifyWarnsOnACapBelowTheFreeFloor(t *testing.T) {
	keyPath, pub := writeTestKey(t)
	pubPath := writeTestPub(t, pub)
	priv, err := loadPrivateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	signRaw := func(c int) string {
		payload, err := encode(document{Version: version, CustomerID: "c", DeviceCap: c, Kind: "subscription", GraceDays: 30,
			IssuedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		return formPrefix + "." + b64.EncodeToString(payload) + "." + b64.EncodeToString(mustSign(t, priv, signingInput(payload)))
	}
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	run := func(form string, flags ...string) (string, error) {
		var out bytes.Buffer
		err := verifyCmd(append([]string{"--pub", pubPath}, flags...), strings.NewReader(form), &out, now)
		return out.String(), err
	}

	out, err := run(signRaw(10))
	if err != nil {
		t.Fatalf("a sub-floor file must still verify: %v", err)
	}
	if !strings.Contains(out, "WARNING: device_cap 10 is below the free cap of 25") {
		t.Errorf("verify did not warn about the sub-floor cap:\n%s", out)
	}
	if _, err := run(signRaw(10), "--require-active", "--min-cap", "25"); err != nil {
		t.Errorf("the server enforces 25 for a cap of 10, so --min-cap 25 must pass: %v", err)
	}
	if _, err := run(signRaw(10), "--min-cap", "26"); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Errorf("a sub-floor file passed --min-cap 26: %v", err)
	}
	for _, c := range []int{0, 25, 26} {
		out, err := run(signRaw(c))
		if err != nil || strings.Contains(out, "WARNING") {
			t.Errorf("cap %d: err %v, output:\n%s", c, err, out)
		}
	}
}
