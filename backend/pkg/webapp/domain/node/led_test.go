package node

import (
	"errors"
	"testing"
)

// The Validate bounds are what stands between an arbitrary client payload and
// the agent: sources outside the vocabulary and oversized lists must be
// refused with the operator-worded sentinel (the handler maps it to 400).
func TestLEDConfigValidate(t *testing.T) {
	ok := LEDConfig{DigitalSources: []string{"temperature", "load", "off"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	bad := LEDConfig{DigitalSources: []string{"sparkle"}}
	if err := bad.Validate(); !errors.Is(err, ErrLEDInvalid) {
		t.Fatalf("unknown source: got %v, want ErrLEDInvalid", err)
	}
	long := LEDConfig{DigitalSources: make([]string, LEDMaxDigitalSources+1)}
	for i := range long.DigitalSources {
		long.DigitalSources[i] = LEDSourceOff
	}
	if err := long.Validate(); !errors.Is(err, ErrLEDInvalid) {
		t.Fatalf("oversized list: got %v, want ErrLEDInvalid", err)
	}
}

// Hash must be stable for equal configs and differ when the config differs —
// it is what the settle records and what in_sync compares.
func TestLEDConfigHash(t *testing.T) {
	a := LEDConfig{StealthMode: true, DigitalSources: []string{"temperature"}}
	b := LEDConfig{StealthMode: true, DigitalSources: []string{"temperature"}}
	c := LEDConfig{StealthMode: false, DigitalSources: []string{"temperature"}}
	if a.Hash() == "" || a.Hash() != b.Hash() {
		t.Fatal("equal configs must hash equal and non-empty")
	}
	if a.Hash() == c.Hash() {
		t.Fatal("different configs must hash differently")
	}
}

// IsZero gates whether the agent keeps a state file at all; an all-off
// sources list is still "zero" (nothing to converge, nothing to persist).
func TestLEDConfigIsZero(t *testing.T) {
	if !(&LEDConfig{DigitalSources: []string{"off", "off"}}).IsZero() {
		t.Fatal("all-off sources should be zero")
	}
	if (&LEDConfig{PwrLEDOff: true}).IsZero() {
		t.Fatal("pwr off is not zero")
	}
}
