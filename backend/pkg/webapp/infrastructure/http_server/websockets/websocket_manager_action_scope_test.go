package websockets

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"

	"github.com/google/uuid"
)

// An account-global frame (no OU scope) used to reach every active member of
// the account. Its HTTP representation does not: `GET /images` is gated on
// image.read and `GET /nodes/adoption-refusals` on node.create, so a member
// holding no binding — or a binding without that action — was refused the
// list and handed the same rows over the socket, device serial included.
// These tests drive fanOutToAccount directly, the way
// processToClients does, without starting the hub's goroutines.

// authorizeCall records what mayReceive asked, so a test can assert it asks
// the same question middleware.RequireAction asks: this action, nil target.
type authorizeCall struct {
	userID   uuid.UUID
	action   authz.Action
	targetOU *uuid.UUID
}

// fakeActionAuthorizer is an active member whose account-wide grants are
// decided by `grant` per (user, action). `authorizeErr` is a lookup failure.
// The OU half serves the OU-scoped branch, which must keep working unchanged.
type fakeActionAuthorizer struct {
	grant        func(userID uuid.UUID, action authz.Action) bool
	authorizeErr error

	allowAllOUs bool
	ous         map[uuid.UUID]bool

	calls []authorizeCall
}

func (f *fakeActionAuthorizer) RequireActiveMembership(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (f *fakeActionAuthorizer) AllowedOUs(context.Context, uuid.UUID, uuid.UUID, authz.Action) (bool, map[uuid.UUID]bool, error) {
	return f.allowAllOUs, f.ous, nil
}

func (f *fakeActionAuthorizer) Authorize(_ context.Context, _ uuid.UUID, userID uuid.UUID, action authz.Action, targetOU *uuid.UUID) error {
	f.calls = append(f.calls, authorizeCall{userID: userID, action: action, targetOU: targetOU})
	if f.authorizeErr != nil {
		return f.authorizeErr
	}
	if f.grant != nil && f.grant(userID, action) {
		return nil
	}
	return errors.New("forbidden")
}

func grantOnly(actions ...authz.Action) func(uuid.UUID, authz.Action) bool {
	return func(_ uuid.UUID, action authz.Action) bool {
		for _, a := range actions {
			if a == action {
				return true
			}
		}
		return false
	}
}

func grantEverything(uuid.UUID, authz.Action) bool { return true }

// connectMember authorizes a socket for the claims the fake auth service
// currently holds and discards its authorization_status frame, so what is
// left in toClient afterwards is only what fanOutToAccount delivered.
func connectMember(t *testing.T, m *WebSocketManager) *Client {
	t.Helper()
	c := connectAndAuthorize(t, m)
	if status := authStatus(t, c); status.Status != "ok" {
		t.Fatalf("the member must authorize; status = %q", status.Status)
	}
	return c
}

// receivedTypes drains the frames queued for a socket and returns their
// MessageWrapper types in order.
func receivedTypes(t *testing.T, c *Client) []string {
	t.Helper()
	var types []string
	for {
		select {
		case frame := <-c.toClient:
			wrapper, ok := frame.(websockets3.MessageWrapper)
			if !ok {
				t.Fatalf("frame is a %T, want a MessageWrapper", frame)
			}
			types = append(types, wrapper.Type)
		default:
			return types
		}
	}
}

func imageEvent(accountID uuid.UUID, kind string) websockets3.Message {
	return websockets3.NewAccountMessage(accountID, authz.ActionImageRead, websockets3.MessageWrapper{
		Type:    kind,
		Payload: map[string]string{"id": uuid.NewString()},
	})
}

func adoptionRefusedEvent(accountID uuid.UUID) websockets3.Message {
	return websockets3.NewAccountMessage(accountID, authz.ActionNodeCreate, websockets3.MessageWrapper{
		Type:    "node_adoption_refused",
		Payload: map[string]string{"hardware_serial": "10000000abcdef01"},
	})
}

// deliverAll fans out the whole account-global inventory: the three image
// events and the adoption refusal.
func deliverAll(m *WebSocketManager, accountID uuid.UUID) {
	for _, kind := range []string{"image_created", "image_updated", "image_deleted"} {
		m.fanOutToAccount(imageEvent(accountID, kind))
	}
	m.fanOutToAccount(adoptionRefusedEvent(accountID))
}

func TestBindinglessMemberReceivesNoAccountGlobalFrame(t *testing.T) {
	claims := testClaims()
	m := newMembershipTestManager(t, claims)
	// Active, but holds no binding at all — the enforcer refuses every action.
	m.SetMemberAuthorizer(&fakeActionAuthorizer{grant: nil})
	c := connectMember(t, m)

	deliverAll(m, claims.AccountID)

	if got := receivedTypes(t, c); len(got) != 0 {
		t.Fatalf("a member with no bindings received %v; GET /images and GET /nodes/adoption-refusals would both have refused them", got)
	}
}

func TestAccountGlobalFrameFollowsTheRestRoutesAction(t *testing.T) {
	t.Run("image.read receives image events only", func(t *testing.T) {
		claims := testClaims()
		m := newMembershipTestManager(t, claims)
		auth := &fakeActionAuthorizer{grant: grantOnly(authz.ActionImageRead)}
		m.SetMemberAuthorizer(auth)
		c := connectMember(t, m)

		deliverAll(m, claims.AccountID)

		got := receivedTypes(t, c)
		if strings.Join(got, ",") != "image_created,image_updated,image_deleted" {
			t.Fatalf("received %v, want the three image events and no adoption refusal", got)
		}
	})

	t.Run("node.create receives the adoption refusal only", func(t *testing.T) {
		claims := testClaims()
		m := newMembershipTestManager(t, claims)
		auth := &fakeActionAuthorizer{grant: grantOnly(authz.ActionNodeCreate)}
		m.SetMemberAuthorizer(auth)
		c := connectMember(t, m)

		deliverAll(m, claims.AccountID)

		got := receivedTypes(t, c)
		if strings.Join(got, ",") != "node_adoption_refused" {
			t.Fatalf("received %v, want only node_adoption_refused", got)
		}
	})

	t.Run("asks the enforcer exactly what RequireAction asks", func(t *testing.T) {
		// middleware.RequireAction calls enforcer.Authorize(ctx, account, user,
		// action, nil). The socket must pass the declared action and a NIL
		// target: an account-level action resolved against a unit would take
		// the any-scope branch for catalog reads and the wrong roles for the
		// rest, and the two paths would answer differently.
		claims := testClaims()
		m := newMembershipTestManager(t, claims)
		auth := &fakeActionAuthorizer{grant: grantEverything}
		m.SetMemberAuthorizer(auth)
		connectMember(t, m)

		m.fanOutToAccount(adoptionRefusedEvent(claims.AccountID))

		if len(auth.calls) != 1 {
			t.Fatalf("Authorize was asked %d times, want once per recipient per frame", len(auth.calls))
		}
		call := auth.calls[0]
		if call.userID != claims.UserID || call.action != authz.ActionNodeCreate || call.targetOU != nil {
			t.Fatalf("asked (user=%s action=%q target=%v), want (user=%s action=%q target=<nil>)",
				call.userID, call.action, call.targetOU, claims.UserID, authz.ActionNodeCreate)
		}
	})
}

func TestAccountGlobalFanOutFiltersPerRecipient(t *testing.T) {
	// Two members of the same account on the same hub: the frame is decided
	// per socket, not once per message, or the first recipient's answer would
	// be everyone's.
	claims := testClaims()
	m := newMembershipTestManager(t, claims)
	imageReader := claims.UserID
	adopter := uuid.New()
	auth := &fakeActionAuthorizer{grant: func(userID uuid.UUID, action authz.Action) bool {
		switch userID {
		case imageReader:
			return action == authz.ActionImageRead
		case adopter:
			return action == authz.ActionNodeCreate
		}
		return false
	}}
	m.SetMemberAuthorizer(auth)

	readerSocket := connectMember(t, m)
	// The fake auth service holds the claims pointer, so repointing the user
	// id makes the next socket belong to the second member.
	claims.UserID = adopter
	adopterSocket := connectMember(t, m)

	deliverAll(m, claims.AccountID)

	if got := receivedTypes(t, readerSocket); strings.Join(got, ",") != "image_created,image_updated,image_deleted" {
		t.Fatalf("image reader received %v", got)
	}
	if got := receivedTypes(t, adopterSocket); strings.Join(got, ",") != "node_adoption_refused" {
		t.Fatalf("adopter received %v", got)
	}
}

func TestAccountGlobalFrameWithoutAnActionIsDropped(t *testing.T) {
	// Fail CLOSED: "no action declared" is the state that used to mean
	// "everyone", so an allow-all enforcer must not be consulted at all — the
	// frame has nothing to ask about.
	claims := testClaims()
	m := newMembershipTestManager(t, claims)
	auth := &fakeActionAuthorizer{grant: grantEverything}
	m.SetMemberAuthorizer(auth)
	c := connectMember(t, m)

	m.fanOutToAccount(websockets3.Message{
		AccountID: &claims.AccountID,
		Payload:   websockets3.MessageWrapper{Type: "image_created", Payload: map[string]string{}},
	})

	if got := receivedTypes(t, c); len(got) != 0 {
		t.Fatalf("an account-global frame with no RequiredAction was delivered: %v", got)
	}
	if len(auth.calls) != 0 {
		t.Fatalf("the enforcer was asked %d times about a frame that declares no action", len(auth.calls))
	}
}

func TestUndeclaredActionIsLoggedOncePerType(t *testing.T) {
	// The drop is a programming error worth a log line, but it fires per
	// recipient per frame; on a busy hub that is a log flood. Once per payload
	// type is enough to find the sender.
	claims := testClaims()
	var buf bytes.Buffer
	m := NewWebSocketManager(fakeAuthService{claims: claims}, make(chan websockets3.Message, 1), nil,
		slog.New(slog.NewTextHandler(&buf, nil)))
	m.SetMemberAuthorizer(&fakeActionAuthorizer{grant: grantEverything})
	connectMember(t, m)

	for i := 0; i < 3; i++ {
		m.fanOutToAccount(websockets3.Message{
			AccountID: &claims.AccountID,
			Payload:   websockets3.MessageWrapper{Type: "image_created", Payload: map[string]string{}},
		})
	}
	m.fanOutToAccount(websockets3.Message{
		AccountID: &claims.AccountID,
		Payload:   websockets3.MessageWrapper{Type: "node_adoption_refused", Payload: map[string]string{}},
	})

	if n := strings.Count(buf.String(), "no RequiredAction"); n != 2 {
		t.Fatalf("logged the undeclared action %d times, want once per type (2):\n%s", n, buf.String())
	}
}

func TestAuthorizerErrorDropsTheAccountGlobalFrame(t *testing.T) {
	claims := testClaims()
	m := newMembershipTestManager(t, claims)
	m.SetMemberAuthorizer(&fakeActionAuthorizer{grant: grantEverything, authorizeErr: errors.New("database is away")})
	c := connectMember(t, m)

	deliverAll(m, claims.AccountID)

	if got := receivedTypes(t, c); len(got) != 0 {
		t.Fatalf("a frame was delivered while the enforcer could not answer: %v", got)
	}
}

func TestNoAuthorizerWiredDropsTheAccountGlobalFrame(t *testing.T) {
	// A socket cannot authorize without an authorizer, so the identity is
	// registered first and the authorizer removed afterwards — the shape of a
	// composition root that wired authorization but forgot the setter.
	claims := testClaims()
	m := newMembershipTestManager(t, claims)
	m.SetMemberAuthorizer(&fakeActionAuthorizer{grant: grantEverything})
	c := connectMember(t, m)
	m.SetMemberAuthorizer(nil)

	deliverAll(m, claims.AccountID)

	if got := receivedTypes(t, c); len(got) != 0 {
		t.Fatalf("frames were delivered with no authorizer wired: %v", got)
	}
}

func TestOUScopedRoutingIsUnchanged(t *testing.T) {
	// The OU branch keys on node.read through AllowedOUs and carries no
	// RequiredAction; it must neither consult Authorize nor be caught by the
	// empty-action drop.
	unit := uuid.New()
	other := uuid.New()
	nodeEvent := func(accountID uuid.UUID, ou *uuid.UUID) websockets3.Message {
		return websockets3.NewAccountMessageForOU(accountID, ou, websockets3.MessageWrapper{
			Type:    "node_updated",
			Payload: map[string]string{},
		})
	}

	cases := []struct {
		name      string
		allowAll  bool
		ous       map[uuid.UUID]bool
		scope     *uuid.UUID
		delivered bool
	}{
		{"account-wide holder, unit", true, nil, &unit, true},
		{"account-wide holder, unassigned", true, nil, nil, true},
		{"OU-scoped holder, own unit", false, map[uuid.UUID]bool{unit: true}, &unit, true},
		{"OU-scoped holder, other unit", false, map[uuid.UUID]bool{unit: true}, &other, false},
		{"OU-scoped holder, unassigned", false, map[uuid.UUID]bool{unit: true}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := testClaims()
			m := newMembershipTestManager(t, claims)
			auth := &fakeActionAuthorizer{grant: nil, allowAllOUs: tc.allowAll, ous: tc.ous}
			m.SetMemberAuthorizer(auth)
			c := connectMember(t, m)

			m.fanOutToAccount(nodeEvent(claims.AccountID, tc.scope))

			got := receivedTypes(t, c)
			if tc.delivered && len(got) != 1 {
				t.Fatalf("received %v, want the node event", got)
			}
			if !tc.delivered && len(got) != 0 {
				t.Fatalf("received %v, want nothing", got)
			}
			if len(auth.calls) != 0 {
				t.Fatalf("the OU branch asked Authorize %d times; it decides by AllowedOUs", len(auth.calls))
			}
		})
	}
}
