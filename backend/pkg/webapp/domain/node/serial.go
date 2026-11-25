package node

import (
	"regexp"
	"strings"
)

// FullSerialLen is the width, in hex nibbles, of a full Raspberry Pi hardware
// serial. A shorter value is the low word — all a WebUSB harvest can see.
const FullSerialLen = 16

// fullSerialRe matches a full Raspberry Pi hardware serial: exactly 16 hex
// nibbles. Shorter values (the 8-hex WebUSB low word) and non-hex strings
// (the agent's "unknown" sentinel, vendor placeholders) do not match.
var fullSerialRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// NormalizeSerial canonicalizes a hardware serial for comparison: trimmed and
// lowercased. Stored values are never rewritten — normalization applies only
// where serials are compared, so legacy rows keep working verbatim.
func NormalizeSerial(serial string) string {
	return strings.ToLower(strings.TrimSpace(serial))
}

// IsUniqueFullSerial reports whether a NORMALIZED serial is a full 16-hex
// serial that can be treated as globally unique. Degenerate serials made of a
// single repeated nibble (all-zeros etc.) are excluded: boards and VMs with no
// wired SoC serial genuinely report such values, so two unrelated devices may
// share one — treating them as unique would let the first account that adopts
// such a device lock every other tenant's identical boards out.
func IsUniqueFullSerial(normalizedSerial string) bool {
	if !fullSerialRe.MatchString(normalizedSerial) {
		return false
	}
	first := normalizedSerial[0]
	for i := 1; i < len(normalizedSerial); i++ {
		if normalizedSerial[i] != first {
			return true
		}
	}
	return false
}

// LowWordLen is the width, in hex nibbles, of a serial's low word: the eight
// hex a WebUSB harvest reads and an EEPROM baked from it asks for at boot.
const LowWordLen = 8

// LowWord returns the low word of a NORMALIZED full serial, or "" when the
// serial is not a full one.
func LowWord(normalizedSerial string) string {
	if !fullSerialRe.MatchString(normalizedSerial) {
		return ""
	}
	return normalizedSerial[FullSerialLen-LowWordLen:]
}

// LowWordIsGuarded reports whether a NORMALIZED serial takes part in the
// cross-account low-word guard: a full non-degenerate serial whose low word is
// not itself a single repeated nibble, or a hex stamp between the low-word
// width and a full serial. The degenerate forms are excluded for the reason
// IsUniqueFullSerial excludes them: unrelated boards genuinely report them.
func LowWordIsGuarded(normalizedSerial string) bool {
	if IsUniqueFullSerial(normalizedSerial) {
		return !repeatedNibble(LowWord(normalizedSerial))
	}
	return shortStampRe.MatchString(normalizedSerial) && !repeatedNibble(normalizedSerial)
}

// shortStampRe matches a short (low-word) stamp: hex, at least the low-word
// width, shorter than a full serial.
var shortStampRe = regexp.MustCompile(`^[0-9a-f]{8,15}$`)

func repeatedNibble(s string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return true
}

// SerialConflictsAcrossAccounts reports whether binding this NORMALIZED serial
// would take it out of another account's hands.
//
// All three conditions are stated here once because two kinds of caller have to
// agree on them: the guard that REFUSES a cross-account bind, and any read that
// tells an operator such a bind would be refused. A read that asked only "does
// another account hold this" would flag rows the guard binds happily — an
// account holding a stale duplicate of its OWN serial, every short WebUSB stamp,
// every all-zeros VM serial — and a panel built on that answer would hide the
// only recovery affordance a healthy device has.
func SerialConflictsAcrossAccounts(normalizedSerial string, heldByThisAccount, heldByAnotherAccount bool) bool {
	return IsUniqueFullSerial(normalizedSerial) && heldByAnotherAccount && !heldByThisAccount
}
