package websockets

import (
	"testing"

	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"

	"github.com/google/uuid"
)

// receivedPayloads drains the frames queued for a socket and returns their
// MessageWrapper payloads in order.
func receivedPayloads(t *testing.T, c *Client) []interface{} {
	t.Helper()
	var payloads []interface{}
	for {
		select {
		case frame := <-c.toClient:
			wrapper, ok := frame.(websockets3.MessageWrapper)
			if !ok {
				t.Fatalf("frame is a %T, want a MessageWrapper", frame)
			}
			payloads = append(payloads, wrapper.Payload)
		default:
			return payloads
		}
	}
}

// A unit's resource whose REST read demands an action of its own (a playbook
// run: playbook.read) must reach only node.read holders of the unit who also
// hold that action, asked the way RequireAction asks it: nil target.
func TestOUScopedFrameWithARouteActionNeedsBoth(t *testing.T) {
	unit := uuid.New()
	cases := []struct {
		name      string
		ous       map[uuid.UUID]bool
		grant     func(uuid.UUID, authz.Action) bool
		delivered bool
	}{
		{"unit reader with the action", map[uuid.UUID]bool{unit: true}, grantOnly(authz.ActionPlaybookRead), true},
		{"unit reader without the action", map[uuid.UUID]bool{unit: true}, grantOnly(), false},
		{"action holder outside the unit", map[uuid.UUID]bool{}, grantOnly(authz.ActionPlaybookRead), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := testClaims()
			m := newMembershipTestManager(t, claims)
			auth := &fakeActionAuthorizer{grant: tc.grant, ous: tc.ous}
			m.SetMemberAuthorizer(auth)
			c := connectMember(t, m)

			m.fanOutToAccount(websockets3.NewAccountMessageForOU(claims.AccountID, &unit, websockets3.MessageWrapper{
				Type:    "workflow_execution_updated",
				Payload: "run",
			}).RequiringAction(authz.ActionPlaybookRead))

			got := receivedTypes(t, c)
			if tc.delivered != (len(got) == 1) {
				t.Fatalf("received %v, delivered want %v", got, tc.delivered)
			}
			for _, call := range auth.calls {
				if call.targetOU != nil {
					t.Fatalf("the route action must be asked with a nil target, got %v", call.targetOU)
				}
			}
		})
	}
}

// A frame split by WithRedactedVariant carries the full payload only to a
// recipient holding the full-payload action account-wide; everyone else the
// message reaches gets the redacted variant, never the full one.
func TestRedactedVariantIsChosenPerRecipient(t *testing.T) {
	unit := uuid.New()
	msg := func(accountID uuid.UUID, redacted websockets3.MessagePayload) websockets3.Message {
		return websockets3.NewAccountMessageForOU(accountID, &unit, websockets3.MessageWrapper{
			Type:    "workflow_execution_updated",
			Payload: "full",
		}).WithRedactedVariant(authz.ActionSecretReadValue, redacted)
	}
	redacted := websockets3.MessageWrapper{Type: "workflow_execution_updated", Payload: "redacted"}

	cases := []struct {
		name     string
		grant    func(uuid.UUID, authz.Action) bool
		redacted websockets3.MessagePayload
		want     []interface{}
	}{
		{"privileged recipient", grantOnly(authz.ActionSecretReadValue), redacted, []interface{}{"full"}},
		{"unprivileged recipient", grantOnly(), redacted, []interface{}{"redacted"}},
		{"unprivileged recipient, no variant", grantOnly(), nil, nil},
		{"privileged recipient, no variant", grantOnly(authz.ActionSecretReadValue), nil, []interface{}{"full"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := testClaims()
			m := newMembershipTestManager(t, claims)
			m.SetMemberAuthorizer(&fakeActionAuthorizer{grant: tc.grant, allowAllOUs: true})
			c := connectMember(t, m)

			m.fanOutToAccount(msg(claims.AccountID, tc.redacted))

			got := receivedPayloads(t, c)
			if len(got) != len(tc.want) {
				t.Fatalf("received %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("received %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The SSH-adopt job frame follows GET /node-adoption-jobs/:id, gated on
// node.ssh_adopt: an account-wide reader without that action receives nothing.
func TestAdoptionJobFrameNeedsSSHAdopt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		grant     func(uuid.UUID, authz.Action) bool
		delivered bool
	}{
		{"viewer", grantOnly(authz.ActionNodeRead), false},
		{"adopter", grantOnly(authz.ActionNodeRead, authz.ActionNodeSSHAdopt), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := testClaims()
			m := newMembershipTestManager(t, claims)
			m.SetMemberAuthorizer(&fakeActionAuthorizer{grant: tc.grant, allowAllOUs: true})
			c := connectMember(t, m)

			m.fanOutToAccount(websockets3.NewAccountMessage(claims.AccountID, authz.ActionNodeSSHAdopt, websockets3.MessageWrapper{
				Type:    "node_adoption_job_updated",
				Payload: map[string]string{"host": "device.invalid"},
			}))

			if got := receivedTypes(t, c); tc.delivered != (len(got) == 1) {
				t.Fatalf("received %v, delivered want %v", got, tc.delivered)
			}
		})
	}
}
