package secret

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestNewSecret(t *testing.T) {
	acc := uuid.New()

	if _, err := NewSecret(acc, "  ", KindEnv, "K", "V", true); err != ErrNameRequired {
		t.Fatalf("expected ErrNameRequired for blank name, got %v", err)
	}
	if _, err := NewSecret(acc, "name", SecretKind("bogus"), "K", "V", true); err != ErrInvalidKind {
		t.Fatalf("expected ErrInvalidKind for bad kind, got %v", err)
	}

	s, err := NewSecret(acc, "  DB_PASSWORD  ", KindEnv, "DB_PASSWORD", "s3cret", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name != "DB_PASSWORD" {
		t.Errorf("name should be trimmed, got %q", s.Name)
	}
	if !s.HasValue() {
		t.Error("expected HasValue to be true")
	}
	if s.AccountID != acc {
		t.Error("account id should be set")
	}
}

func TestValidKind(t *testing.T) {
	for _, k := range []SecretKind{KindEnv, KindCredential, KindOpaque} {
		if !ValidKind(k) {
			t.Errorf("%q should be valid", k)
		}
	}
	if ValidKind(SecretKind("nope")) {
		t.Error("unknown kind should be invalid")
	}
}

func TestNewSecretForcesSensitiveForNonEnvKinds(t *testing.T) {
	cases := []struct {
		kind          SecretKind
		sensitiveArg  bool
		wantSensitive bool
	}{
		{KindEnv, false, false},
		{KindEnv, true, true},
		{KindCredential, false, true},
		{KindCredential, true, true},
		{KindOpaque, false, true},
		{KindOpaque, true, true},
	}
	for _, tc := range cases {
		s, err := NewSecret(uuid.New(), "name", tc.kind, "k", "v", tc.sensitiveArg)
		if err != nil {
			t.Fatalf("NewSecret(%s, sensitive=%v): %v", tc.kind, tc.sensitiveArg, err)
		}
		if s.Sensitive != tc.wantSensitive {
			t.Errorf("kind=%s sensitive arg=%v: got %v, want %v", tc.kind, tc.sensitiveArg, s.Sensitive, tc.wantSensitive)
		}
	}
}

func TestChangeSensitivityTransitions(t *testing.T) {
	// Variable (env, non-sensitive) -> sensitive: allowed.
	v, _ := NewSecret(uuid.New(), "var", KindEnv, "K", "v", false)
	if err := v.ChangeSensitivity(true); err != nil {
		t.Fatalf("variable -> sensitive must be allowed: %v", err)
	}
	// ...and never back.
	if err := v.ChangeSensitivity(false); !errors.Is(err, ErrCannotDesensitize) {
		t.Fatalf("sensitive -> variable must be rejected, got %v", err)
	}

	// Credential can never become non-sensitive.
	c, _ := NewSecret(uuid.New(), "cred", KindCredential, "user", "pw", true)
	if err := c.ChangeSensitivity(false); !errors.Is(err, ErrCannotDesensitize) {
		t.Fatalf("credential -> variable must be rejected, got %v", err)
	}

	// Setting an already-false flag to false is a no-op, not an error.
	nv, _ := NewSecret(uuid.New(), "var2", KindEnv, "K", "v", false)
	if err := nv.ChangeSensitivity(false); err != nil {
		t.Fatalf("no-op false -> false must be allowed: %v", err)
	}
}

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{" Node-Specific ", "AUTOMATION", "automation", "", "infra"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{"node-specific", "automation", "infra"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}

	if _, err := NormalizeTags([]string{"has space"}); !errors.Is(err, ErrInvalidTag) {
		t.Errorf("expected ErrInvalidTag for space, got %v", err)
	}
	if _, err := NormalizeTags([]string{"comma,inside"}); !errors.Is(err, ErrInvalidTag) {
		t.Errorf("expected ErrInvalidTag for comma, got %v", err)
	}
	if _, err := NormalizeTags([]string{"naïve"}); !errors.Is(err, ErrInvalidTag) {
		t.Errorf("expected ErrInvalidTag for non-ascii, got %v", err)
	}
	many := make([]string, 11)
	for i := range many {
		many[i] = fmt.Sprintf("tag-%d", i)
	}
	if _, err := NormalizeTags(many); !errors.Is(err, ErrTooManyTags) {
		t.Errorf("expected ErrTooManyTags, got %v", err)
	}
}

func TestMergeTags(t *testing.T) {
	got, err := MergeTags([]string{"automation"}, []string{"External", "automation"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(got) != 2 || got[0] != "automation" || got[1] != "external" {
		t.Fatalf("expected [automation external], got %v", got)
	}
}
