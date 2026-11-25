package node

import "testing"

func TestNormalizeSerial(t *testing.T) {
	cases := map[string]string{
		"  10000000ABCDEF12  ": "10000000abcdef12",
		"10000000abcdef12":     "10000000abcdef12",
		"UNKNOWN":              "unknown",
		"":                     "",
	}
	for in, want := range cases {
		if got := NormalizeSerial(in); got != want {
			t.Errorf("NormalizeSerial(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsUniqueFullSerial(t *testing.T) {
	cases := []struct {
		serial string
		want   bool
	}{
		{"10000000abcdef12", true},  // full CM serial
		{"00000000abcd0001", true},  // classic Pi (zero high word, distinct low word)
		{"0000000000000000", false}, // degenerate: boards with no wired serial share it
		{"ffffffffffffffff", false}, // degenerate: repeated nibble
		{"aaaaaaaaaaaaaaaa", false}, // degenerate: repeated nibble
		{"abcd0001", false},         // short WebUSB low word
		{"unknown", false},          // agent sentinel for non-Pi hardware
		{"", false},
		{"10000000abcdef1", false},   // 15 chars
		{"10000000abcdef123", false}, // 17 chars
		{"10000000ABCDEF12", false},  // not normalized (uppercase) — callers must normalize first
		{"to be filled by ", false},  // vendor garbage
	}
	for _, c := range cases {
		if got := IsUniqueFullSerial(c.serial); got != c.want {
			t.Errorf("IsUniqueFullSerial(%q) = %v, want %v", c.serial, got, c.want)
		}
	}
}

// The predicate exists so a read that REPORTS a cross-account conflict and the
// guard that REFUSES one cannot disagree. The rows that matter are the ones a
// naive "does another account hold it" would get wrong: an account holding a
// stale duplicate of its own serial, and every serial the guard never treats as
// unique in the first place.
func TestSerialConflictsAcrossAccounts(t *testing.T) {
	const full = "10000000abcdef12"
	cases := []struct {
		name              string
		serial            string
		heldByThisAccount bool
		heldByAnother     bool
		want              bool
	}{
		{"another account holds it", full, false, true, true},
		{"nobody else holds it", full, false, false, false},
		{"only this account holds it", full, true, false, false},
		{"both hold it — the own-account exemption", full, true, true, false},
		{"short WebUSB stamp is never guarded", "e86a9c8a", false, true, false},
		{"degenerate serial is shared by real boards", "0000000000000000", false, true, false},
		{"agent sentinel", "unknown", false, true, false},
		{"empty", "", false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SerialConflictsAcrossAccounts(c.serial, c.heldByThisAccount, c.heldByAnother); got != c.want {
				t.Errorf("SerialConflictsAcrossAccounts(%q, this=%v, other=%v) = %v, want %v",
					c.serial, c.heldByThisAccount, c.heldByAnother, got, c.want)
			}
		})
	}
}
