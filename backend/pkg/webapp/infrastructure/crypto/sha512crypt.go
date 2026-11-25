package crypto

import (
	"crypto/rand"
	"crypto/sha512"
	"fmt"
	"strings"
)

// SHA-512 crypt(3) — the `$6$` format Linux keeps in /etc/shadow.
//
// This exists so a password never has to travel to a device in cleartext. The
// platform resolves an access entry's password server-side and sends the HASH;
// the agent feeds it to `chpasswd -e`, which writes it into /etc/shadow
// verbatim. What crosses the wire is then exactly what an attacker with root on
// that node could read anyway, instead of a credential that is also valid on
// every OTHER node in the account.
//
// Written here rather than pulled in as a dependency: the algorithm is a fixed,
// published specification (Ulrich Drepper's SHA-crypt) with public test vectors,
// it is about a hundred lines, and it will never need to change. A third-party
// package for that is supply-chain surface bought with nothing.
//
// $6$ specifically, not bcrypt or yescrypt. glibc and libxcrypt both understand
// $6$ everywhere; bcrypt ($2b$) is libxcrypt-only and yescrypt ($y$) is newer
// still, and a hash the target's PAM cannot verify does not fail at write time —
// it fails silently at the next login, on a device that may now have no other
// way in.

const (
	// sha512CryptPrefix marks the format in /etc/shadow.
	sha512CryptPrefix = "$6$"

	// defaultRounds is what shadow-utils uses when nothing says otherwise, so a
	// hash we write is indistinguishable from one the device would have written
	// for itself. Deliberately not raised: the hash's exposure is the same as
	// /etc/shadow's, and a non-default cost is a difference an operator would
	// have to explain while troubleshooting.
	defaultRounds = 5000

	minRounds = 1000
	maxRounds = 999999999

	// maxSaltLen is the specification's cap. Longer salts are truncated, which
	// is why the test vectors with over-long salts still verify.
	maxSaltLen = 16

	// saltAlphabet is the crypt(3) alphabet, also used for the digest encoding.
	saltAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// HashPasswordForShadow returns a `$6$` crypt(3) hash of password with a fresh
// random salt, ready to hand to `chpasswd -e`.
//
// A random salt per call is deliberate and safe here: the access config's hash
// mixes in only whether a user HAS a password, never its value, so a different
// hash each time does not make the desired state look changed and does not
// re-trigger the applied-generation self-heal.
func HashPasswordForShadow(password string) (string, error) {
	salt, err := randomSalt()
	if err != nil {
		return "", err
	}
	return sha512Crypt(password, salt, defaultRounds, false), nil
}

// randomSalt draws maxSaltLen characters from the crypt alphabet.
//
// Rejection-free: 64 is a power of two and the alphabet is exactly 64
// characters, so masking six bits is unbiased.
func randomSalt() (string, error) {
	buf := make([]byte, maxSaltLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating a password salt: %w", err)
	}
	out := make([]byte, maxSaltLen)
	for i, b := range buf {
		out[i] = saltAlphabet[b&0x3f]
	}
	return string(out), nil
}

// sha512Crypt is the algorithm itself. explicitRounds controls whether the
// `rounds=` field appears in the output, which the specification omits when the
// count is the default — the test vectors depend on both shapes.
func sha512Crypt(password, salt string, rounds int, explicitRounds bool) string {
	if rounds < minRounds {
		rounds = minRounds
	}
	if rounds > maxRounds {
		rounds = maxRounds
	}
	if len(salt) > maxSaltLen {
		salt = salt[:maxSaltLen]
	}

	pw := []byte(password)
	sl := []byte(salt)

	// Digest B: password, salt, password.
	b := sha512.New()
	b.Write(pw)
	b.Write(sl)
	b.Write(pw)
	digestB := b.Sum(nil)

	// Digest A: password, salt, then |password| bytes of B, then one pass over
	// the bits of |password| adding B for a 1 and the password for a 0.
	a := sha512.New()
	a.Write(pw)
	a.Write(sl)
	writeRepeated(a, digestB, len(pw))
	for cnt := len(pw); cnt > 0; cnt >>= 1 {
		if cnt&1 != 0 {
			a.Write(digestB)
		} else {
			a.Write(pw)
		}
	}
	digestA := a.Sum(nil)

	// Sequence P: |password| bytes derived from the password alone.
	dp := sha512.New()
	for i := 0; i < len(pw); i++ {
		dp.Write(pw)
	}
	seqP := repeatTo(dp.Sum(nil), len(pw))

	// Sequence S: |salt| bytes derived from the salt, repeated a count that
	// depends on the first byte of A.
	ds := sha512.New()
	for i := 0; i < 16+int(digestA[0]); i++ {
		ds.Write(sl)
	}
	seqS := repeatTo(ds.Sum(nil), len(sl))

	// The stretching loop. This is the whole cost of the function.
	cur := digestA
	for i := 0; i < rounds; i++ {
		c := sha512.New()
		if i&1 != 0 {
			c.Write(seqP)
		} else {
			c.Write(cur)
		}
		if i%3 != 0 {
			c.Write(seqS)
		}
		if i%7 != 0 {
			c.Write(seqP)
		}
		if i&1 != 0 {
			c.Write(cur)
		} else {
			c.Write(seqP)
		}
		cur = c.Sum(nil)
	}

	var out strings.Builder
	out.WriteString(sha512CryptPrefix)
	if explicitRounds {
		fmt.Fprintf(&out, "rounds=%d$", rounds)
	}
	out.WriteString(salt)
	out.WriteByte('$')
	out.WriteString(encodeShadowB64(cur))
	return out.String()
}

// writeRepeated writes n bytes of block, cycling through it as needed.
func writeRepeated(h interface{ Write([]byte) (int, error) }, block []byte, n int) {
	for n > len(block) {
		h.Write(block)
		n -= len(block)
	}
	h.Write(block[:n])
}

// repeatTo builds n bytes by cycling through block.
func repeatTo(block []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		take := n - len(out)
		if take > len(block) {
			take = len(block)
		}
		out = append(out, block[:take]...)
	}
	return out
}

// encodeShadowB64 renders the 64-byte digest in crypt(3)'s base64, which is
// neither RFC 4648 nor applied in byte order.
//
// The byte triples are read in a fixed permutation: starting at 0 and stepping
// by 22 (mod 63), each group takes (i, i+21, i+42) mod 63. Generated rather
// than transcribed, because the transcribed table in glibc is 22 lines of
// hand-written indices and a typo in one of them produces a hash that is wrong
// only for some inputs.
func encodeShadowB64(digest []byte) string {
	var out strings.Builder
	i := 0
	for group := 0; group < 21; group++ {
		out.WriteString(b64From24Bit(digest[i], digest[(i+21)%63], digest[(i+42)%63], 4))
		i = (i + 22) % 63
	}
	// The 64th byte is left over and encodes to two characters on its own.
	out.WriteString(b64From24Bit(0, 0, digest[63], 2))
	return out.String()
}

// b64From24Bit emits n characters of the little-endian 24-bit value b2b1b0.
func b64From24Bit(b2, b1, b0 byte, n int) string {
	w := uint32(b2)<<16 | uint32(b1)<<8 | uint32(b0)
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, saltAlphabet[w&0x3f])
		w >>= 6
	}
	return string(out)
}
