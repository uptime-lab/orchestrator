package signing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
)

// Verification of a detached agent-artifact signature on the SERVER side.
//
// The backend used to only serve signatures and bake its key set into
// install.sh; the device and the script did the verifying. The release import
// (`server --import-agent-release`) has to refuse an unsigned or foreign
// package BEFORE it lands on the CDN, so the server now verifies too. This is
// a MIRROR of agent/pkg/signing (verifyWithKeys: ASN.1-DER ECDSA over
// SHA-384, accepted iff it matches ANY trusted key), not a second design: the
// agent is a separate Go module and cannot be imported from here. Change the
// two together — a server that accepts what the agent refuses publishes a
// package every update_agent then fails on, and the reverse refuses a
// release every device would have installed.

// ErrUntrusted means the signature did not match any trusted key. Same text
// as the agent's sentinel so an operator reads one message for one fact.
var ErrUntrusted = errors.New("agent artifact signature does not match any trusted key")

// ErrNotP384 means a trusted key is ECDSA on a curve other than P-384. Same
// rule and text as the agent's: the scheme is ECDSA P-384 over SHA-384, and a
// key on another curve is refused rather than trusted.
var ErrNotP384 = errors.New("trusted signing key is not ECDSA P-384")

// requireP384 refuses a key whose curve is not P-384.
func requireP384(k *ecdsa.PublicKey) error {
	if k == nil || k.Curve != elliptic.P384() {
		return ErrNotP384
	}
	return nil
}

// TrustedKey is one parsed public key with the identity an operator can
// compare against the PEM files: the file it came from and the SHA-256 of its
// SubjectPublicKeyInfo DER (`openssl pkey -pubin -in K.pem -outform DER |
// sha256sum`).
type TrustedKey struct {
	Name        string
	Fingerprint string
	Key         *ecdsa.PublicKey
}

// ID is the short form printed in summaries: file name plus the first 16 hex
// characters of the SPKI fingerprint.
func (k TrustedKey) ID() string {
	fp := k.Fingerprint
	if len(fp) > 16 {
		fp = fp[:16]
	}
	return k.Name + ":" + fp
}

// ParsePublicKeyPEM parses one PEM-encoded ECDSA P-384 public key.
func ParsePublicKeyPEM(name string, data []byte) (TrustedKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return TrustedKey{}, fmt.Errorf("no PEM block in %s", name)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return TrustedKey{}, fmt.Errorf("parse key %s: %w", name, err)
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return TrustedKey{}, fmt.Errorf("key %s is not ECDSA", name)
	}
	if err := requireP384(ec); err != nil {
		return TrustedKey{}, fmt.Errorf("key %s: %w", name, err)
	}
	sum := sha256.Sum256(block.Bytes)
	return TrustedKey{Name: name, Fingerprint: hex.EncodeToString(sum[:]), Key: ec}, nil
}

// TrustedKeys parses every embedded trusted public key, in file-name order.
// An empty or malformed set is a build mistake and is an error (fail-closed),
// exactly as in the agent.
func TrustedKeys() ([]TrustedKey, error) {
	entries, err := keyFS.ReadDir("keys")
	if err != nil {
		return nil, fmt.Errorf("read embedded keys: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	keys := make([]TrustedKey, 0, len(names))
	for _, n := range names {
		b, err := keyFS.ReadFile("keys/" + n)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", n, err)
		}
		k, err := ParsePublicKeyPEM(n, b)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, errors.New("no trusted signing keys embedded")
	}
	return keys, nil
}

// VerifyDetached reports which trusted key signed data. Any non-nil error
// means "do not publish" (fail-closed).
func VerifyDetached(keys []TrustedKey, data, sig []byte) (TrustedKey, error) {
	return VerifyDigest(keys, sha512.Sum384(data), sig)
}

// VerifyDigest is VerifyDetached for a caller that hashed the artifact while
// streaming it (the import copies a package once and hashes that copy, so the
// bytes verified are the bytes stored).
func VerifyDigest(keys []TrustedKey, digest [sha512.Size384]byte, sig []byte) (TrustedKey, error) {
	if len(keys) == 0 {
		return TrustedKey{}, errors.New("no trusted signing keys")
	}
	if len(sig) == 0 {
		return TrustedKey{}, errors.New("empty signature")
	}
	for _, k := range keys {
		if err := requireP384(k.Key); err != nil {
			return TrustedKey{}, fmt.Errorf("key %s: %w", k.Name, err)
		}
	}
	for _, k := range keys {
		if ecdsa.VerifyASN1(k.Key, digest[:], sig) {
			return k, nil
		}
	}
	return TrustedKey{}, ErrUntrusted
}
