package ssh_host_key_ticket

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const fp = "SHA256:serverkeyserverkeyserverkeyserverkeyserverk"

func testKey(b byte) []byte {
	key := make([]byte, KeyBytes)
	for i := range key {
		key[i] = b
	}
	return key
}

func issuerAt(t *testing.T, now time.Time) *Issuer {
	t.Helper()
	iss, err := NewIssuer(testKey(7), DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	return iss.WithClock(func() time.Time { return now })
}

func binding() Binding {
	return Binding{
		AccountID:          uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		UserID:             uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		Host:               "pi.lan",
		Port:               22,
		DialAddr:           "192.0.2.7",
		HostKeyFingerprint: fp,
	}
}

func request(b Binding) Binding {
	b.HostKeyFingerprint = ""
	return b
}

func TestIssueThenVerifyReturnsTheObservedFingerprint(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	iss := issuerAt(t, now)
	token, exp, err := iss.Issue(binding())
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(DefaultTTL)) {
		t.Errorf("expiresAt = %s, want now+TTL", exp)
	}
	got, err := iss.Verify(token, request(binding()))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != fp {
		t.Errorf("fingerprint = %q, want %q", got, fp)
	}
}

func TestTicketIsCompactAndOpaqueToTheEye(t *testing.T) {
	iss := issuerAt(t, time.Now())
	token, _, err := iss.Issue(binding())
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(token, " \n=") {
		t.Errorf("token %q carries characters that need quoting", token)
	}
	if len(token) > 320 {
		t.Errorf("token is %d bytes; it travels in JSON on every adopt", len(token))
	}
}

func TestExpiredTicketIsRefused(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	iss := issuerAt(t, now)
	token, _, _ := iss.Issue(binding())

	late := iss.WithClock(func() time.Time { return now.Add(DefaultTTL + time.Second) })
	_, err := late.Verify(token, request(binding()))
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
	if !strings.Contains(refusal.Reason, "15 minutes") {
		t.Errorf("reason %q should tell the operator how old is too old", refusal.Reason)
	}
}

func TestTicketStillValidOneSecondBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	iss := issuerAt(t, now)
	token, _, _ := iss.Issue(binding())
	iss.WithClock(func() time.Time { return now.Add(DefaultTTL - time.Second) })
	if _, err := iss.Verify(token, request(binding())); err != nil {
		t.Fatalf("Verify just before expiry: %v", err)
	}
}

func TestEveryBoundFieldIsChecked(t *testing.T) {
	iss := issuerAt(t, time.Now())
	token, _, _ := iss.Issue(binding())

	cases := map[string]struct {
		mutate func(*Binding)
		want   string
	}{
		"account": {func(b *Binding) { b.AccountID = uuid.New() }, "different account"},
		"user":    {func(b *Binding) { b.UserID = uuid.New() }, "different user"},
		"host":    {func(b *Binding) { b.Host = "other.lan" }, "different host"},
		"port":    {func(b *Binding) { b.Port = 2222 }, "different port"},
		"address": {func(b *Binding) { b.DialAddr = "10.0.0.1" }, "different address"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := request(binding())
			tc.mutate(&req)
			_, err := iss.Verify(token, req)
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want a Refusal", err)
			}
			if !strings.Contains(refusal.Reason, tc.want) {
				t.Errorf("reason = %q, want it to name the %s", refusal.Reason, name)
			}
		})
	}
}

func TestTamperedPayloadIsRefusedAsMalformed(t *testing.T) {
	iss := issuerAt(t, time.Now())
	token, _, _ := iss.Issue(binding())
	dot := strings.IndexByte(token, '.')
	payload, _ := base64.RawURLEncoding.DecodeString(token[:dot])
	// Swap the fingerprint for another one, keeping the original MAC.
	forged := strings.Replace(string(payload), fp, "SHA256:attackerkeyattackerkeyattackerkeyattac", 1)
	forgedToken := base64.RawURLEncoding.EncodeToString([]byte(forged)) + token[dot:]

	_, err := iss.Verify(forgedToken, request(binding()))
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestTicketFromAnotherKeyIsRefused(t *testing.T) {
	a := issuerAt(t, time.Now())
	token, _, _ := a.Issue(binding())
	b, _ := NewIssuer(testKey(9), DefaultTTL)
	if _, err := b.Verify(token, request(binding())); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed (a restart must invalidate tickets)", err)
	}
}

func TestGarbageTokensAreMalformedNotPanics(t *testing.T) {
	iss := issuerAt(t, time.Now())
	for _, tok := range []string{"", ".", "abc", "abc.", ".abc", "not base64!.xx", "YQ.YQ", "eyJ9.c2ln"} {
		if _, err := iss.Verify(tok, request(binding())); !errors.Is(err, ErrMalformed) {
			t.Errorf("Verify(%q) = %v, want ErrMalformed", tok, err)
		}
	}
}

func TestIssueRefusesAnEmptyFingerprint(t *testing.T) {
	iss := issuerAt(t, time.Now())
	b := binding()
	b.HostKeyFingerprint = ""
	if _, _, err := iss.Issue(b); err == nil {
		t.Fatal("Issue accepted a binding with no fingerprint")
	}
}

func TestRandomIssuersDoNotShareKeys(t *testing.T) {
	a, err := NewRandomIssuer(0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRandomIssuer(0)
	if err != nil {
		t.Fatal(err)
	}
	if a.TTL() != DefaultTTL {
		t.Errorf("TTL = %s, want the default", a.TTL())
	}
	token, _, _ := a.Issue(binding())
	if _, err := b.Verify(token, request(binding())); err == nil {
		t.Fatal("a ticket from one process verified in another")
	}
}
