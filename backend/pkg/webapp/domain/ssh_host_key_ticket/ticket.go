// Package ssh_host_key_ticket issues and verifies the short-lived proof that a
// preflight handed an operator: "THIS user, in THIS account, saw THIS host key
// on THIS host:port a few minutes ago". An adoption presents the ticket back
// and connects strictly against the key inside it, so the platform never
// accepts a host key on first use during an adoption — the request that
// offers SSH and sudo credentials to a device.
//
// The ticket is an HMAC-SHA256 over a canonical encoding of the binding, keyed
// by a per-process random key. Nothing is stored: a restart invalidates every
// outstanding ticket, which is fine because the UI re-runs the preflight
// immediately before adopting, and an API client that lost its ticket runs the
// preflight again. Storing the key would only make a stolen ticket last longer.
package ssh_host_key_ticket

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// KeyBytes is the HMAC key length. 32 bytes matches SHA-256's output.
	KeyBytes = 32
	// DefaultTTL bounds how long a preflight's proof stays usable. Long enough
	// for an operator to read the result and fill in the node details; short
	// enough that a ticket lifted from a browser tab is stale by the time it
	// could be reused.
	DefaultTTL = 15 * time.Minute

	version   = "v1"
	fieldSep  = "\n"
	fieldsLen = 8
)

// Binding is everything a ticket vouches for. Every field is compared on
// verification; a request that differs in any one of them is not the request
// the preflight ran for.
type Binding struct {
	AccountID uuid.UUID
	UserID    uuid.UUID
	Host      string
	Port      int
	// DialAddr is the vetted IP the allowlist resolved the host to, or "" when
	// no allowlist is configured and the name itself is dialed. Binding it
	// means a name that resolves elsewhere at adopt time fails the ticket
	// rather than carrying the preflight's trust to a different address.
	DialAddr           string
	HostKeyFingerprint string
}

// Refusal names why a ticket did not verify. The Reason is operator-safe: it
// carries no key material and no ticket bytes.
type Refusal struct {
	Reason string
}

func (r *Refusal) Error() string { return r.Reason }

// ErrMalformed is returned for a token that is not a ticket at all.
var ErrMalformed = &Refusal{Reason: "the host key ticket is not one this server issued"}

// Issuer mints and verifies tickets with one key.
type Issuer struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

// NewIssuer builds an issuer over an explicit key; the key must be KeyBytes
// long. Tests use it to share a key or to hold a different one.
func NewIssuer(key []byte, ttl time.Duration) (*Issuer, error) {
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("ssh_host_key_ticket: key must be %d bytes, got %d", KeyBytes, len(key))
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Issuer{key: append([]byte(nil), key...), ttl: ttl, now: time.Now}, nil
}

// NewRandomIssuer builds an issuer over a fresh random key: the production
// constructor. A failure to read randomness is not recoverable by retrying.
func NewRandomIssuer(ttl time.Duration) (*Issuer, error) {
	key := make([]byte, KeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("ssh_host_key_ticket: generate key: %w", err)
	}
	return NewIssuer(key, ttl)
}

// WithClock substitutes the time source (tests).
func (i *Issuer) WithClock(now func() time.Time) *Issuer {
	i.now = now
	return i
}

// TTL is how long a freshly issued ticket stays valid.
func (i *Issuer) TTL() time.Duration { return i.ttl }

// Issue mints a ticket for the binding, returning the token and its expiry.
func (i *Issuer) Issue(b Binding) (token string, expiresAt time.Time, err error) {
	if b.HostKeyFingerprint == "" {
		return "", time.Time{}, errors.New("ssh_host_key_ticket: a ticket needs the observed fingerprint")
	}
	if strings.ContainsAny(b.Host+b.DialAddr+b.HostKeyFingerprint, fieldSep) {
		return "", time.Time{}, errors.New("ssh_host_key_ticket: binding fields must not contain newlines")
	}
	expiresAt = i.now().Add(i.ttl).Truncate(time.Second)
	payload := encode(b, expiresAt)
	mac := i.mac(payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(mac), expiresAt, nil
}

// Verify checks the token against the binding the ADOPT request describes
// (everything but the fingerprint, which the ticket supplies) and returns the
// fingerprint the preflight observed. Any difference, an expired ticket, a
// bad MAC or a malformed token answers a *Refusal.
func (i *Issuer) Verify(token string, b Binding) (string, error) {
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return "", ErrMalformed
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(token[:dot])
	if err != nil {
		return "", ErrMalformed
	}
	mac, err := enc.DecodeString(token[dot+1:])
	if err != nil {
		return "", ErrMalformed
	}
	// The MAC is checked FIRST and in constant time, before any field is read:
	// a forged payload learns nothing from which field the comparison reached.
	if !hmac.Equal(mac, i.mac(string(payload))) {
		return "", ErrMalformed
	}
	fields := strings.Split(string(payload), fieldSep)
	if len(fields) != fieldsLen || fields[0] != version {
		return "", ErrMalformed
	}
	expUnix, err := strconv.ParseInt(fields[7], 10, 64)
	if err != nil {
		return "", ErrMalformed
	}
	if !i.now().Before(time.Unix(expUnix, 0)) {
		return "", &Refusal{Reason: "the connection test is more than " + durationWords(i.ttl) + " old"}
	}
	switch {
	case fields[1] != b.AccountID.String():
		return "", &Refusal{Reason: "the connection test was run in a different account"}
	case fields[2] != b.UserID.String():
		return "", &Refusal{Reason: "the connection test was run by a different user"}
	case fields[3] != b.Host:
		return "", &Refusal{Reason: "the connection test was run against a different host"}
	case fields[4] != strconv.Itoa(b.Port):
		return "", &Refusal{Reason: "the connection test was run against a different port"}
	case fields[5] != b.DialAddr:
		return "", &Refusal{Reason: "the host now resolves to a different address than it did during the connection test"}
	}
	fp := fields[6]
	if fp == "" {
		return "", ErrMalformed
	}
	return fp, nil
}

func encode(b Binding, expiresAt time.Time) string {
	return strings.Join([]string{
		version,
		b.AccountID.String(),
		b.UserID.String(),
		b.Host,
		strconv.Itoa(b.Port),
		b.DialAddr,
		b.HostKeyFingerprint,
		strconv.FormatInt(expiresAt.Unix(), 10),
	}, fieldSep)
}

func (i *Issuer) mac(payload string) []byte {
	h := hmac.New(sha256.New, i.key)
	h.Write([]byte(payload))
	return h.Sum(nil)
}

// durationWords renders the TTL for an operator ("15 minutes"), avoiding
// Go's "15m0s".
func durationWords(d time.Duration) string {
	if d%time.Minute == 0 && d >= time.Minute {
		m := int(d / time.Minute)
		if m == 1 {
			return "1 minute"
		}
		return strconv.Itoa(m) + " minutes"
	}
	s := int(d / time.Second)
	if s == 1 {
		return "1 second"
	}
	return strconv.Itoa(s) + " seconds"
}
