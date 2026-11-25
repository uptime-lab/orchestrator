package entitlement

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sampleDoc() Document {
	return Document{
		Version:    Version,
		CustomerID: "customer-1",
		DeviceCap:  100,
		IssuedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ExpiresAt:  time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Kind:       KindEvaluation,
		GraceDays:  30,
		Note:       "ninety-day evaluation",
	}
}

func TestSignThenVerifyRoundTrips(t *testing.T) {
	pub, priv := testKey(t)
	form, err := Sign(sampleDoc(), priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.HasPrefix(form, FormPrefix+".") {
		t.Fatalf("form %q does not carry the prefix", form)
	}
	got, err := VerifyWith(form, []crypto.PublicKey{pub})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	want := sampleDoc()
	if *got != want {
		t.Errorf("round trip changed the document:\n got %+v\nwant %+v", *got, want)
	}
}

// A paste copied out of an e-mail arrives wrapped; the parser must not care.
func TestVerifyIgnoresWhitespace(t *testing.T) {
	pub, priv := testKey(t)
	form, _ := Sign(sampleDoc(), priv)
	wrapped := "  " + form[:20] + "\n" + form[20:50] + "\r\n\t" + form[50:] + "\n"
	if _, err := VerifyWith(wrapped, []crypto.PublicKey{pub}); err != nil {
		t.Fatalf("wrapped form refused: %v", err)
	}
}

func TestTamperedPayloadIsRefusedAsBadSignature(t *testing.T) {
	pub, priv := testKey(t)
	form, _ := Sign(sampleDoc(), priv)
	parts := strings.Split(form, ".")
	// Raise the cap inside the signed payload: the bytes still decode, the
	// signature no longer covers them.
	raised := Document(sampleDoc())
	raised.DeviceCap = 100000
	payload, _ := Encode(raised)
	forged := parts[0] + "." + b64.EncodeToString(payload) + "." + parts[2]
	_, err := VerifyWith(forged, []crypto.PublicKey{pub})
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("err = %v, want ErrSignatureInvalid", err)
	}
}

func TestTamperedSignatureIsRefused(t *testing.T) {
	pub, priv := testKey(t)
	form, _ := Sign(sampleDoc(), priv)
	parts := strings.Split(form, ".")
	sig, _ := b64.DecodeString(parts[2])
	sig[0] ^= 0x01
	broken := parts[0] + "." + parts[1] + "." + b64.EncodeToString(sig)
	if _, err := VerifyWith(broken, []crypto.PublicKey{pub}); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("err = %v, want ErrSignatureInvalid", err)
	}
}

func TestAFileSignedByAnotherKeyIsRefused(t *testing.T) {
	_, priv := testKey(t)
	otherPub, _ := testKey(t)
	form, _ := Sign(sampleDoc(), priv)
	if _, err := VerifyWith(form, []crypto.PublicKey{otherPub}); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("err = %v, want ErrSignatureInvalid", err)
	}
}

// Any one trusted key suffices, which is what a rotation relies on: the
// successor ships beside its predecessor and files signed by either verify.
func TestAnyTrustedKeyVerifies(t *testing.T) {
	pub1, priv1 := testKey(t)
	pub2, _ := testKey(t)
	form, _ := Sign(sampleDoc(), priv1)
	if _, err := VerifyWith(form, []crypto.PublicKey{pub2, pub1}); err != nil {
		t.Fatalf("verify with a two-key store: %v", err)
	}
}

func TestVerifyWithNoKeysFailsClosed(t *testing.T) {
	_, priv := testKey(t)
	form, _ := Sign(sampleDoc(), priv)
	if _, err := VerifyWith(form, nil); !errors.Is(err, ErrNoTrustedKeys) {
		t.Fatalf("err = %v, want ErrNoTrustedKeys", err)
	}
}

func TestParseRefusesWhatIsNotAnEntitlement(t *testing.T) {
	cases := map[string]error{
		"":                                   ErrFormMalformed,
		"hello":                              ErrFormMalformed,
		"entl1.abc":                          ErrFormMalformed,
		"entl2.abc.def":                      ErrFormVersion,
		"entl1.!!!.def":                      ErrFormMalformed,
		"entl1.YWJj.c2ln":                    ErrFormMalformed, // neither 64 bytes nor a DER signature
		strings.Repeat("a", MaxFormLength+1): ErrFormTooLong,
	}
	for form, want := range cases {
		if _, err := Parse(form); !errors.Is(err, want) {
			t.Errorf("Parse(%.20q) = %v, want %v", form, err, want)
		}
	}
}

// The signer must refuse to issue what the verifier would refuse.
func TestSignRefusesAnInvalidDocument(t *testing.T) {
	_, priv := testKey(t)
	cases := []struct {
		name   string
		mutate func(*Document)
		want   error
	}{
		{"version", func(d *Document) { d.Version = 2 }, ErrUnsupportedVersion},
		{"customer blank", func(d *Document) { d.CustomerID = "  " }, ErrCustomerIDMissing},
		{"customer long", func(d *Document) { d.CustomerID = strings.Repeat("x", MaxCustomerIDLength+1) }, ErrCustomerIDTooLong},
		{"cap negative", func(d *Document) { d.DeviceCap = -1 }, ErrDeviceCapNegative},
		{"kind", func(d *Document) { d.Kind = "perpetual" }, ErrKindUnknown},
		{"grace negative", func(d *Document) { d.GraceDays = -1 }, ErrGraceDaysOutOfRange},
		{"grace huge", func(d *Document) { d.GraceDays = MaxGraceDays + 1 }, ErrGraceDaysOutOfRange},
		{"note long", func(d *Document) { d.Note = strings.Repeat("n", MaxNoteLength+1) }, ErrNoteTooLong},
		{"dates missing", func(d *Document) { d.IssuedAt = time.Time{} }, ErrDatesMissing},
		{"expiry before issue", func(d *Document) { d.ExpiresAt = d.IssuedAt }, ErrExpiryBeforeIssue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := sampleDoc()
			tc.mutate(&doc)
			if _, err := Sign(doc, priv); !errors.Is(err, tc.want) {
				t.Fatalf("Sign = %v, want %v", err, tc.want)
			}
		})
	}
}

// The grace arithmetic, at every boundary. The grace runs from the SIGNED
// expiry: nothing about the install's own clock history enters it.
func TestGraceArithmetic(t *testing.T) {
	doc := sampleDoc() // expires 2026-04-01, 30 days grace
	graceEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if got := doc.GraceUntil(); !got.Equal(graceEnd) {
		t.Fatalf("GraceUntil = %v, want %v", got, graceEnd)
	}
	cases := []struct {
		at    time.Time
		phase Phase
		cap   int
	}{
		{doc.IssuedAt.Add(-24 * time.Hour), PhaseActive, 100}, // a clock behind issued_at is not lapsed
		{doc.ExpiresAt.Add(-time.Second), PhaseActive, 100},
		{doc.ExpiresAt, PhaseGrace, 100},
		{graceEnd.Add(-time.Second), PhaseGrace, 100},
		{graceEnd, PhaseLapsed, FreeDeviceCap},
		{graceEnd.AddDate(10, 0, 0), PhaseLapsed, FreeDeviceCap},
	}
	for _, tc := range cases {
		if got := doc.PhaseAt(tc.at); got != tc.phase {
			t.Errorf("at %v: phase %s, want %s", tc.at, got, tc.phase)
		}
		if got := doc.EffectiveCap(tc.at); got != tc.cap {
			t.Errorf("at %v: cap %d, want %d", tc.at, got, tc.cap)
		}
	}
}

// grace_days 0 means the cap falls the moment the file expires — the shape a
// short free key uses so that the grace does not double its term.
func TestZeroGraceLapsesAtExpiry(t *testing.T) {
	doc := sampleDoc()
	doc.GraceDays = 0
	if doc.PhaseAt(doc.ExpiresAt) != PhaseLapsed {
		t.Error("with no grace, expiry is the lapse")
	}
	if doc.PhaseAt(doc.ExpiresAt.Add(-time.Second)) != PhaseActive {
		t.Error("a second before expiry is still active")
	}
}

// A hosted file has no device cap; "unlimited" must survive the arithmetic
// rather than being read as "zero devices".
func TestUnlimitedCapStaysUnlimitedUntilLapse(t *testing.T) {
	doc := sampleDoc()
	doc.Kind = KindHosted
	doc.DeviceCap = 0
	if got := doc.EffectiveCap(doc.IssuedAt); !Unlimited(got) {
		t.Errorf("active hosted cap = %d, want unlimited", got)
	}
	if got := doc.EffectiveCap(doc.GraceUntil()); got != FreeDeviceCap {
		t.Errorf("lapsed hosted cap = %d, want the free cap", got)
	}
}

// The free cap is a FLOOR (docs/ENTITLEMENT.md): a genuinely signed file
// whose positive cap is below it still verifies, and acts as the free cap.
// 0 stays unlimited. Every phase, every boundary around the floor.
func TestSignedCapNeverLowersTheFreeFloor(t *testing.T) {
	pub, priv := testKey(t)
	base := sampleDoc() // expires 2026-04-01, 30 days grace
	active := base.ExpiresAt.Add(-time.Hour)
	grace := base.ExpiresAt.Add(time.Hour)
	lapsed := base.GraceUntil().Add(time.Hour)
	cases := []struct {
		signed     int
		activeCap  int
		belowFloor bool
	}{
		{0, 0, false}, // unlimited
		{1, FreeDeviceCap, true},
		{10, FreeDeviceCap, true},
		{24, FreeDeviceCap, true},
		{25, 25, false},
		{26, 26, false},
	}
	for _, tc := range cases {
		doc := base
		doc.DeviceCap = tc.signed
		form, err := Sign(doc, priv)
		if err != nil {
			t.Fatalf("cap %d: sign: %v", tc.signed, err)
		}
		got, err := VerifyWith(form, []crypto.PublicKey{pub})
		if err != nil {
			t.Fatalf("cap %d: a genuine sub-floor file must still verify: %v", tc.signed, err)
		}
		if got.DeviceCap != tc.signed {
			t.Errorf("cap %d: the signed value must be kept verbatim, got %d", tc.signed, got.DeviceCap)
		}
		if b := got.CapBelowFreeFloor(); b != tc.belowFloor {
			t.Errorf("cap %d: CapBelowFreeFloor = %v, want %v", tc.signed, b, tc.belowFloor)
		}
		if c := got.EffectiveCap(active); c != tc.activeCap {
			t.Errorf("cap %d: active effective cap %d, want %d", tc.signed, c, tc.activeCap)
		}
		if c := got.EffectiveCap(grace); c != tc.activeCap {
			t.Errorf("cap %d: grace effective cap %d, want %d", tc.signed, c, tc.activeCap)
		}
		if c := got.EffectiveCap(lapsed); c != FreeDeviceCap {
			t.Errorf("cap %d: lapsed effective cap %d, want the free cap", tc.signed, c)
		}
	}
}

// The tool in tools/entitlement-signer implements the format independently.
// Its output must verify here byte for byte, including a note carrying the
// characters an HTML-escaping encoder would rewrite.
func TestFixtureSignedByTheToolVerifies(t *testing.T) {
	form, err := os.ReadFile("testdata/signer-fixture.entl")
	if err != nil {
		t.Fatal(err)
	}
	pemData, err := os.ReadFile("testdata/signer-fixture.pem")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParsePublicKeysPEM(pemData)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := VerifyWith(string(form), keys)
	if err != nil {
		t.Fatalf("the tool's fixture does not verify: %v", err)
	}
	if doc.CustomerID != "example-customer" || doc.DeviceCap != 100 || doc.Kind != KindEvaluation || doc.GraceDays != 14 {
		t.Errorf("fixture decoded to %+v", *doc)
	}
	if !strings.Contains(doc.Note, "<not> HTML & escaped") {
		t.Errorf("note lost its unescaped characters: %q", doc.Note)
	}
	// Re-encoding what we parsed must reproduce the tool's payload bytes:
	// that is the canonical-encoding agreement the two implementations owe
	// each other.
	parsed, _ := Parse(string(form))
	ours, _ := Encode(*doc)
	if string(ours) != string(parsed.Payload) {
		t.Errorf("canonical encodings differ:\n tool %s\n ours %s", parsed.Payload, ours)
	}
	// The fixture key is a test key, not the production trust store.
	if _, err := Verify(string(form)); !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("fixture verified against the embedded keys: %v", err)
	}
}

func TestEmbeddedTrustStoreLoads(t *testing.T) {
	if err := TrustedKeysError(); err != nil {
		t.Fatalf("embedded keys: %v", err)
	}
	if len(TrustedKeys()) == 0 {
		t.Fatal("no embedded key")
	}
}

// The development key is recognised by its fingerprint, from the copy kept
// in testdata so the check outlives the swap to the token-held key; a
// different key is not mistaken for it; and what the build embeds today is
// reported for what it is.
func TestDevelopmentKeyIsRecognised(t *testing.T) {
	data, err := os.ReadFile("testdata/development-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	devKeys, err := ParsePublicKeysPEM(data)
	if err != nil || len(devKeys) != 1 {
		t.Fatalf("development key copy: %v", err)
	}
	if got := KeyFingerprint(devKeys[0]); got != DevelopmentKeyFingerprint {
		t.Fatalf("DevelopmentKeyFingerprint = %s, but the development key hashes to %s", DevelopmentKeyFingerprint, got)
	}
	other, _ := testKey(t)
	if !containsDevelopmentKey([]crypto.PublicKey{other, devKeys[0]}) {
		t.Error("a set holding the development key was not recognised")
	}
	if containsDevelopmentKey([]crypto.PublicKey{other}) {
		t.Error("another key was taken for the development key")
	}
	embedded := false
	for _, k := range TrustedKeys() {
		if KeyFingerprint(k) == KeyFingerprint(devKeys[0]) {
			embedded = true
		}
	}
	if EmbedsDevelopmentKey() != embedded {
		t.Errorf("EmbedsDevelopmentKey() = %v, but the embedded set does%s hold the development key",
			EmbedsDevelopmentKey(), map[bool]string{true: "", false: " not"}[embedded])
	}
}

func TestDeviceCapErrorDeclaresItsMeaning(t *testing.T) {
	e := &DeviceCapError{Cap: 25, Count: 25, InstanceOperator: true, AccountDevices: 3, AccountDevicesKnown: true}
	if e.HTTPStatus() != 409 || e.ErrorCode() != DeviceCapErrorCode {
		t.Errorf("status %d code %q", e.HTTPStatus(), e.ErrorCode())
	}
	msg := e.Error()
	for _, want := range []string{"25", DefaultSubscriptionURL, "keep working"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	details, _ := e.ErrorDetails().(map[string]any)
	if details["cap"] != 25 || details["count"] != int64(25) || details["subscription_url"] != DefaultSubscriptionURL ||
		details["account_devices"] != int64(3) || details["instance_operator"] != true {
		t.Errorf("operator details = %v", details)
	}
}

// The install-wide admitted count is the
// operator's, not a tenant's. A refusal to anyone else carries the caller's
// own count, the cap and instance_operator:false — and neither the count nor
// the subscription link in the details or the message. The zero value is
// that tenant view, so an unscoped refusal discloses nothing either.
func TestDeviceCapErrorShowsATenantOnlyItsOwnCount(t *testing.T) {
	e := &DeviceCapError{Cap: 25, Count: 97, SubscriptionURL: "https://example.com/plans",
		AccountDevices: 4, AccountDevicesKnown: true}
	if e.Error() != TenantRefusalMessage {
		t.Errorf("tenant message = %q", e.Error())
	}
	details, _ := e.ErrorDetails().(map[string]any)
	want := map[string]any{"account_devices": int64(4), "instance_operator": false, "cap": 25}
	if len(details) != len(want) {
		t.Fatalf("tenant details = %v, want exactly %v", details, want)
	}
	for k, v := range want {
		if details[k] != v {
			t.Errorf("tenant details[%q] = %v, want %v", k, details[k], v)
		}
	}

	unscoped := &DeviceCapError{Cap: 25, Count: 97}
	d, _ := unscoped.ErrorDetails().(map[string]any)
	if _, leaked := d["count"]; leaked || strings.Contains(unscoped.Error(), "97") {
		t.Errorf("an unscoped refusal disclosed the install count: %v / %q", d, unscoped.Error())
	}
	if _, present := d["account_devices"]; present {
		t.Errorf("an unknown account count was rendered: %v", d)
	}
}

// A cap that could not be checked is its own refusal: 503 (try again), its
// own code, a message that names neither the database's error nor a count,
// and no unwrapping to the cause — a caller matching a driver error must not
// mistake the refusal for it.
func TestDeviceCapUnverifiedErrorDeclaresItsMeaning(t *testing.T) {
	cause := errors.New("ERROR: canceling statement due to statement timeout (SQLSTATE 57014)")
	e := &DeviceCapUnverifiedError{Cap: 25, Cause: cause}
	if e.HTTPStatus() != 503 || e.ErrorCode() != DeviceCapUnverifiedErrorCode || e.ErrorCode() == DeviceCapErrorCode {
		t.Errorf("status %d code %q", e.HTTPStatus(), e.ErrorCode())
	}
	if strings.Contains(e.Error(), "SQLSTATE") || strings.Contains(e.Error(), "25") {
		t.Errorf("message %q carries the cause or a number", e.Error())
	}
	if !strings.Contains(e.Error(), "could not be checked") {
		t.Errorf("message %q does not say the limit could not be checked", e.Error())
	}
	if errors.Is(e, cause) {
		t.Error("the refusal unwraps to the database's error")
	}
	if GRPCUnverifiedMessage == GRPCRefusalMessage {
		t.Error("the gRPC messages are equal; an agent would hold for the cap's long retry")
	}
}
