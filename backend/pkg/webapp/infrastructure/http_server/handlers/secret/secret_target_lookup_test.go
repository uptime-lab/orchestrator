package secret

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	secret_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret"
	secret_tag_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret_tag"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

type ouListingAttachmentRepo struct {
	*fakeAttachmentRepo
}

func (f ouListingAttachmentRepo) ListByOU(_ context.Context, accountID, ouID uuid.UUID) ([]secret.SecretAttachment, error) {
	var out []secret.SecretAttachment
	for _, a := range f.atts {
		if a.AccountID == accountID && a.OUID != nil && *a.OUID == ouID {
			out = append(out, a)
		}
	}
	return out, nil
}

// newTargetLookupEnv is an account-wide super_admin (who may read secret
// values) of an account holding one unit and one node, beside a second
// account holding its own unit and node.
func newTargetLookupEnv(t *testing.T) (attrTestEnv, uuid.UUID, uuid.UUID) {
	t.Helper()
	accountID, userID := uuid.New(), uuid.New()
	otherAccount := uuid.New()
	ownOU, foreignOU := uuid.New(), uuid.New()
	ownNode, foreignNode := uuid.New(), uuid.New()
	nodes := map[uuid.UUID]*node.Node{
		ownNode:     {ID: ownNode, AccountID: accountID, OUID: &ownOU},
		foreignNode: {ID: foreignNode, AccountID: otherAccount},
	}
	ous := fakeOURepo{ous: []organizational_unit.OrganizationalUnit{
		{ID: ownOU, AccountID: accountID, Name: "own"},
		{ID: foreignOU, AccountID: otherAccount, Name: "foreign"},
	}}

	recorder, _ := audittest.New()
	enforcer := authz_service.NewEnforcer(
		fakeMemberRepo{member: account_member.NewAccountMember(accountID, userID)},
		fakeBindingRepo{bindings: []role_binding.RoleBinding{
			*role_binding.NewRoleBinding(accountID, userID, authz.RoleSuperAdmin, nil, nil),
		}},
		fakeRoleSettingRepo{}, ous, recorder, discardLogger(),
	)
	svc := secret_service.NewService(newFakeSecretRepo(), ouListingAttachmentRepo{&fakeAttachmentRepo{}},
		fakeNodeRepo{nodes: nodes}, ous, recorder, discardLogger())
	handlerRecorder, sink := audittest.New()
	h := NewSecretHandler(svc, secret_tag_service.NewService(fakeTagRepo{}, discardLogger()),
		fakeNodeReader{nodes: nodes}, handlerRecorder, discardLogger())

	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", userID.String())
			c.Set("accountID", accountID.String())
			return next(c)
		}
	}
	h.RegisterRoutes(e, auth, enforcer)
	env := attrTestEnv{e: e, accountID: accountID, userID: userID, ouA: ownOU, ouB: foreignOU,
		nodeA: ownNode, nodeB: foreignNode, audit: sink}
	return env, foreignNode, foreignOU
}

// A secret list scoped to a node or unit resolves that target in the caller's
// account first: another account's id and an id that does not exist both
// answer 404 — not an empty 200 — and write no "plaintext served" row.
func TestSecretListsRefuseTargetsOutsideTheAccount(t *testing.T) {
	env, foreignNode, foreignOU := newTargetLookupEnv(t)
	for _, path := range []string{
		"/api/v1/nodes/" + foreignNode.String() + "/secrets",
		"/api/v1/nodes/" + uuid.NewString() + "/secrets",
		"/api/v1/organizational-units/" + foreignOU.String() + "/secrets",
		"/api/v1/organizational-units/" + uuid.NewString() + "/secrets",
	} {
		if rec := env.do(http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (body: %s)", path, rec.Code, rec.Body.String())
		}
	}
	if got := env.audit.Actions(); len(got) != 0 {
		t.Fatalf("refused lists recorded %v", got)
	}
}

// A list that carries no value served no plaintext, so it records no read
// even for a caller who may read values.
func TestSecretListWithoutValuesRecordsNoRead(t *testing.T) {
	env, _, _ := newTargetLookupEnv(t)
	for _, path := range []string{
		"/api/v1/nodes/" + env.nodeA.String() + "/secrets",
		"/api/v1/organizational-units/" + env.ouA.String() + "/secrets",
	} {
		if rec := env.do(http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (body: %s)", path, rec.Code, rec.Body.String())
		}
	}
	if _, ok := env.audit.Find(audit_event.ActionSecretValuesRead); ok {
		t.Fatal("an empty list recorded a secret-values read")
	}
}

// Attaching to a target outside the account is an ordinary "not found", not a
// server fault: the lookup's own 404 reaches the caller through a handler that
// has no arm for it.
func TestAttachingToATargetOutsideTheAccountIs404(t *testing.T) {
	env, foreignNode, foreignOU := newTargetLookupEnv(t)
	for _, body := range []string{
		`{"node_id":"` + foreignNode.String() + `"}`,
		`{"ou_id":"` + foreignOU.String() + `"}`,
		`{"node_id":"` + uuid.NewString() + `"}`,
	} {
		rec := env.do(http.MethodPost, "/api/v1/secret-tags/fleet/attachments", body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("attach %s = %d, want 404 (body: %s)", body, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{
		"/api/v1/nodes/" + foreignNode.String() + "/secret-attachments",
		"/api/v1/organizational-units/" + foreignOU.String() + "/secret-attachments",
	} {
		rec := env.do(http.MethodPut, path, `{"secret_ids":[],"tags":[]}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("PUT %s = %d, want 404 (body: %s)", path, rec.Code, rec.Body.String())
		}
	}
}

// The direct-attachment list is what an editor changes one attachment from. A
// tag no secret carries delivers nothing, so it is absent from the effective
// set; it must still be listed here, with the id DELETE takes. A target
// outside the account is a 404.
func TestTargetAttachmentListIncludesTagsNoSecretCarries(t *testing.T) {
	env, foreignNode, foreignOU := newTargetLookupEnv(t)
	for _, tc := range []struct{ path, body string }{
		{"/api/v1/nodes/" + env.nodeA.String() + "/secret-attachments", `{"node_id":"` + env.nodeA.String() + `"}`},
		{"/api/v1/organizational-units/" + env.ouA.String() + "/secret-attachments", `{"ou_id":"` + env.ouA.String() + `"}`},
	} {
		if rec := env.do(http.MethodPost, "/api/v1/secret-tags/db/attachments", tc.body); rec.Code != http.StatusCreated {
			t.Fatalf("attach tag = %d (body: %s)", rec.Code, rec.Body.String())
		}
		rec := env.do(http.MethodGet, tc.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d (body: %s)", tc.path, rec.Code, rec.Body.String())
		}
		var resp struct {
			Data struct {
				Attachments []struct {
					ID  string `json:"id"`
					Tag string `json:"tag"`
				} `json:"attachments"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Data.Attachments) != 1 || resp.Data.Attachments[0].Tag != "db" || resp.Data.Attachments[0].ID == "" {
			t.Fatalf("GET %s listed %+v, want the one tag attachment", tc.path, resp.Data.Attachments)
		}
	}
	for _, path := range []string{
		"/api/v1/nodes/" + foreignNode.String() + "/secret-attachments",
		"/api/v1/organizational-units/" + foreignOU.String() + "/secret-attachments",
		"/api/v1/nodes/" + uuid.NewString() + "/secret-attachments",
	} {
		if rec := env.do(http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (body: %s)", path, rec.Code, rec.Body.String())
		}
	}
}
