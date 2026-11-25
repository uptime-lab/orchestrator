package refresh_token

import (
	"time"

	"github.com/google/uuid"
)

// Session is one login, as a user sees it in "where am I signed in".
//
// A session IS a token family: a login starts a family and every rotation stays
// inside it, so the family — not the individual token — is the unit a person
// recognises ("my laptop") and the unit revocation has always operated on.
// Without this view a user has no way to see the sessions they hold: an account
// can accumulate dozens of live refresh tokens with nothing able to list or
// count them.
type Session struct {
	FamilyID uuid.UUID
	// UserAgent and IP are recorded at login. Both are self-reported by the
	// client and are identification hints for a human, never an authorization
	// input.
	UserAgent string
	IP        string
	// CreatedAt is when the login happened; LastUsedAt is the most recent
	// rotation, which is what makes a stale session recognisable.
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
	// Current marks the session making the request, so the UI can label it and
	// refuse to let someone sign themselves out by accident.
	Current bool
}

// Device carries the request-side identification captured at login. Truncated
// on the way in: a User-Agent is attacker-controlled free text, and nothing
// downstream should have to defend against a megabyte of it.
type Device struct {
	UserAgent string
	IP        string
}

// maxUserAgent bounds the stored string. Real agents are well under 256 bytes.
const maxUserAgent = 255

// maxIP bounds the stored address, generously enough for IPv6 with a port.
const maxIP = 64

// NewDevice normalizes request-side identification for storage.
func NewDevice(userAgent, ip string) Device {
	return Device{
		UserAgent: truncate(userAgent, maxUserAgent),
		IP:        truncate(ip, maxIP),
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
