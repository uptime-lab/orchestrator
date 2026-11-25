package buildinfo

import "testing"

func TestRevision(t *testing.T) {
	if got := Revision(); got != "" {
		t.Fatalf("Revision() before SetRevision = %q, want empty", got)
	}
	SetRevision("0123456789ab")
	if got := Revision(); got != "0123456789ab" {
		t.Fatalf("Revision() = %q, want the recorded value", got)
	}
}

func TestVersion(t *testing.T) {
	if got := Version(); got != "" {
		t.Fatalf("Version() before SetVersion = %q, want empty", got)
	}
	SetVersion("0.1.1")
	if got := Version(); got != "0.1.1" {
		t.Fatalf("Version() = %q, want the stamped value", got)
	}
}
