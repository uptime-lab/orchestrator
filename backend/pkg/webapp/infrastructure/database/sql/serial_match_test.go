package sql

import "testing"

// The suffix fallback in GetByHardwareSerialGlobal runs with NO account filter,
// because the netboot endpoint has only a serial and no account context. It is
// therefore reachable from an unauthenticated URL segment, and the httpboot
// serial pattern accepts a single character.
//
// Without the floor, `right(hardware_serial, 1) = 'f'` would resolve to the
// newest node in that hex bucket across every account, an enumeration
// primitive. It would also bypass the endpoint's abuse guard, which strikes
// only when a lookup FAILS: a suffix probe succeeds, so probing would accrue no
// strikes. The floor keeps that guard working, because a sub-floor serial falls
// through to not-found.
func TestSuffixMatchIsRefusedForSerialsShortEnoughToEnumerate(t *testing.T) {
	for _, serial := range []string{"f", "ab", "abc", "abcd", "abcde", "abcdef", "abcdefa"} {
		if isSuffixMatchableSerial(serial) {
			t.Errorf("serial %q (len %d) must NOT be suffix-matchable: it is an enumeration primitive",
				serial, len(serial))
		}
	}
}

// 8 hex is what the WebUSB flasher harvests from the USB descriptor, so it is
// the real low-word width and must keep working.
func TestSuffixMatchAcceptsTheWebUSBLowWordWidth(t *testing.T) {
	for _, serial := range []string{"e86a9c8a", "abcdefab", "abcdefabc", "abcdefabcdefabc"} {
		if !isSuffixMatchableSerial(serial) {
			t.Errorf("serial %q (len %d) is a legitimate low-word stamp and must stay matchable",
				serial, len(serial))
		}
	}
}

// A full 16-hex serial is matched exactly, never by suffix — otherwise a full
// serial could match a longer stored one.
func TestFullSerialIsNeverSuffixMatched(t *testing.T) {
	for _, serial := range []string{"abcdefabcdefabcd", "abcdefabcdefabcde"} {
		if isSuffixMatchableSerial(serial) {
			t.Errorf("serial %q (len %d) is full-width and must be matched exactly", serial, len(serial))
		}
	}
	if isSuffixMatchableSerial("") {
		t.Error("an empty serial must never be suffix-matchable")
	}
}

// The floor must sit at the low-word width, not below it. Pinned because the
// account-scoped sibling applies the same number to the STORED value, and the
// two are only safe while they agree.
func TestSuffixFloorIsTheLowWordWidth(t *testing.T) {
	if minSuffixSerialLen != 8 {
		t.Fatalf("minSuffixSerialLen is %d; the account-scoped lookup bounds its own "+
			"fallback at 8, and the two must agree", minSuffixSerialLen)
	}
}
