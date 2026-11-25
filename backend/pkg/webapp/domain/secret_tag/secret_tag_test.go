package secret_tag

import (
	"testing"

	"github.com/google/uuid"
)

func TestDefaultColorForIsStableAndInPalette(t *testing.T) {
	c1 := DefaultColorFor("infra")
	c2 := DefaultColorFor("infra")
	if c1 != c2 {
		t.Fatalf("default color not stable: %q vs %q", c1, c2)
	}
	if !ValidColor(c1) {
		t.Fatalf("default color %q not in palette", c1)
	}
}

func TestValidColor(t *testing.T) {
	if !ValidColor("violet") || !ValidColor("lightBlue") {
		t.Error("expected palette colors to be valid")
	}
	if ValidColor("chartreuse") || ValidColor("") {
		t.Error("expected off-palette colors to be invalid")
	}
}

func TestNewTag(t *testing.T) {
	acc := uuid.New()

	// Explicit color + name normalization (lowercased/trimmed).
	tag, err := NewTag(acc, "  Infra  ", "blue")
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	if tag.Name != "infra" || tag.Color != "blue" {
		t.Fatalf("unexpected tag: %+v", tag)
	}

	// Empty color falls back to the deterministic default.
	tag2, err := NewTag(acc, "prod", "")
	if err != nil {
		t.Fatalf("NewTag default: %v", err)
	}
	if tag2.Color != DefaultColorFor("prod") {
		t.Errorf("expected default color, got %q", tag2.Color)
	}

	// Invalid color rejected.
	if _, err := NewTag(acc, "x", "neon"); err != ErrInvalidColor {
		t.Errorf("expected ErrInvalidColor, got %v", err)
	}
	// Empty name rejected.
	if _, err := NewTag(acc, "   ", "blue"); err == nil {
		t.Error("expected empty name to be rejected")
	}
	// Invalid charset rejected.
	if _, err := NewTag(acc, "Has Space", "blue"); err == nil {
		t.Error("expected invalid name charset to be rejected")
	}
}
