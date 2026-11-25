package passkey

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func newTestService() *Service {
	return &Service{ceremonies: newCeremonyStore(ceremonyTTL)}
}

// A challenge must be usable exactly once: replaying a ceremony ticket would
// let a captured authenticator response be submitted a second time.
func TestCeremonyTicketIsSingleUse(t *testing.T) {
	store := newCeremonyStore(time.Minute)
	ticket := store.put(&webauthn.SessionData{})

	if _, ok := store.take(ticket); !ok {
		t.Fatal("the first use of a ticket must succeed")
	}
	if _, ok := store.take(ticket); ok {
		t.Error("a ticket must not be usable twice")
	}
}

func TestExpiredCeremonyTicketIsRejected(t *testing.T) {
	store := newCeremonyStore(-time.Second) // already expired on insert
	ticket := store.put(&webauthn.SessionData{})

	if _, ok := store.take(ticket); ok {
		t.Error("an expired ticket must not be accepted")
	}
}

func TestUnknownCeremonyTicketIsRejected(t *testing.T) {
	store := newCeremonyStore(time.Minute)

	if _, ok := store.take("not-a-real-ticket"); ok {
		t.Error("an unknown ticket must not be accepted")
	}
}

// The RP ID is a bare domain. Including the port would produce a relying party
// the browser never matches, and every passkey registered under it would be
// permanently unusable.
func TestRelyingPartyIDStripsThePort(t *testing.T) {
	svc := newTestService()
	req := httptest.NewRequest("POST", "/auth/passkey/begin", nil)
	req.Host = "app.example.com:8443"

	wa, err := svc.relyingParty(req)
	if err != nil {
		t.Fatalf("relyingParty: %v", err)
	}

	if wa.Config.RPID != "app.example.com" {
		t.Errorf("RPID = %q, want %q", wa.Config.RPID, "app.example.com")
	}
	// The ORIGIN, unlike the id, must keep the port.
	if len(wa.Config.RPOrigins) != 1 || wa.Config.RPOrigins[0] != "https://app.example.com:8443" {
		t.Errorf("RPOrigins = %v, want the full origin including the port", wa.Config.RPOrigins)
	}
}

// One binary serves any number of hostnames, so the relying
// party follows the request rather than a build-time constant.
func TestRelyingPartyIDFollowsTheRequestHost(t *testing.T) {
	svc := newTestService()
	for _, host := range []string{"app.example.com", "orchestrator.example.internal"} {
		req := httptest.NewRequest("POST", "/auth/passkey/begin", nil)
		req.Host = host

		wa, err := svc.relyingParty(req)
		if err != nil {
			t.Fatalf("relyingParty(%s): %v", host, err)
		}
		if wa.Config.RPID != host {
			t.Errorf("RPID = %q, want %q", wa.Config.RPID, host)
		}
	}
}

func TestRelyingPartyOverrideWins(t *testing.T) {
	svc := newTestService()
	svc.rpIDOverride = "pinned.example.com"
	req := httptest.NewRequest("POST", "/auth/passkey/begin", nil)
	req.Host = "internal-proxy.local"

	wa, err := svc.relyingParty(req)
	if err != nil {
		t.Fatalf("relyingParty: %v", err)
	}

	if wa.Config.RPID != "pinned.example.com" {
		t.Errorf("RPID = %q, want the configured override", wa.Config.RPID)
	}
}

// The origin check is WebAuthn's only anti-phishing property, so the value it
// is checked against must not come from a header the caller controls.
func TestForwardedHostHeaderIsIgnored(t *testing.T) {
	svc := newTestService()
	req := httptest.NewRequest("POST", "/auth/passkey/begin", nil)
	req.Host = "app.example.com"
	req.Header.Set("X-Forwarded-Host", "evil.example.net")
	req.Header.Set("X-Forwarded-Proto", "http")

	wa, err := svc.relyingParty(req)
	if err != nil {
		t.Fatalf("relyingParty: %v", err)
	}

	if wa.Config.RPID != "app.example.com" {
		t.Errorf("RPID = %q — a forwarding header must not steer the relying party", wa.Config.RPID)
	}
	if wa.Config.RPOrigins[0] != "https://app.example.com" {
		t.Errorf("origin = %q — the scheme must not come from a header either", wa.Config.RPOrigins[0])
	}
}

// Local development is the only case where a plaintext origin is legitimate,
// and browsers enforce that themselves.
func TestLoopbackHostUsesHTTP(t *testing.T) {
	svc := newTestService()
	req := httptest.NewRequest("POST", "/auth/passkey/begin", nil)
	req.Host = "localhost:5174"

	wa, err := svc.relyingParty(req)
	if err != nil {
		t.Fatalf("relyingParty: %v", err)
	}

	if wa.Config.RPOrigins[0] != "http://localhost:5174" {
		t.Errorf("origin = %q, want http on loopback", wa.Config.RPOrigins[0])
	}
}
