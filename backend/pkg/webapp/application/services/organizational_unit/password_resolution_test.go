package organizational_unit

import "testing"

func strPtr(s string) *string { return &s }

// The precedence itself: node beats OU beats root beats account.
func TestPickPasswordPrecedence(t *testing.T) {
	got, unreadable := pickPassword(
		passwordLevel{value: "from-node"},
		passwordLevelPtr(strPtr("from-ou"), false),
		passwordLevelPtr(strPtr("from-root"), false),
		passwordLevelPtr(strPtr("from-account"), false),
	)
	if unreadable || got != "from-node" {
		t.Fatalf("got (%q, %v), want the node's value", got, unreadable)
	}

	got, _ = pickPassword(
		passwordLevel{},
		passwordLevelPtr(nil, false),
		passwordLevelPtr(strPtr("from-root"), false),
		passwordLevelPtr(strPtr("from-account"), false),
	)
	if got != "from-root" {
		t.Fatalf("got %q, want the root OU's value once the nearer levels are unset", got)
	}

	got, unreadable = pickPassword(passwordLevel{}, passwordLevelPtr(nil, false))
	if unreadable || got != "" {
		t.Fatalf("got (%q, %v), want no password and no error flag", got, unreadable)
	}
}

// The whole reason this is not pickString: a level that HOLDS a password we
// cannot decrypt is present and broken, not absent. Falling through would give
// the device the parent's credential instead — one password silently
// substituted for another, reported as success.
func TestPickPasswordStopsAtAnUnreadableLevel(t *testing.T) {
	got, unreadable := pickPassword(
		passwordLevel{unreadable: true}, // node: stored, undecryptable
		passwordLevelPtr(strPtr("from-ou"), false),
		passwordLevelPtr(strPtr("from-root"), false),
	)
	if !unreadable {
		t.Fatal("an unreadable node password must be reported, not inherited over")
	}
	if got != "" {
		t.Fatalf("got %q, want no password", got)
	}

	// Same rule one level down: the node is genuinely unset, the OU is broken.
	got, unreadable = pickPassword(
		passwordLevel{},
		passwordLevelPtr(nil, true),
		passwordLevelPtr(strPtr("from-root"), false),
	)
	if !unreadable || got != "" {
		t.Fatalf("got (%q, %v), want the OU's failure to stop resolution", got, unreadable)
	}
}

// A broken level BELOW the one that answers is irrelevant: resolution never
// reaches it, so a stale unreadable account policy must not block a node that
// has its own password.
func TestPickPasswordIgnoresUnreadableLevelsItNeverReaches(t *testing.T) {
	got, unreadable := pickPassword(
		passwordLevel{value: "from-node"},
		passwordLevelPtr(nil, true),
		passwordLevelPtr(nil, true),
	)
	if unreadable || got != "from-node" {
		t.Fatalf("got (%q, %v), want the node's value and no failure", got, unreadable)
	}
}
