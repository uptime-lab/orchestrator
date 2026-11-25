package cdnsign

import (
	"net/url"
	"testing"
	"time"
)

func TestSignAndVerifyRoundTrip(t *testing.T) {
	key := DeriveKey("some-strong-secret")
	name := "images/abc/os.img"

	signed, err := AppendSignature("http://host/cdn/"+name, name, key, time.Hour)
	if err != nil {
		t.Fatalf("AppendSignature: %v", err)
	}

	u, err := url.Parse(signed)
	if err != nil {
		t.Fatalf("parse signed url: %v", err)
	}
	q := u.Query()
	if err := Verify(name, q.Get(ParamExpires), q.Get(ParamSignature), key); err != nil {
		t.Fatalf("Verify valid signature: %v", err)
	}
}

func TestVerifyRejectsMissing(t *testing.T) {
	key := DeriveKey("s")
	if err := Verify("n", "", "", key); err == nil {
		t.Fatal("expected error for missing signature")
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	key := DeriveKey("s")
	name := "images/x.img"
	signed, _ := AppendSignature("http://h/cdn/"+name, name, key, time.Hour)
	q, _ := url.Parse(signed)
	// Verify against a different path -> signature must not match.
	if err := Verify("images/other.img", q.Query().Get(ParamExpires), q.Query().Get(ParamSignature), key); err == nil {
		t.Fatal("expected error for path mismatch")
	}
	// Wrong key must not match.
	if err := Verify(name, q.Query().Get(ParamExpires), q.Query().Get(ParamSignature), DeriveKey("other")); err == nil {
		t.Fatal("expected error for wrong key")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	key := DeriveKey("s")
	name := "images/x.img"
	signed, _ := AppendSignature("http://h/cdn/"+name, name, key, -time.Second)
	q, _ := url.Parse(signed)
	if err := Verify(name, q.Query().Get(ParamExpires), q.Query().Get(ParamSignature), key); err == nil {
		t.Fatal("expected error for expired signature")
	}
}
