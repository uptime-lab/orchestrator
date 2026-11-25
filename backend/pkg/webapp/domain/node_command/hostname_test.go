package node_command

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeHostnameAcceptsOnlyWhatTheDeviceAccepts(t *testing.T) {
	valid := []struct{ in, want string }{
		{"node-01", "node-01"},
		{"  node-01\n", "node-01"},
		{"a", "a"},
		{strings.Repeat("a", 63), strings.Repeat("a", 63)},
	}
	for _, tc := range valid {
		got, err := NormalizeHostname(tc.in)
		if err != nil {
			t.Errorf("NormalizeHostname(%q) rejected a valid label: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeHostname(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	invalid := []string{
		"",
		"-leading",
		"trailing-",
		"UPPER",
		"has space",
		"dots.are.not.a.label",
		"tok/en",
		strings.Repeat("a", 64),
	}
	for _, in := range invalid {
		if _, err := NormalizeHostname(in); !errors.Is(err, ErrInvalidHostname) {
			t.Errorf("NormalizeHostname(%q) must fail with ErrInvalidHostname, got %v", in, err)
		}
	}
}

// A rejection message is written verbatim into node_commands.details, which is
// plaintext and readable by every node.read caller — and the value that gets
// rejected is typically a resolved {{secrets.*}}.
func TestTheRejectionMessageDoesNotNameTheRejectedValue(t *testing.T) {
	const secret = "S3cret-Token-Value"
	_, err := NormalizeHostname(secret)
	if err == nil {
		t.Fatal("expected the value to be rejected")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the rejected value was quoted back into the error: %q", err)
	}
}
