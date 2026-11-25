// Package cdnsign provides HMAC-signed, expiring URLs for the local CDN so that
// serving a file requires a capability token rather than mere knowledge of the
// path. It is used by the image service (to mint URLs) and the CDN handler (to
// verify them). Transport stays plain HTTP — the signature travels in the query
// string, which is compatible with clients that cannot use HTTPS.
package cdnsign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"time"
)

// Query parameter names for the expiry and signature.
const (
	ParamExpires   = "exp"
	ParamSignature = "sig"
)

// ErrInvalidSignature is returned when the signature is missing, malformed,
// expired, or does not match.
var ErrInvalidSignature = errors.New("invalid or expired CDN signature")

// DeriveKey derives the CDN URL signing key from a base secret (the JWT secret)
// with domain separation, so the CDN key is not the raw JWT secret.
func DeriveKey(baseSecret string) []byte {
	m := hmac.New(sha256.New, []byte(baseSecret))
	m.Write([]byte("orchestrator-cdn-url-signing-v1"))
	return m.Sum(nil)
}

// computeSignature is the HMAC over the storage path bound to its expiry.
func computeSignature(key []byte, name string, exp int64) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(name))
	m.Write([]byte{'\n'})
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// Sign returns the (expires, signature) pair for name with the given TTL, for
// callers that pass the capability as separate values (e.g. command params)
// rather than baked into a URL. Verify with the same name.
func Sign(name string, key []byte, ttl time.Duration) (expStr, sig string) {
	exp := time.Now().Add(ttl).Unix()
	return strconv.FormatInt(exp, 10), computeSignature(key, name, exp)
}

// AppendSignature adds exp and sig query parameters to rawURL, signing over
// name (the storage-relative path the URL resolves to).
func AppendSignature(rawURL, name string, key []byte, ttl time.Duration) (string, error) {
	exp := time.Now().Add(ttl).Unix()
	sig := computeSignature(key, name, exp)

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(ParamExpires, strconv.FormatInt(exp, 10))
	q.Set(ParamSignature, sig)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Verify checks the expiry and signature for the given storage path. name must
// be the same path that was signed (the request path relative to /cdn/).
func Verify(name, expStr, sig string, key []byte) error {
	if expStr == "" || sig == "" {
		return ErrInvalidSignature
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	if time.Now().Unix() > exp {
		return ErrInvalidSignature
	}
	expected := computeSignature(key, name, exp)
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return ErrInvalidSignature
	}
	return nil
}
