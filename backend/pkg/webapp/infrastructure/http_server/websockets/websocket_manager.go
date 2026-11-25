package websockets

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"

	"github.com/google/uuid"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

const (
	// authDeadline is how long a socket may stay unauthenticated after the
	// upgrade. The token arrives in the first frame, so a real client authorizes
	// within milliseconds; anything still silent after this is not a client.
	// Matches the gRPC endpoint's 15s pre-auth reaper.
	authDeadline = 15 * time.Second

	// maxUnauthorizedClients bounds sockets that have not yet presented a
	// token. This is the number a stranger controls, so it is the tighter of
	// the two bounds; it is still far above any plausible burst of real users
	// reconnecting after a deploy.
	maxUnauthorizedClients = 200

	// maxTotalClients bounds every socket, authorized or not. A browser tab
	// holds one; a hundred operators with five tabs each stay well inside it.
	maxTotalClients = 2000
)

// WebSocketManager handles WebSocket connections and message routing
type WebSocketManager struct {
	toClients   chan websockets3.Message
	fromClients chan websockets3.InboundMessage

	mu                  sync.RWMutex // Mutex to protect access to client maps
	unauthorizedClients map[websockets3.ClientID]*Client
	authorizedClients   map[websockets3.ClientID]*Client

	// This is a list of client IDs that are connected to the websocket server
	userClients    map[uuid.UUID][]websockets3.ClientID
	accountClients map[uuid.UUID][]websockets3.ClientID

	removeClientQueue chan websockets3.ClientID

	// pendingUpgrades is the number of admitted connections that are between
	// the cap decision and their insertion into unauthorizedClients. It is
	// counted as unauthorized by the admission check, which is what makes the
	// cap a bound: without it N concurrent arrivals all read the same map
	// length, all pass, and all upgrade. Guarded by mu.
	pendingUpgrades int

	// clientIdentity records who each authorized socket belongs to, so an
	// OU-scoped message can be filtered per recipient at send time.
	clientIdentity map[websockets3.ClientID]clientIdentity

	authService services.AuthService
	// authorizer resolves membership at authorization and which OUs a member
	// may read at send time. Nil is fail-closed at both: no socket authorizes,
	// and OU-scoped messages reach nobody — never "send to everyone".
	authorizer MemberAuthorizer
	// twoFactorPolicy refuses sockets from members who still owe an enrollment.
	twoFactorPolicy TwoFactorPolicy
	upgrader        websocket.Upgrader
	// upgrade performs the HTTP upgrade; it is m.upgrader.Upgrade in
	// production and a hook only for tests that need to park many handlers
	// between admission and insertion.
	upgrade func(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error)
	// maxUnauthorized / maxTotal are the two admission bounds, the package
	// constants in production and lowered by tests.
	maxUnauthorized int
	maxTotal        int
	logger          *slog.Logger

	// undeclaredActionLogged throttles the "account-global frame declares no
	// RequiredAction" log line to once per payload type: the drop happens per
	// recipient per frame, and a busy sender would otherwise flood the log
	// with the same programming error.
	undeclaredActionMu     sync.Mutex
	undeclaredActionLogged map[string]struct{}
}

// clientIdentity is the authenticated principal behind a socket.
type clientIdentity struct {
	userID    uuid.UUID
	accountID uuid.UUID
}

// MemberAuthorizer answers the three questions the socket asks the authz
// enforcer (which satisfies it): is this principal still an ACTIVE member of
// the account, which organizational units may they read, and may they perform
// a given account-level action.
type MemberAuthorizer interface {
	// RequireActiveMembership returns nil only for an active member; any
	// error, a lookup failure included, is a refusal.
	RequireActiveMembership(ctx context.Context, accountID, userID uuid.UUID) error
	AllowedOUs(ctx context.Context, accountID, userID uuid.UUID, action authz.Action) (bool, map[uuid.UUID]bool, error)
	// Authorize is the enforcer method middleware.RequireAction calls for a
	// REST route; mayReceive calls it with the same arguments (nil target)
	// for an account-global frame, so the WebSocket rule cannot drift from the
	// HTTP rule. Nil means granted; any error, a lookup failure included, is a
	// refusal.
	Authorize(ctx context.Context, accountID, userID uuid.UUID, action authz.Action, targetOU *uuid.UUID) error
}

// SetMemberAuthorizer wires membership resolution for authorization and OU
// scoping for pushed messages.
func (m *WebSocketManager) SetMemberAuthorizer(a MemberAuthorizer) {
	m.authorizer = a
}

// DisconnectUser closes every socket belonging to a user, in every account.
//
// Called when a membership is revoked or suspended. Without it a removed member
// keeps receiving live updates until their access token expires — the socket
// authorizes ONCE, on its first frame, so nothing re-checks membership for the
// life of the connection. Their next connect re-authorizes, and since
// authorizeClient resolves membership through the MemberAuthorizer, is refused
// — the access token alone is not enough, being stateless and valid to its
// TTL after the revocation.
//
// Disconnecting across accounts is deliberate: a socket is indexed by user, and
// a revocation is a strong enough signal to make the person prove themselves
// again everywhere rather than risk leaving the wrong one open.
func (m *WebSocketManager) DisconnectUser(userID uuid.UUID) {
	m.mu.RLock()
	clientIDs := append([]websockets3.ClientID(nil), m.userClients[userID]...)
	clients := make([]*Client, 0, len(clientIDs))
	for _, id := range clientIDs {
		if client, ok := m.authorizedClients[id]; ok {
			clients = append(clients, client)
		}
	}
	m.mu.RUnlock()

	// Closed outside the lock: Close makes ReadPump fail, which enqueues the
	// removal that processClientRemovals then handles under the same mutex.
	for _, client := range clients {
		client.Close()
	}
	if len(clients) > 0 {
		m.logger.Info("closed websockets for a revoked member",
			"user_id", userID, "sockets", len(clients))
	}
}

// TwoFactorPolicy answers whether a member still owes an enrollment.
type TwoFactorPolicy interface {
	AccountRequiresTwoFactor(ctx context.Context, accountID uuid.UUID) bool
	UserHasTwoFactor(ctx context.Context, userID uuid.UUID) bool
}

// SetTwoFactorPolicy wires the enrollment requirement into WS authorization.
//
// This socket authenticates with a token in its FIRST FRAME rather than an HTTP
// middleware, so the enrollment gate that guards the REST surface never sees
// it. Without this check, the live node-data feed would be the one place a
// member of a 2FA-mandating account could reach without enrolling — and it is
// among the most data-rich surfaces there is.
func (m *WebSocketManager) SetTwoFactorPolicy(p TwoFactorPolicy) {
	m.twoFactorPolicy = p
}

// mayReceive reports whether a socket should get an account-addressed message.
//
// Two shapes, one rule — a live update is visible exactly to those who could
// GET the same resource over HTTP:
//
//   - An OU-scoped message (ScopeOU set, or ScopeOUUnassigned) follows the
//     node lists: node.read resolved per unit through AllowedOUs, an OU-less
//     resource reaching account-wide holders only.
//   - An account-global message follows the REST route that lists the
//     resource: its RequiredAction is put to the enforcer with a nil target,
//     the call middleware.RequireAction makes. This branch once returned
//     true unconditionally, so a member whose GET /images or GET
//     /nodes/adoption-refusals answered 403 received the same rows — device
//     serial included — over the socket.
//
// Fails CLOSED on every uncertainty — unknown client, no authorizer wired, no
// action declared, an error resolving permissions. A dropped live update costs
// a stale panel until the next fetch; a wrongly delivered one is the leak this
// exists to prevent.
func (m *WebSocketManager) mayReceive(clientID websockets3.ClientID, message websockets3.Message) bool {
	m.mu.RLock()
	identity, known := m.clientIdentity[clientID]
	m.mu.RUnlock()
	if !known || m.authorizer == nil {
		return false
	}

	if message.ScopeOU == nil && !message.ScopeOUUnassigned {
		return m.mayReceiveAccountGlobal(clientID, identity, message)
	}

	allowAll, ous, err := m.authorizer.AllowedOUs(
		context.Background(), identity.accountID, identity.userID, authz.ActionNodeRead)
	if err != nil {
		m.logger.Warn("websocket: failed to resolve OU scope, withholding message",
			"client_id", clientID, "error", err)
		return false
	}
	visible := allowAll
	// An OU-less resource is visible only to account-wide holders, mirroring
	// how the list handlers treat it.
	if !allowAll && message.ScopeOU != nil {
		visible = ous[*message.ScopeOU]
	}
	if !visible {
		return false
	}
	// A unit's resource whose REST route also demands an action of its own
	// (a playbook run: playbook.read) needs that action too.
	if message.RequiredAction != "" {
		return m.holdsAccountWide(clientID, identity, message.RequiredAction)
	}
	return true
}

// payloadFor picks what an admitted recipient is sent: the full payload, or,
// for a message split by WithRedactedVariant, the redacted one unless the
// recipient holds FullPayloadAction account-wide. Nil means send nothing.
func (m *WebSocketManager) payloadFor(clientID websockets3.ClientID, message websockets3.Message) websockets3.MessagePayload {
	if message.FullPayloadAction == "" {
		return message.Payload
	}
	m.mu.RLock()
	identity, known := m.clientIdentity[clientID]
	m.mu.RUnlock()
	if known && m.authorizer != nil && m.holdsAccountWide(clientID, identity, message.FullPayloadAction) {
		return message.Payload
	}
	return message.RedactedPayload
}

// mayReceiveAccountGlobal is the account-global half of mayReceive: the
// recipient must hold message.RequiredAction the way the REST route demands
// it. The target is nil on purpose — that is what RequireAction passes, and it
// is what makes an account-level action need an account-wide binding (or, for
// the catalog reads in authz.IsAnyScopeRead, any binding), the same answer the
// HTTP door gives.
func (m *WebSocketManager) mayReceiveAccountGlobal(clientID websockets3.ClientID, identity clientIdentity, message websockets3.Message) bool {
	if message.RequiredAction == "" {
		m.logUndeclaredAction(message)
		return false
	}
	return m.holdsAccountWide(clientID, identity, message.RequiredAction)
}

// holdsAccountWide asks the enforcer the question middleware.RequireAction
// asks: this action, nil target. Any error is a refusal.
func (m *WebSocketManager) holdsAccountWide(clientID websockets3.ClientID, identity clientIdentity, action authz.Action) bool {
	err := m.authorizer.Authorize(context.Background(), identity.accountID, identity.userID, action, nil)
	if err == nil {
		return true
	}
	// A refusal is the ordinary outcome for a member without the right and
	// not worth a line per frame; a lookup failure is.
	if !errors.Is(err, app_errors.ErrForbidden) && !errors.Is(err, app_errors.ErrMembershipDisabled) {
		m.logger.Warn("websocket: failed to resolve permission for a message, withholding it",
			"client_id", clientID, "action", action, "error", err)
	}
	return false
}

// logUndeclaredAction reports an account-global message that declares no
// RequiredAction, once per payload type. NewAccountMessage makes the action a
// required argument, so this is reachable only through a hand-built Message —
// a programming error, and one that must surface in the log rather than as a
// silently stale panel nobody connects to a missing declaration.
func (m *WebSocketManager) logUndeclaredAction(message websockets3.Message) {
	kind := "<unknown>"
	switch p := message.Payload.(type) {
	case websockets3.MessageWrapper:
		kind = p.Type
	case *websockets3.MessageWrapper:
		if p != nil {
			kind = p.Type
		}
	default:
		if p != nil {
			kind = reflect.TypeOf(p).String()
		}
	}

	m.undeclaredActionMu.Lock()
	_, seen := m.undeclaredActionLogged[kind]
	if !seen {
		m.undeclaredActionLogged[kind] = struct{}{}
	}
	m.undeclaredActionMu.Unlock()
	if seen {
		return
	}
	m.logger.Error("websocket: account-global message declares no RequiredAction, withholding it from every recipient (logged once per type)",
		"type", kind, "account_id", message.AccountID)
}

// Run starts the WebSocket manager's background processes
func (m *WebSocketManager) Run() {
	go m.processToClients()
	go m.processFromClients()
	go m.processClientRemovals()

	m.logger.Info("websocket manager is running")
}

// HandleConnection upgrades an Server connection to WebSocket and manages it.
//
// This route carries no auth middleware by design — the token arrives in the
// first frame, not in a header — which makes everything before authorizeClient
// reachable by anyone on the internet. Two bounds therefore apply here and
// nowhere else: how many unauthenticated sockets may exist at once, and how
// long one may stay unauthenticated. Without them a peer that completes the
// upgrade and then says nothing costs two goroutines and a 1000-slot buffer,
// forever, because the token-expiry close only arms once a token has been seen.
// The gRPC endpoint has had a 15s pre-auth deadline for exactly this reason,
// and the terminal 10s; this is the same guarantee for the socket the browser
// uses.
func (m *WebSocketManager) HandleConnection(c echo.Context) error {
	// Refuse BEFORE upgrading: an HTTP error costs nothing, an upgraded socket
	// costs two goroutines. The unauthenticated bound is the tighter of the two
	// because it is the one a stranger controls.
	//
	// The check and the reservation are ONE locked step: a connection that
	// passes is counted (pendingUpgrades) before the lock is released, so the
	// next arrival sees it. Reading the map length, unlocking, upgrading and
	// inserting afterwards let every one of N simultaneous unauthenticated
	// arrivals observe room and exceed both caps.
	m.mu.Lock()
	unauthorized := len(m.unauthorizedClients) + m.pendingUpgrades
	total := unauthorized + len(m.authorizedClients)
	if unauthorized >= m.maxUnauthorized || total >= m.maxTotal {
		m.mu.Unlock()
		m.logger.Warn("refusing websocket upgrade: connection limit reached",
			"unauthorized", unauthorized, "total", total)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "too many connections")
	}
	m.pendingUpgrades++
	m.mu.Unlock()

	// Upgrade the Server connection to a WebSocket connection
	// The reservation is released by construction, not by the happy path: a
	// panic inside the upgrade is swallowed by the Recover middleware, and a
	// reservation it left behind would shrink the cap by one forever.
	reservationHeld := true
	releaseReservationLocked := func() {
		if reservationHeld {
			reservationHeld = false
			m.pendingUpgrades--
		}
	}
	defer func() {
		m.mu.Lock()
		releaseReservationLocked()
		m.mu.Unlock()
	}()

	conn, err := m.upgrade(c.Response(), c.Request())

	// The reservation becomes the map entry in the same locked step, so the
	// count never dips between the two; a failed upgrade releases it. From
	// here on the map IS the count, and removeClient releases the slot by
	// deleting the entry — the pre-auth reaper and a dropped peer both end
	// there.
	m.mu.Lock()
	releaseReservationLocked()
	if err != nil {
		m.mu.Unlock()
		m.logger.Error("failed to upgrade connection to websocket", "error", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "could not upgrade connection")
	}
	client := NewClient(conn, m.fromClients, m.removeClientQueue, m.logger)
	// Initially, the client is unauthorized
	m.unauthorizedClients[client.ClientID] = &client
	m.mu.Unlock()

	// Start client read and write loops
	go client.WritePump()
	go client.ReadPump()

	// Reap it if it never authorizes. Closing makes ReadPump fail, which
	// enqueues removal through the ordinary path, so the bookkeeping stays in
	// one place.
	time.AfterFunc(authDeadline, func() {
		if client.Authorized() {
			return
		}
		m.logger.Info("closing websocket: no authorization within the pre-auth deadline",
			"client_id", client.ClientID, "deadline", authDeadline)
		client.Close()
	})

	m.logger.Info("new websocket connection established", "client_id", client.ClientID)

	return nil
}

// authorizeClient handles client authorization using JWT token
func (m *WebSocketManager) authorizeClient(ctx context.Context, clientID websockets3.ClientID, message websockets3.InboundMessage) {
	m.mu.RLock()
	client, ok := m.unauthorizedClients[clientID]
	m.mu.RUnlock()

	if !ok {
		m.logger.Warn("client not found for authorization", "client_id", clientID)
		return
	}

	if client == nil {
		m.logger.Error("client is nil", "client_id", clientID)
		return
	}

	// Parse authorization payload
	var payload websockets3.AuthorizationPayload
	err := json.Unmarshal(message.Payload, &payload)
	if err != nil {
		m.logger.Error("failed to parse authorization payload", "error", err, "client_id", clientID)
		client.Send(websockets3.NewMessageAuthorizationStatusError("invalid request"))
		return
	}

	// Validate the token
	tokenClaims, err := m.authService.ValidateAccessToken(payload.Token)
	if err != nil {
		errorMsg := "invalid token"
		if errors.Is(err, app_errors.ErrTokenExpired) {
			errorMsg = app_errors.ErrTokenExpired.Error()
		} else if errors.Is(err, app_errors.ErrTokenInvalid) {
			errorMsg = app_errors.ErrTokenInvalid.Error()
		}

		m.logger.Warn("token validation failed", "error", err, "client_id", clientID)
		client.Send(websockets3.NewMessageAuthorizationStatusError(errorMsg))
		return
	}

	// Bound the socket's lifetime to the access token's expiry (revocation
	// window ≤ token TTL, same as HTTP).
	if tokenClaims.ExpiresAt != nil {
		client.SetExpiry(tokenClaims.ExpiresAt.Time)
	}

	// Refuse the socket if the account mandates a second factor this user has
	// not enrolled — the REST gate cannot see this connection.
	if m.twoFactorPolicy != nil {
		ctx := context.Background()
		if m.twoFactorPolicy.AccountRequiresTwoFactor(ctx, tokenClaims.AccountID) &&
			!m.twoFactorPolicy.UserHasTwoFactor(ctx, tokenClaims.UserID) {
			m.logger.Info("websocket refused: two-factor enrollment required",
				"client_id", clientID, "user_id", tokenClaims.UserID)
			client.Send(websockets3.NewMessageAuthorizationStatusError("two_factor_enrollment_required"))
			return
		}
	}

	// Refuse the socket unless the principal is still an ACTIVE member of the
	// account. The access token proves who signed in, not that they still
	// belong here: it is stateless and valid to its TTL after a revocation, so
	// without this a removed member whose sockets DisconnectUser just closed
	// reconnected with the same token and received the account-wide feed.
	// Fails CLOSED, the rule mayReceive already applies to a
	// missing filter: no authorizer wired, or a lookup that errors, refuses.
	// The socket stays unauthorized, so the pre-auth reaper closes it.
	if m.authorizer == nil {
		m.logger.Error("websocket refused: no member authorizer wired, cannot resolve membership",
			"client_id", clientID, "user_id", tokenClaims.UserID)
		client.Send(websockets3.NewMessageAuthorizationStatusError("membership_required"))
		return
	}
	if err := m.authorizer.RequireActiveMembership(ctx, tokenClaims.AccountID, tokenClaims.UserID); err != nil {
		m.logger.Info("websocket refused: not an active member of the account",
			"client_id", clientID, "user_id", tokenClaims.UserID,
			"account_id", tokenClaims.AccountID, "error", err)
		client.Send(websockets3.NewMessageAuthorizationStatusError("membership_required"))
		return
	}

	m.mu.Lock()
	// Publish only if this is still the same live socket that was fetched
	// above. The checks ran with the lock released, and in that window the
	// read pump can have exited and the hub processed its removal: publishing
	// then puts a closed client back into the maps with its only removal
	// event already consumed, so it holds a slot, is indexed for fan-out and
	// is never removed again. Removal is terminal — a client
	// the hub has removed, or whose socket has closed, never re-enters a map.
	if current, present := m.unauthorizedClients[clientID]; !present || current != client || client.Closed() {
		m.mu.Unlock()
		m.logger.Info("websocket closed during authorization; not publishing it",
			"client_id", clientID, "user_id", tokenClaims.UserID)
		// Nothing is left to bookkeep, but make sure the socket itself is
		// gone: a Close on an already-closed conn is a no-op.
		client.Close()
		return
	}
	// Move client from unauthorized to authorized
	m.authorizedClients[clientID] = client
	m.clientIdentity[clientID] = clientIdentity{
		userID:    tokenClaims.UserID,
		accountID: tokenClaims.AccountID,
	}

	// Add client to user's client list
	userID := tokenClaims.UserID
	if _, exists := m.userClients[userID]; !exists {
		m.userClients[userID] = make([]websockets3.ClientID, 0)
	}
	m.userClients[userID] = append(m.userClients[userID], clientID)

	// Add client to account's client list
	accountID := tokenClaims.AccountID
	if _, exists := m.accountClients[accountID]; !exists {
		m.accountClients[accountID] = make([]websockets3.ClientID, 0)
	}
	m.accountClients[accountID] = append(m.accountClients[accountID], clientID)

	delete(m.unauthorizedClients, clientID)
	// Tell the pre-auth reaper this socket made it, so a timer that fires a
	// moment later does not close a working connection. Under the same lock
	// as the publication, so the reaper and the maps never disagree.
	client.MarkAuthorized()
	m.mu.Unlock()

	m.logger.Info("client authorized successfully",
		"client_id", clientID,
		"user_id", tokenClaims.UserID,
		"account_id", tokenClaims.AccountID)

	// Send success confirmation to client
	client.Send(websockets3.NewMessageAuthorizationStatusOk())
}

// processToClients distributes messages to clients
func (m *WebSocketManager) processToClients() {
	for {
		message, ok := <-m.toClients
		if !ok {
			m.logger.Warn("toClients channel closed, exiting websocket manager")
			return
		}

		// If message has a specific UserID, send only to that user's clients
		if message.UserID != nil {
			m.mu.RLock()
			clientIDs, exists := m.userClients[*message.UserID]
			if !exists {
				m.mu.RUnlock()
				m.logger.Debug("no clients found for user", "user_id", message.UserID)
				continue
			}

			// Create a copy of client IDs to release the lock sooner
			clientIDsCopy := make([]websockets3.ClientID, len(clientIDs))
			copy(clientIDsCopy, clientIDs)
			m.mu.RUnlock()

			for _, clientID := range clientIDsCopy {
				m.mu.RLock()
				client, exists := m.authorizedClients[clientID]
				m.mu.RUnlock()

				if !exists {
					m.logger.Warn("attempted to send message to non-existent client",
						"client_id", clientID, "user_id", message.UserID)
					continue
				}
				client.Send(message.Payload)
			}
		} else if message.AccountID != nil {
			m.fanOutToAccount(message)
		}
	}
}

// fanOutToAccount delivers an account-addressed message to every authorized
// socket of that account that mayReceive admits. Split out of processToClients
// so the per-recipient filter can be driven by a test without the hub's
// goroutines.
func (m *WebSocketManager) fanOutToAccount(message websockets3.Message) {
	m.mu.RLock()
	clientIDs, exists := m.accountClients[*message.AccountID]
	if !exists {
		m.mu.RUnlock()
		m.logger.Debug("no clients found for account", "account_id", message.AccountID)
		return
	}

	// Create a copy of client IDs to release the lock sooner
	clientIDsCopy := make([]websockets3.ClientID, len(clientIDs))
	copy(clientIDsCopy, clientIDs)
	m.mu.RUnlock()

	for _, clientID := range clientIDsCopy {
		m.mu.RLock()
		client, exists := m.authorizedClients[clientID]
		m.mu.RUnlock()

		if !exists {
			m.logger.Warn("attempted to send message to non-existent client",
				"client_id", clientID, "account_id", message.AccountID)
			continue
		}
		if !m.mayReceive(clientID, message) {
			continue
		}
		payload := m.payloadFor(clientID, message)
		if payload == nil {
			continue
		}
		client.Send(payload)
	}
}

// processFromClients handles incoming messages from clients
func (m *WebSocketManager) processFromClients() {
	for {
		message := <-m.fromClients

		switch message.Type {
		case websockets3.InboundMessageTypeAuthorization:
			m.authorizeClient(context.Background(), message.ClientID, message)
		// Add other message type handlers here
		default:
			m.logger.Debug("received unhandled message type", "type", message.Type, "client_id", message.ClientID)
		}
	}
}

// processClientRemovals handles client disconnections
func (m *WebSocketManager) processClientRemovals() {
	for {
		m.removeClient(<-m.removeClientQueue)
	}
}

// removeClient takes one socket out of every map and index. Removal is
// terminal: the client is marked closed under the lock before it leaves the
// maps, so an authorization still in flight for it cannot publish it again.
func (m *WebSocketManager) removeClient(idToRemove websockets3.ClientID) {
	m.mu.Lock()
	if c, ok := m.authorizedClients[idToRemove]; ok && c != nil {
		c.markClosed()
	}
	if c, ok := m.unauthorizedClients[idToRemove]; ok && c != nil {
		c.markClosed()
	}
	// Remove from both connection maps
	delete(m.authorizedClients, idToRemove)
	delete(m.unauthorizedClients, idToRemove)
	delete(m.clientIdentity, idToRemove)

	// Remove from userClients map
	for userID, clientIDs := range m.userClients {
		newClientIDs := make([]websockets3.ClientID, 0, len(clientIDs))
		for _, clientID := range clientIDs {
			if clientID != idToRemove {
				newClientIDs = append(newClientIDs, clientID)
			}
		}

		// Update the user's client list
		if len(newClientIDs) == 0 {
			// If no clients left, remove the user entry
			delete(m.userClients, userID)
		} else {
			m.userClients[userID] = newClientIDs
		}
	}

	// Remove from accountClients map
	for accountID, clientIDs := range m.accountClients {
		newClientIDs := make([]websockets3.ClientID, 0, len(clientIDs))
		for _, clientID := range clientIDs {
			if clientID != idToRemove {
				newClientIDs = append(newClientIDs, clientID)
			}
		}

		// Update the account's client list
		if len(newClientIDs) == 0 {
			// If no clients left, remove the account entry
			delete(m.accountClients, accountID)
		} else {
			m.accountClients[accountID] = newClientIDs
		}
	}
	m.mu.Unlock()

	m.logger.Info("client removed", "client_id", idToRemove)
}

// NewWebSocketManager creates a new WebSocket manager
// NewWebSocketManager wires the account fan-out socket. checkOrigin comes from
// the shared wsorigin policy; it used to be derived from an allowCORS boolean
// that was ALWAYS true (the server installs a wildcard CORS policy when the
// block is disabled), so the upgrader accepted every origin on every install.
// A nil checkOrigin falls back to gorilla's same-origin default
// rather than to allow-all.
func NewWebSocketManager(authService services.AuthService, toClientsQueue chan websockets3.Message, checkOrigin func(*http.Request) bool, logger *slog.Logger) *WebSocketManager {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	if checkOrigin != nil {
		upgrader.CheckOrigin = checkOrigin
	}

	m := &WebSocketManager{
		toClients:           toClientsQueue,
		fromClients:         make(chan websockets3.InboundMessage, 1000),
		unauthorizedClients: make(map[websockets3.ClientID]*Client),
		authorizedClients:   make(map[websockets3.ClientID]*Client),
		clientIdentity:      make(map[websockets3.ClientID]clientIdentity),
		userClients:         make(map[uuid.UUID][]websockets3.ClientID),
		accountClients:      make(map[uuid.UUID][]websockets3.ClientID),
		removeClientQueue:   make(chan websockets3.ClientID, 100),
		authService:         authService,
		upgrader:            upgrader,
		logger:              logger.With("component", "websocket_manager"),

		undeclaredActionLogged: make(map[string]struct{}),

		maxUnauthorized: maxUnauthorizedClients,
		maxTotal:        maxTotalClients,
	}
	m.upgrade = func(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
		return m.upgrader.Upgrade(w, r, nil)
	}
	return m
}
