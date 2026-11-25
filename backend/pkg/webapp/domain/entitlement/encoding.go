package entitlement

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The paste form.
//
//	entl1.<payload>.<signature>
//
// `payload` is the document's JSON, as marshalled by the signer, base64url
// without padding; `signature` is base64url without padding, over
// SigningPrefix followed by the payload bytes, made by one of the two key
// types the trust store accepts: Ed25519 (64 bytes over the message itself)
// or ECDSA P-384 (ASN.1 DER, as ecdsa.SignASN1 produces, over the SHA-384 of
// the message — the scheme the agent artifacts are signed with, because the
// production key lives on a PIV token whose firmware has no Ed25519). The
// paste form does not name the algorithm: the verifier tries each trusted
// key with that key's own algorithm. The prefix binds
// the signature to this format and version, so a signature made over some
// other JSON that happens to parse as a document cannot be presented here.
//
// Verification runs over the payload bytes exactly as received and only then
// parses them, so the verifier has no canonicalisation step to get wrong; the
// signer is the only party that has to produce canonical bytes, and it does so
// by marshalling the Document struct in its declared field order.
//
// Whitespace anywhere in a pasted value is ignored: a file is often copied out
// of an e-mail or a terminal that wrapped it.
const (
	FormPrefix    = "entl1"
	SigningPrefix = "orchestrator-entitlement/1\n"
	// MaxFormLength bounds what a paste may carry. The largest legitimate
	// document is a few hundred bytes; the bound exists so the parse never
	// works on an arbitrary upload.
	MaxFormLength = 8 * 1024
	// maxECDSASignatureSize is the longest ASN.1 DER ECDSA P-384 signature:
	// a SEQUENCE header (2) and two INTEGERs of at most 2+49 bytes each.
	maxECDSASignatureSize = 104
	// minECDSASignatureSize refuses a DER blob too short to hold two
	// INTEGERs before any key is tried.
	minECDSASignatureSize = 8
)

// Parse and verification sentinels. They are deliberately distinct from the
// Validate sentinels: "this is not an entitlement at all" and "this is a
// well-signed entitlement with a bad field" are different findings.
var (
	ErrFormTooLong      = errors.New("entitlement: the pasted value is too long")
	ErrFormMalformed    = errors.New("entitlement: the pasted value is not an entitlement file")
	ErrFormVersion      = errors.New("entitlement: the pasted value is not a version this install understands")
	ErrSignatureInvalid = errors.New("entitlement: the signature does not verify against this install's trusted keys")
	ErrNoTrustedKeys    = errors.New("entitlement: no trusted signing key is available")
)

var b64 = base64.RawURLEncoding

// Encode marshals the document into its canonical payload bytes. Exported for
// the signer's benefit and for tests; a verifier never calls it.
func Encode(doc Document) ([]byte, error) {
	doc.IssuedAt = doc.IssuedAt.UTC().Truncate(1e9)
	doc.ExpiresAt = doc.ExpiresAt.UTC().Truncate(1e9)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// The default encoder escapes <, > and & for HTML embedding; the note is
	// free text and a verifier must see the same bytes the signer produced, so
	// escaping is off on both sides by construction (the signer tool uses the
	// same setting).
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Sign produces the paste form of a document with the given private key: an
// ed25519.PrivateKey, an *ecdsa.PrivateKey on P-384, or any crypto.Signer
// whose public half is one of those (a token-held key). The document is
// validated first: a signer must not be able to issue a file the verifier
// would refuse on arrival.
func Sign(doc Document, priv crypto.Signer) (string, error) {
	if err := doc.Validate(); err != nil {
		return "", err
	}
	payload, err := Encode(doc)
	if err != nil {
		return "", err
	}
	sig, err := signMessage(priv, signingInput(payload))
	if err != nil {
		return "", err
	}
	return FormPrefix + "." + b64.EncodeToString(payload) + "." + b64.EncodeToString(sig), nil
}

// signMessage signs the prefixed message with the algorithm the key's public
// half selects, refusing any key the trust store would not load.
func signMessage(priv crypto.Signer, msg []byte) ([]byte, error) {
	pub, err := acceptedKey(priv.Public())
	if err != nil {
		return nil, err
	}
	if _, ok := pub.(ed25519.PublicKey); ok {
		return priv.Sign(rand.Reader, msg, crypto.Hash(0))
	}
	// *ecdsa.PublicKey on P-384, by acceptedKey.
	digest := sha512.Sum384(msg)
	return priv.Sign(rand.Reader, digest[:], crypto.SHA384)
}

// verifyOne checks one signature against one trusted key with that key's
// algorithm.
func verifyOne(key crypto.PublicKey, msg, sig []byte) bool {
	switch k := key.(type) {
	case ed25519.PublicKey:
		return len(sig) == ed25519.SignatureSize && ed25519.Verify(k, msg, sig)
	case *ecdsa.PublicKey:
		digest := sha512.Sum384(msg)
		return ecdsa.VerifyASN1(k, digest[:], sig)
	default:
		return false
	}
}

// plausibleSignature is the shape check Parse applies before any key is
// tried: an Ed25519 signature's exact size, or a DER SEQUENCE no longer than
// a P-384 signature can be.
func plausibleSignature(sig []byte) bool {
	if len(sig) == ed25519.SignatureSize {
		return true
	}
	return len(sig) >= minECDSASignatureSize && len(sig) <= maxECDSASignatureSize && sig[0] == 0x30
}

func signingInput(payload []byte) []byte {
	in := make([]byte, 0, len(SigningPrefix)+len(payload))
	in = append(in, SigningPrefix...)
	return append(in, payload...)
}

// Parsed is a paste form split into its parts, before any signature check.
type Parsed struct {
	Payload   []byte
	Signature []byte
}

// Parse splits a paste form into payload and signature without verifying
// anything. The split is strict about shape and lenient about whitespace.
func Parse(form string) (Parsed, error) {
	if len(form) > MaxFormLength {
		return Parsed{}, ErrFormTooLong
	}
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		}
		return r
	}, form)
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return Parsed{}, ErrFormMalformed
	}
	if parts[0] != FormPrefix {
		if strings.HasPrefix(parts[0], "entl") {
			return Parsed{}, ErrFormVersion
		}
		return Parsed{}, ErrFormMalformed
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil || len(payload) == 0 {
		return Parsed{}, ErrFormMalformed
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !plausibleSignature(sig) {
		return Parsed{}, ErrFormMalformed
	}
	return Parsed{Payload: payload, Signature: sig}, nil
}

// VerifyWith parses a paste form, checks its signature against the given
// keys (any one of them suffices, which is what allows a signing key to be
// rotated without invalidating files signed by its predecessor), and returns
// the validated document.
//
// The signature is checked BEFORE the payload is parsed: an unsigned payload
// is never interpreted, so a malformed field inside a forged document reports
// as a bad signature, not as a malformed field.
func VerifyWith(form string, keys []crypto.PublicKey) (*Document, error) {
	if len(keys) == 0 {
		return nil, ErrNoTrustedKeys
	}
	parsed, err := Parse(form)
	if err != nil {
		return nil, err
	}
	input := signingInput(parsed.Payload)
	verified := false
	for _, key := range keys {
		if verifyOne(key, input, parsed.Signature) {
			verified = true
			break
		}
	}
	if !verified {
		return nil, ErrSignatureInvalid
	}
	var doc Document
	if err := json.Unmarshal(parsed.Payload, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFormMalformed, err)
	}
	if err := doc.Validate(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// Verify checks a paste form against the keys embedded in this build.
func Verify(form string) (*Document, error) {
	return VerifyWith(form, TrustedKeys())
}
