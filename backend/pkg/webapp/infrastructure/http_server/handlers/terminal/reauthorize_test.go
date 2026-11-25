package terminal

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
)

// The periodic re-check behind an open shell used to re-run only the enforcer,
// against the unit captured at the handshake. A node moved to another unit,
// a deleted node, a sign-out, "sign out everywhere" and a password change all
// left the shell open until the browser stopped answering pings.

func (hs *harness) reauthorize() error {
	return hs.h.reauthorize(context.Background(), hs.auth.claims, hs.nodeID)
}

func TestReauthorizeAllowsAnUnchangedSession(t *testing.T) {
	hs := newHarness(t)
	if err := hs.reauthorize(); err != nil {
		t.Fatalf("nothing changed, yet the tick refused: %v", err)
	}
}

// The unit is re-read every tick: a member granted unit A must lose the shell
// when the node moves to unit B.
func TestReauthorizeRefusesAfterTheNodeMovesToAnotherUnit(t *testing.T) {
	hs := newHarness(t)
	if err := hs.reauthorize(); err != nil {
		t.Fatal(err)
	}
	ouB := uuid.New()
	hs.nodes.moveTo(&ouB)
	if err := hs.reauthorize(); err == nil {
		t.Fatal("the shell survived the node moving to a unit the member was never granted")
	}
}

func TestReauthorizeRefusesOnceTheNodeIsGone(t *testing.T) {
	hs := newHarness(t)
	hs.nodes.vanish()
	if err := hs.reauthorize(); err == nil {
		t.Fatal("the shell survived the node being deleted")
	}
}

// A sign-out revokes the refresh family; the access token that opened the
// shell keeps verifying, so the tick must ask the session inventory.
func TestReauthorizeRefusesOnceTheSessionIsRevoked(t *testing.T) {
	hs := newHarness(t)
	hs.auth.live.Store(false)
	if err := hs.reauthorize(); err == nil {
		t.Fatal("the shell survived its session being signed out")
	}
}

func TestReauthorizeRefusesARemovedMember(t *testing.T) {
	hs := newHarness(t)
	hs.h.enforcer = authztest.NonMember()
	if err := hs.reauthorize(); err == nil {
		t.Fatal("the shell survived the member being removed")
	}
}

// The access token EXPIRING is not a reason to end the shell: the browser
// refreshes it inside the same family, and the family is what is checked. So
// the tick never re-validates the token — it would end every shell at the
// token TTL.
func TestReauthorizeIgnoresAccessTokenExpiryWhileTheFamilyIsLive(t *testing.T) {
	hs := newHarness(t)
	hs.auth.claims.RegisteredClaims = jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
	}
	hs.auth.validateCalls.Store(0)
	if err := hs.reauthorize(); err != nil {
		t.Fatalf("an expired access token on a live session ended the shell: %v", err)
	}
	if n := hs.auth.validateCalls.Load(); n != 0 {
		t.Fatalf("the tick validated the access token %d times; it must not", n)
	}
}

// The role that matters is the one held NOW, against the unit the node is in
// NOW: a viewer of unit A holds no terminal right there either.
func TestReauthorizeRefusesADemotedMember(t *testing.T) {
	hs := newHarness(t)
	hs.h.enforcer = hs.enforcerFor(authz.RoleViewer)
	if err := hs.reauthorize(); err == nil {
		t.Fatal("the shell survived the member being demoted below node.terminal")
	}
}
