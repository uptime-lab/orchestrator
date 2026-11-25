package node_command

import (
	"os"
	"regexp"
	"testing"
)

// slugConstPattern finds the Slug* constant declarations in the package source.
var slugConstPattern = regexp.MustCompile(`(?m)^\s*(Slug\w+)\s*=\s*"([a-z_]+)"`)

// AllSlugs is what the cross-package guards check against, so it drifting from
// the constants is not a cosmetic problem: a slug missing from it is a slug the
// staleness guards never look at.
//
// Read from the SOURCE rather than by reflection, because the point is to catch
// a constant somebody added without deciding what it means for the five
// behavioural maps — and a reflective inventory would have absorbed it.
func TestAllSlugsCoversEveryConstant(t *testing.T) {
	src, err := os.ReadFile("node_command.go")
	if err != nil {
		t.Fatalf("reading the package source: %v", err)
	}
	matches := slugConstPattern.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 25 {
		t.Fatalf("found only %d slug constants; the pattern has stopped matching the source", len(matches))
	}

	inventory := map[string]bool{}
	for _, s := range AllSlugs() {
		if inventory[s] {
			t.Errorf("AllSlugs lists %q twice", s)
		}
		inventory[s] = true
	}

	declared := map[string]bool{}
	for _, m := range matches {
		declared[m[2]] = true
		if !inventory[m[2]] {
			t.Errorf("%s = %q is declared but absent from AllSlugs. Add it — and while you are "+
				"there, decide whether it is queueable, managed-OS-only, rebooting, "+
				"device-exclusive and how it is authorized", m[1], m[2])
		}
	}
	for slug := range inventory {
		if !declared[slug] {
			t.Errorf("AllSlugs lists %q, which no constant declares any more", slug)
		}
	}
}
