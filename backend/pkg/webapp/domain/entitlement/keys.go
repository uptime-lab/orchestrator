package entitlement

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io/fs"
	"sort"
	"sync"
)

// The trusted entitlement-signing public keys, embedded at build time. The
// private halves live outside every repository, with the person who issues
// entitlements. Nothing here is secret, and a key is added by dropping a PEM
// into the directory: every key present is trusted, which is what lets a key
// be rotated by shipping the successor beside its predecessor for a release.
//
//go:embed keys/*.pem
var keyFS embed.FS

// ParsePublicKeysPEM extracts every trusted public key from PEM text. Two key
// types are accepted, each verified by its own algorithm (verifyOne):
// Ed25519, and ECDSA on P-384 — the production key is held on a PIV token
// whose firmware has no Ed25519. A block of any other type or curve is an
// error rather than skipped: a trust store that silently ignores a mis-typed
// key is one that looks populated while verifying nothing.
func ParsePublicKeysPEM(data []byte) ([]crypto.PublicKey, error) {
	var keys []crypto.PublicKey
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("entitlement: unexpected PEM block %q", block.Type)
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("entitlement: parsing public key: %w", err)
		}
		key, err := acceptedKey(parsed)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("entitlement: no public key in PEM data")
	}
	return keys, nil
}

// acceptedKey admits a parsed PKIX key only if it is one of the two types
// the verifier implements. Anything else is refused at load, never at
// verification, so a wrong key file is a boot-time ERROR naming the file,
// not a silent rejection of every entitlement.
func acceptedKey(parsed any) (crypto.PublicKey, error) {
	switch k := parsed.(type) {
	case ed25519.PublicKey:
		return k, nil
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P384() {
			return nil, fmt.Errorf("entitlement: ECDSA public key is on %s, want P-384", k.Curve.Params().Name)
		}
		return k, nil
	default:
		return nil, fmt.Errorf("entitlement: public key is %T, want Ed25519 or ECDSA P-384", parsed)
	}
}

// DevelopmentKeyFingerprint is the SHA-256, in hex, of the raw Ed25519
// public key of the DEVELOPMENT signing pair the entitlement was built and
// tested with. A release embeds the production key, whose private half is
// held on the issuer's hardware token; a build that embeds this one says so
// at boot (EmbedsDevelopmentKey), so it cannot pass for a release unnoticed.
// A copy of the key stays in testdata/ so this constant remains checkable
// once keys/ no longer carries it.
const DevelopmentKeyFingerprint = "7398e8668a3576df24a062ec960bdc128960ff17ac27c313c6b2b368a02c7cc9"

// KeyFingerprint is the SHA-256, in hex, of a raw Ed25519 public key, or of
// the PKIX DER encoding of any other key. The Ed25519 form is the one
// DevelopmentKeyFingerprint was computed over and must not change.
func KeyFingerprint(k crypto.PublicKey) string {
	var data []byte
	switch key := k.(type) {
	case ed25519.PublicKey:
		data = key
	default:
		der, err := x509.MarshalPKIXPublicKey(k)
		if err != nil {
			return ""
		}
		data = der
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// EmbedsDevelopmentKey reports whether this build trusts the development key.
func EmbedsDevelopmentKey() bool {
	return containsDevelopmentKey(TrustedKeys())
}

func containsDevelopmentKey(keys []crypto.PublicKey) bool {
	for _, k := range keys {
		if KeyFingerprint(k) == DevelopmentKeyFingerprint {
			return true
		}
	}
	return false
}

var (
	trustedOnce sync.Once
	trustedKeys []crypto.PublicKey
	trustedErr  error
)

// TrustedKeys returns the embedded keys, parsed once. An embed set that fails
// to parse is a build defect; it is reported through TrustedKeysError and the
// result is an EMPTY set, so verification fails closed rather than panicking
// on every admission.
func TrustedKeys() []crypto.PublicKey {
	loadTrusted()
	return trustedKeys
}

// TrustedKeysError reports why the embedded set could not be loaded, or nil.
// The composition root logs it at boot so the defect is visible before the
// first operator pastes a file.
func TrustedKeysError() error {
	loadTrusted()
	return trustedErr
}

func loadTrusted() {
	trustedOnce.Do(func() {
		trustedKeys, trustedErr = loadKeyDir(keyFS, "keys")
	})
}

// loadKeyDir parses every file in dir ON ITS OWN, in name order, and drops
// the WHOLE set when one file fails: a key file that does not parse (a wrong
// key type, a P-256 key, a stray block) names itself in the error and the
// install trusts no key at all, so the defect is a boot-time ERROR rather than
// a trust store that quietly carries one key fewer than the build intended.
func loadKeyDir(fsys fs.FS, dir string) ([]crypto.PublicKey, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("entitlement: reading embedded keys: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var keys []crypto.PublicKey
	for _, name := range names {
		data, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("entitlement: reading embedded key %s: %w", name, err)
		}
		parsed, err := ParsePublicKeysPEM(data)
		if err != nil {
			return nil, fmt.Errorf("entitlement: embedded key %s: %w", name, err)
		}
		keys = append(keys, parsed...)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("entitlement: the embedded key set is empty")
	}
	return keys, nil
}
