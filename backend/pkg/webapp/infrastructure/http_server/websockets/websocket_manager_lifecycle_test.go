package websockets

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// The two lifecycle transitions of the account socket used to
// check their invariant in one step and act on it in another. Admission read
// the counts, released the lock, upgraded and only then inserted, so N
// concurrent unauthenticated upgrades all saw room; promotion fetched the
// client, ran the token/2FA/membership checks unlocked and then published the
// pointer unconditionally, so a client whose removal had already been
// processed was put back into the maps with its only removal event consumed.
// These tests park the transitions in the middle with hooks and drive the
// interleaving by hand, so they are deterministic rather than a stress run.

// TestAdmissionCapIsAHardBoundUnderConcurrentUpgrades sends twice the cap of
// simultaneous upgrades, parks every handler that reaches the upgrade behind a
// barrier, and only then lets them through: the cap must have been decided
// BEFORE the upgrade, so at most K sockets are ever upgraded or inserted, and
// exactly K are, since nothing was released in between.
func TestAdmissionCapIsAHardBoundUnderConcurrentUpgrades(t *testing.T) {
	const capacity = 8

	cases := []struct {
		name string
		// The two bounds; the one under test is set to capacity and the other
		// left wide open, so each case proves its own variable is inside the
		// atomic step.
		maxUnauthorized int
		maxTotal        int
		// preAuthorized sockets already in the authorized map, which count
		// toward the total bound only.
		preAuthorized int
	}{
		{name: "unauthorized bound", maxUnauthorized: capacity, maxTotal: 10 * capacity},
		{name: "total bound", maxUnauthorized: 10 * capacity, maxTotal: capacity, preAuthorized: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newLifecycleTestManager(t)
			m.maxUnauthorized = tc.maxUnauthorized
			m.maxTotal = tc.maxTotal
			for i := 0; i < tc.preAuthorized; i++ {
				c := NewClient(nil, m.fromClients, m.removeClientQueue, m.logger)
				m.mu.Lock()
				m.authorizedClients[c.ClientID] = &c
				m.mu.Unlock()
			}
			wantAdmitted := int32(capacity - tc.preAuthorized)
			arrivals := 2 * capacity

			// The barrier: every handler that reaches the upgrade counts
			// itself and waits, so the cap decision cannot be helped by
			// earlier arrivals having been inserted already.
			var upgrades atomic.Int32
			release := make(chan struct{})
			realUpgrade := m.upgrade
			m.upgrade = func(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
				upgrades.Add(1)
				<-release
				return realUpgrade(w, r)
			}

			e := echo.New()
			e.GET("/ws", m.HandleConnection)
			srv := httptest.NewServer(e)
			defer srv.Close()
			url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

			var refused, accepted atomic.Int32
			conns := make(chan *websocket.Conn, arrivals)
			var wg sync.WaitGroup
			for i := 0; i < arrivals; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
					if err != nil {
						if resp != nil && resp.StatusCode == http.StatusServiceUnavailable {
							refused.Add(1)
							return
						}
						t.Errorf("dial failed for a reason other than the cap: %v", err)
						return
					}
					accepted.Add(1)
					conns <- conn
				}()
			}

			// Every arrival is now either parked at the barrier or refused.
			deadline := time.Now().Add(10 * time.Second)
			for upgrades.Load()+refused.Load() < int32(arrivals) {
				if time.Now().After(deadline) {
					t.Fatalf("after 10s only %d parked + %d refused of %d arrivals",
						upgrades.Load(), refused.Load(), arrivals)
				}
				time.Sleep(time.Millisecond)
			}
			close(release)
			wg.Wait()
			close(conns)
			for conn := range conns {
				defer conn.Close()
			}

			if got := upgrades.Load(); got > wantAdmitted {
				t.Fatalf("%d sockets were upgraded, the cap is %d: the bound was decided after the upgrade", got, wantAdmitted)
			}
			if got := accepted.Load(); got != wantAdmitted {
				t.Fatalf("%d sockets were accepted, want exactly %d: the cap is a bound, not a refusal of everyone", got, wantAdmitted)
			}
			m.mu.RLock()
			unauthorized := len(m.unauthorizedClients)
			total := unauthorized + len(m.authorizedClients)
			m.mu.RUnlock()
			if unauthorized > tc.maxUnauthorized {
				t.Fatalf("%d unauthorized sockets in the map, the bound is %d", unauthorized, tc.maxUnauthorized)
			}
			if total > tc.maxTotal {
				t.Fatalf("%d sockets in the maps, the bound is %d", total, tc.maxTotal)
			}
		})
	}
}

// blockingMembership is a MemberAuthorizer that parks RequireActiveMembership
// until the test releases it, so the test can act on the maps while the
// authorization is in flight with the manager's lock released.
type blockingMembership struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingMembership) RequireActiveMembership(context.Context, uuid.UUID, uuid.UUID) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil
}

func (b *blockingMembership) AllowedOUs(context.Context, uuid.UUID, uuid.UUID, authz.Action) (bool, map[uuid.UUID]bool, error) {
	return true, nil, nil
}

func (b *blockingMembership) Authorize(context.Context, uuid.UUID, uuid.UUID, authz.Action, *uuid.UUID) error {
	return nil
}

// TestAuthorizationDoesNotResurrectARemovedClient: the peer goes away while
// the membership check is running, the read pump enqueues the removal and the
// hub processes it; the authorization then completes. The removed client must
// not be promoted — nothing would ever remove it again.
func TestAuthorizationDoesNotResurrectARemovedClient(t *testing.T) {
	claims := testClaims()
	m := newMembershipTestManager(t, claims)
	membership := &blockingMembership{entered: make(chan struct{}), release: make(chan struct{})}
	m.SetMemberAuthorizer(membership)

	client := NewClient(nil, m.fromClients, m.removeClientQueue, m.logger)
	m.mu.Lock()
	m.unauthorizedClients[client.ClientID] = &client
	m.mu.Unlock()

	payload, err := json.Marshal(websockets3.AuthorizationPayload{Token: "an-access-token"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.authorizeClient(context.Background(), client.ClientID, websockets3.InboundMessage{
			ClientID: client.ClientID,
			Type:     websockets3.InboundMessageTypeAuthorization,
			Payload:  payload,
		})
	}()

	// The authorization is parked inside the membership check, lock released.
	select {
	case <-membership.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the authorizer was never reached")
	}

	// The read pump's removal event is processed — the socket is gone.
	m.removeClient(client.ClientID)

	close(membership.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("authorizeClient did not return")
	}

	inAuthorized, inUserIndex, inUnauthorized := m.isAuthorized(client.ClientID, claims.UserID)
	if inAuthorized {
		t.Fatal("a removed socket was published as authorized: its removal event is already consumed, so nothing will ever remove it")
	}
	if inUserIndex {
		t.Fatal("a removed socket must not be indexed under the user")
	}
	if inUnauthorized {
		t.Fatal("a removed socket must not be back in the unauthorized map")
	}
	m.mu.RLock()
	_, hasIdentity := m.clientIdentity[client.ClientID]
	var inAccountIndex bool
	for _, id := range m.accountClients[claims.AccountID] {
		if id == client.ClientID {
			inAccountIndex = true
		}
	}
	m.mu.RUnlock()
	if hasIdentity {
		t.Fatal("a removed socket must not keep an identity entry")
	}
	if inAccountIndex {
		t.Fatal("a removed socket must not be indexed under the account — fan-out would find it")
	}
	if client.Authorized() {
		t.Fatal("a removed socket must not be marked authorized")
	}
	if !client.Closed() {
		t.Fatal("removal must be terminal: the client is marked closed so nothing can publish it later")
	}
}

func newLifecycleTestManager(t *testing.T) *WebSocketManager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWebSocketManager(fakeAuthService{claims: testClaims()}, make(chan websockets3.Message, 1), nil, logger)
}
