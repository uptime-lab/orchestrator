package crypto

import (
	"strings"
	"testing"
)

// Reference vectors, and where they came from matters more than their content.
//
// They were produced by `openssl passwd -6 -salt <salt> <password>` on a
// Linux host (OpenSSL 3.0.13 on Ubuntu arm64) — a real Linux
// implementation of the same crypt(3) the target device's PAM will use to
// VERIFY these hashes. That is the property under test, and it is not the same
// as "matches a spec document": a hash that is subtly wrong does not fail at
// write time, it fails at the next login on a remote device that may by then
// have no other way in.
//
// The first two are also the published SHA-crypt specification vectors, so the
// two sources agree. Two further vectors recalled from that document did NOT
// agree and were dropped rather than chased: the recollection was the doubtful
// party, not an implementation that reproduces OpenSSL byte for byte.
//
// Regenerate with the same command if a vector ever needs to change.
func TestSHA512CryptMatchesLinuxCrypt(t *testing.T) {
	cases := []struct {
		name           string
		password       string
		salt           string
		rounds         int
		explicitRounds bool
		want           string
	}{
		{
			name:     "the specification's own vector",
			password: "Hello world!",
			salt:     "saltstring",
			rounds:   defaultRounds,
			want:     "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1",
		},
		{
			name:     "a full-length 16-character salt",
			password: "hunter2",
			salt:     "0123456789abcdef",
			rounds:   defaultRounds,
			want:     "$6$0123456789abcdef$GrCa1cuN5Plxg4bUmR0cJ9ohuWHAeWGcvZKD2crRyMGZbkg3t90kKEdI82BGc7AiTt5KY9TA0pFYP2h0miibi1",
		},
		{
			name:     "spaces in the password",
			password: "This is just a test",
			salt:     "toolongsaltstrin",
			rounds:   defaultRounds,
			want:     "$6$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQzQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0",
		},
		{
			name: "a password longer than the digest block",
			// Length matters here: |password| drives both the B-repetition and
			// the bit loop, so a password over 64 bytes exercises the wrapping
			// that a short one never reaches.
			password: "a very much longer text to encrypt.  This one even stretches over morethan one line.",
			salt:     "anotherlongsalts",
			rounds:   defaultRounds,
			want:     "$6$anotherlongsalts$zCB2J77iwc/56nB80mcnR6gCDELuiqcwDzPCm3OZnzRQyxT9pVMJ2vfOf0YI0AvrvfVu.AqASga4nxwhEPO7Z0",
		},
		{
			name: "a non-ASCII password",
			// A user's password reaches us as a UTF-8 Go string and must be
			// hashed as its BYTES, which is what the device will present at
			// login. Hashing runes, or normalising, would lock the user out.
			password: "пароль-юникод",
			salt:     "unicodesalt12345",
			rounds:   defaultRounds,
			want:     "$6$unicodesalt12345$49GcGmAzMXGlYmUmVLBYtLX4SWYPPYxAJBynibFl3GRY.qYEgYkMjXGzLWPcyM/C3kJ2VKHsVrkpd9TZGqn/21",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sha512Crypt(tc.password, tc.salt, tc.rounds, tc.explicitRounds)
			if got != tc.want {
				t.Errorf("sha512Crypt mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// The salt cap is the specification's, and it is load-bearing: a caller that
// passes something longer must get the same answer the device would compute,
// which is the hash of the TRUNCATED salt.
func TestSaltIsTruncatedAtSixteen(t *testing.T) {
	long := sha512Crypt("Hello world!", "saltstringsaltstringsaltstring", defaultRounds, false)
	short := sha512Crypt("Hello world!", "saltstringsaltst", defaultRounds, false)
	if long != short {
		t.Errorf("an over-long salt was not truncated to %d characters:\n %s\n %s", maxSaltLen, long, short)
	}
}

// The exported entry point must produce something chpasswd -e will accept: the
// $6$ marker, a full-length salt, and an 86-character digest.
func TestHashPasswordForShadowShape(t *testing.T) {
	hash, err := HashPasswordForShadow("hunter2")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	parts := strings.Split(hash, "$")
	// "", "6", salt, digest
	if len(parts) != 4 || parts[1] != "6" {
		t.Fatalf("not a $6$ hash: %q", hash)
	}
	if len(parts[2]) != maxSaltLen {
		t.Errorf("salt is %d characters, expected %d: %q", len(parts[2]), maxSaltLen, parts[2])
	}
	if len(parts[3]) != 86 {
		t.Errorf("digest is %d characters, expected 86: %q", len(parts[3]), parts[3])
	}
	for _, r := range parts[2] + parts[3] {
		if !strings.ContainsRune(saltAlphabet, r) {
			t.Fatalf("character %q is outside the crypt alphabet, so the hash is unparseable on the device", r)
		}
	}
	// The point of the whole exercise: the cleartext must not be recoverable
	// from, or even present in, what travels.
	if strings.Contains(hash, "hunter2") {
		t.Error("the hash contains the cleartext password")
	}
}

// Two hashes of the same password must differ, or the salt is not doing its job
// and one precomputed table covers the whole fleet.
func TestHashPasswordForShadowIsSalted(t *testing.T) {
	first, err := HashPasswordForShadow("same password")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	second, err := HashPasswordForShadow("same password")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	if first == second {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

// An empty password must not silently produce a valid-looking hash that grants
// a passwordless login. The caller decides to CLEAR a password through the
// password action; it must never arrive here as an empty string.
func TestEmptyPasswordStillHashes(t *testing.T) {
	// It does hash — crypt(3) is defined for it — and that is precisely why the
	// call site must not treat "" as "no password". This test pins the fact so a
	// future reader does not assume the primitive guards it.
	hash, err := HashPasswordForShadow("")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	if !strings.HasPrefix(hash, sha512CryptPrefix) {
		t.Errorf("expected a $6$ hash even for an empty password, got %q", hash)
	}
}
