package websockets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// The account socket authorizes ONCE, on its first frame, and the JWT it
// authorizes with is stateless: revoking a membership does not invalidate an
// access token already issued. DisconnectUser closes the sockets a removed
// member already holds — but until membership was resolved at authorization,
// the same person could reconnect with the same token and receive the
// account-wide feed (image events with the full ImageResult, adoption refusals
// with the device serial) until the token expired. These tests
// drive authorizeClient directly, the way processFromClients does, without
// starting the hub's goroutines.

// fakeAuthService accepts any token as the fixed principal. Embedding the
// interface leaves every other method nil — none is reached here.
type fakeAuthService struct {
	services.AuthService
	claims *services.TokenClaims
}

func (f fakeAuthService) ValidateAccessToken(string) (*services.TokenClaims, error) {
	return f.claims, nil
}

// fakeMembership stands in for the authz enforcer: `active` is the membership
// row's status, `err` a lookup failure.
type fakeMembership struct {
	active bool
	err    error
}

func (f *fakeMembership) AllowedOUs(context.Context, uuid.UUID, uuid.UUID, authz.Action) (bool, map[uuid.UUID]bool, error) {
	return true, nil, nil
}

func (f *fakeMembership) Authorize(context.Context, uuid.UUID, uuid.UUID, authz.Action, *uuid.UUID) error {
	return nil
}

func (f *fakeMembership) RequireActiveMembership(context.Context, uuid.UUID, uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	if !f.active {
		return app_errors.ErrMembershipDisabled
	}
	return nil
}

func newMembershipTestManager(t *testing.T, claims *services.TokenClaims) *WebSocketManager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Not Run(): authorizeClient is called directly, so no goroutine touches
	// the maps and the test can read them without racing.
	return NewWebSocketManager(fakeAuthService{claims: claims}, make(chan websockets3.Message, 1), nil, logger)
}

func testClaims() *services.TokenClaims {
	return &services.TokenClaims{
		UserID:    uuid.New(),
		AccountID: uuid.New(),
		TokenType: "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
}

// connectAndAuthorize does what HandleConnection + the first frame do: a socket
// in the unauthorized map, then the authorization message. A nil conn is
// Close-safe, and nothing here writes to the wire.
func connectAndAuthorize(t *testing.T, m *WebSocketManager) *Client {
	t.Helper()
	client := NewClient(nil, m.fromClients, m.removeClientQueue, m.logger)
	m.mu.Lock()
	m.unauthorizedClients[client.ClientID] = &client
	m.mu.Unlock()

	payload, err := json.Marshal(websockets3.AuthorizationPayload{Token: "the-original-access-token"})
	if err != nil {
		t.Fatal(err)
	}
	m.authorizeClient(context.Background(), client.ClientID, websockets3.InboundMessage{
		ClientID: client.ClientID,
		Type:     websockets3.InboundMessageTypeAuthorization,
		Payload:  payload,
	})
	return &client
}

// authStatus reads the authorization_status frame the manager queued for the
// client. Exactly one frame is expected either way: ok, or failed with a
// reason.
func authStatus(t *testing.T, c *Client) websockets3.MessageAuthStatusPayload {
	t.Helper()
	select {
	case frame := <-c.toClient:
		var msg websockets3.MessageAuthStatus
		if err := json.Unmarshal(frame.ToBytes(), &msg); err != nil {
			t.Fatalf("frame is not an authorization status: %v", err)
		}
		if msg.Type != "authorization_status" {
			t.Fatalf("expected an authorization_status frame, got %q", msg.Type)
		}
		return msg.Payload
	default:
		t.Fatal("no authorization_status frame was sent")
		return websockets3.MessageAuthStatusPayload{}
	}
}

func (m *WebSocketManager) isAuthorized(id websockets3.ClientID, userID uuid.UUID) (inAuthorized, inUserIndex, inUnauthorized bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, inAuthorized = m.authorizedClients[id]
	for _, cid := range m.userClients[userID] {
		if cid == id {
			inUserIndex = true
		}
	}
	_, inUnauthorized = m.unauthorizedClients[id]
	return
}

func assertRefused(t *testing.T, m *WebSocketManager, c *Client, userID uuid.UUID, wantReason string) {
	t.Helper()
	status := authStatus(t, c)
	if status.Status != "failed" {
		t.Fatalf("status = %q, want failed", status.Status)
	}
	if status.Error == nil || *status.Error != wantReason {
		t.Fatalf("error = %v, want %q", status.Error, wantReason)
	}
	inAuthorized, inUserIndex, inUnauthorized := m.isAuthorized(c.ClientID, userID)
	if inAuthorized {
		t.Fatal("a refused socket must not be in authorizedClients")
	}
	if inUserIndex {
		t.Fatal("a refused socket must not be indexed under the user — DisconnectUser would find it")
	}
	if !inUnauthorized {
		t.Fatal("a refused socket stays unauthorized so the pre-auth reaper closes it")
	}
	if c.Authorized() {
		t.Fatal("a refused socket must not be marked authorized, or the reaper spares it")
	}
}

func TestRemovedMemberCannotReconnectWithTheOriginalToken(t *testing.T) {
	claims := testClaims()
	m := newMembershipTestManager(t, claims)
	membership := &fakeMembership{active: true}
	m.SetMemberAuthorizer(membership)

	// An active member connects: authorized, indexed, told ok.
	c1 := connectAndAuthorize(t, m)
	if status := authStatus(t, c1); status.Status != "ok" {
		t.Fatalf("an active member must authorize; status = %q", status.Status)
	}
	if inAuthorized, inUserIndex, _ := m.isAuthorized(c1.ClientID, claims.UserID); !inAuthorized || !inUserIndex {
		t.Fatal("an authorized socket must be in authorizedClients and the user index")
	}

	// The membership is revoked. The member service invalidates the enforcer
	// and tears down the live sockets ...
	membership.active = false
	m.DisconnectUser(claims.UserID)

	// ... and the browser reconnects with the SAME still-valid access token.
	c2 := connectAndAuthorize(t, m)
	assertRefused(t, m, c2, claims.UserID, "membership_required")
}

func TestMembershipLookupFailureRefusesTheSocket(t *testing.T) {
	claims := testClaims()
	m := newMembershipTestManager(t, claims)
	m.SetMemberAuthorizer(&fakeMembership{active: true, err: errors.New("database is away")})

	c := connectAndAuthorize(t, m)
	assertRefused(t, m, c, claims.UserID, "membership_required")
}

func TestNoMembershipAuthorizerWiredRefusesTheSocket(t *testing.T) {
	// Fail closed, the same rule mayReceive applies to a missing filter: a
	// composition root that forgot the setter must not open the feed to
	// everyone with a token.
	claims := testClaims()
	m := newMembershipTestManager(t, claims)

	c := connectAndAuthorize(t, m)
	assertRefused(t, m, c, claims.UserID, "membership_required")
}
