package secret

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// seedSecret stores a secret directly, as an account-wide administrator would
// through the secrets API.
func seedSecret(t *testing.T, repo *fakeSecretRepo, accountID uuid.UUID, name, value string, tags ...string) *secret.Secret {
	t.Helper()
	sec, err := secret.NewSecret(accountID, name, secret.KindOpaque, "", value, true)
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	sec.Tags = tags
	if err := repo.Create(context.Background(), sec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return sec
}

func mustAttach(t *testing.T, atts *fakeAttachmentRepo, att *secret.SecretAttachment, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("attachment: %v", err)
	}
	if err := atts.Attach(context.Background(), att); err != nil {
		t.Fatalf("attach: %v", err)
	}
}

// A playbook step writes on behalf of whoever may run the playbook on ONE
// node. A secret that reaches anything else — a unit, another node, the whole
// account through no attachment at all — is managed account-wide, and the step
// must not be able to rewrite it by naming it.
func TestUpsertForNodeRefusesASecretThatReachesBeyondTheRunNode(t *testing.T) {
	runNode, otherNode, ou := uuid.New(), uuid.New(), uuid.New()
	cases := []struct {
		name   string
		attach func(t *testing.T, atts *fakeAttachmentRepo, accountID, secretID uuid.UUID)
	}{
		{"attached to a unit", func(t *testing.T, atts *fakeAttachmentRepo, accountID, secretID uuid.UUID) {
			att, err := secret.NewSecretAttachment(accountID, secretID, &ou, nil)
			mustAttach(t, atts, att, err)
		}},
		{"attached to another node", func(t *testing.T, atts *fakeAttachmentRepo, accountID, secretID uuid.UUID) {
			att, err := secret.NewSecretAttachment(accountID, secretID, nil, &otherNode)
			mustAttach(t, atts, att, err)
		}},
		{"attached to the run node AND a unit", func(t *testing.T, atts *fakeAttachmentRepo, accountID, secretID uuid.UUID) {
			att, err := secret.NewSecretAttachment(accountID, secretID, nil, &runNode)
			mustAttach(t, atts, att, err)
			att, err = secret.NewSecretAttachment(accountID, secretID, &ou, nil)
			mustAttach(t, atts, att, err)
		}},
		{"attached nowhere", func(*testing.T, *fakeAttachmentRepo, uuid.UUID, uuid.UUID) {}},
		{"delivered to a unit through its tag", func(t *testing.T, atts *fakeAttachmentRepo, accountID, secretID uuid.UUID) {
			att, err := secret.NewSecretAttachment(accountID, secretID, nil, &runNode)
			mustAttach(t, atts, att, err)
			tagAtt, err := secret.NewTagAttachment(accountID, "vpn", &ou, nil)
			mustAttach(t, atts, tagAtt, err)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, atts := newTestService()
			accountID := uuid.New()
			orig := seedSecret(t, repo, accountID, "vpn_key", "ORIGINAL", "vpn")
			tc.attach(t, atts, accountID, orig.ID)
			before := len(atts.atts)

			err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, runNode,
				"vpn_key", secret.KindOpaque, "", "REPLACED", secret.UpsertOptions{})
			if !errors.Is(err, app_errors.ErrSecretOutsideRunNode) {
				t.Fatalf("got %v, want ErrSecretOutsideRunNode", err)
			}
			got, _ := repo.GetByID(context.Background(), accountID, orig.ID)
			if got.Value != "ORIGINAL" {
				t.Errorf("value was rewritten to %q", got.Value)
			}
			if len(atts.atts) != before {
				t.Errorf("attachments changed: %d -> %d", before, len(atts.atts))
			}
			all, _ := repo.GetAllByAccountID(context.Background(), accountID)
			if len(all) != 1 {
				t.Errorf("a refused write must not create a second secret; have %d", len(all))
			}
		})
	}
}

// A NEW secret must not be tagged into another scope's delivery: a tag that is
// attached somewhere else would carry the step's value to those nodes.
func TestUpsertForNodeRefusesATagDeliveredBeyondTheRunNode(t *testing.T) {
	svc, repo, atts := newTestService()
	accountID, runNode, ou := uuid.New(), uuid.New(), uuid.New()
	tagAtt, err := secret.NewTagAttachment(accountID, "fleet", &ou, nil)
	mustAttach(t, atts, tagAtt, err)

	err = svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, runNode,
		"fresh", secret.KindOpaque, "", "v", secret.UpsertOptions{Tags: []string{"Fleet"}})
	if !errors.Is(err, app_errors.ErrSecretOutsideRunNode) {
		t.Fatalf("got %v, want ErrSecretOutsideRunNode", err)
	}
	if all, _ := repo.GetAllByAccountID(context.Background(), accountID); len(all) != 0 {
		t.Fatalf("a refused write created %d secret(s)", len(all))
	}

	// A tag attached only to the run node is the run node's own business.
	own, err := secret.NewTagAttachment(accountID, "mine", nil, &runNode)
	mustAttach(t, atts, own, err)
	if err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, runNode,
		"fresh", secret.KindOpaque, "", "v", secret.UpsertOptions{Tags: []string{"mine"}}); err != nil {
		t.Fatalf("a tag delivered only to the run node must be accepted: %v", err)
	}
}

// The reserved node-attribute names encode a node id. From a playbook step
// they would reach another node's attribute without the per-node permission
// the attribute API asks for, so they are refused outright — the attribute
// block is the way to write the run node's own.
func TestUpsertForNodeRefusesReservedNames(t *testing.T) {
	svc, repo, atts := newTestService()
	accountID, runNode, otherNode := uuid.New(), uuid.New(), uuid.New()

	// The other node's attribute, as the attribute API stores it.
	attr := seedSecret(t, repo, accountID, secret.NodeAttributeName(otherNode, "TOKEN"), "THEIRS")
	att, err := secret.NewSecretAttachment(accountID, attr.ID, nil, &otherNode)
	mustAttach(t, atts, att, err)

	for _, name := range []string{
		secret.NodeAttributeName(otherNode, "TOKEN"),
		secret.NodeAttributeName(runNode, "TOKEN"),
		"  attr:anything",
		"bad}name",
	} {
		err := svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, runNode,
			name, secret.KindOpaque, "", "MINE", secret.UpsertOptions{})
		if err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
	got, _ := repo.GetByID(context.Background(), accountID, attr.ID)
	if got.Value != "THEIRS" {
		t.Fatalf("another node's attribute was rewritten to %q", got.Value)
	}
}

// The refusal must say what to do, because it surfaces as a failed block in
// the run view.
func TestUpsertForNodeRefusalNamesTheSecret(t *testing.T) {
	svc, repo, atts := newTestService()
	accountID, runNode, ou := uuid.New(), uuid.New(), uuid.New()
	orig := seedSecret(t, repo, accountID, "shared", "x")
	att, err := secret.NewSecretAttachment(accountID, orig.ID, &ou, nil)
	mustAttach(t, atts, att, err)

	err = svc.UpsertForNode(context.Background(), audit.SystemActor(accountID), accountID, runNode,
		"shared", secret.KindOpaque, "", "y", secret.UpsertOptions{})
	if err == nil || !strings.Contains(err.Error(), `"shared"`) {
		t.Fatalf("got %v, want an error naming the secret", err)
	}
}
