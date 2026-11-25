package secret

// Regression tests: the node-attribute routes must enforce the
// node's OU scope. A member whose role bindings are scoped to OU A must not be
// able to read or write attributes of a node in OU B — and must not even learn
// that the node exists (deny -> 404). Within their OU, reads need node.read
// and writes need node.attributes_write (a viewer gets an honest 403).

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	secret_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret"
	secret_tag_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret_tag"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- enforcer fakes (only the methods the Enforcer calls are implemented;
// anything else panics via the embedded nil interface) ---

type fakeMemberRepo struct {
	repository.AccountMemberRepository
	member *account_member.AccountMember
}

func (f fakeMemberRepo) GetByAccountAndUser(_ context.Context, accountID, userID uuid.UUID) (*account_member.AccountMember, error) {
	if f.member != nil && f.member.AccountID == accountID && f.member.UserID == userID {
		return f.member, nil
	}
	return nil, app_errors.ErrNotAMember
}

type fakeBindingRepo struct {
	repository.RoleBindingRepository
	bindings []role_binding.RoleBinding
}

func (f fakeBindingRepo) ListByAccountAndUser(_ context.Context, _, _ uuid.UUID) ([]role_binding.RoleBinding, error) {
	return f.bindings, nil
}

type fakeRoleSettingRepo struct {
	repository.AccountRoleSettingRepository
}

func (f fakeRoleSettingRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]account_role_setting.AccountRoleSetting, error) {
	return nil, nil
}

type fakeOURepo struct {
	repository.OrganizationalUnitRepository
	ous []organizational_unit.OrganizationalUnit
}

func (f fakeOURepo) GetAllByAccountID(_ context.Context, _ uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return f.ous, nil
}

func (f fakeOURepo) GetByID(_ context.Context, accountID, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	for i := range f.ous {
		if f.ous[i].AccountID == accountID && f.ous[i].ID == id {
			out := f.ous[i]
			return &out, nil
		}
	}
	return nil, app_errors.ErrOrganizationalUnitNotFound
}

// --- node fakes ---

type fakeNodeReader struct {
	nodes map[uuid.UUID]*node.Node
}

func (f fakeNodeReader) GetByIDAndAccount(_ context.Context, nodeID, accountID uuid.UUID) (*node.Node, error) {
	if n, ok := f.nodes[nodeID]; ok && n.AccountID == accountID {
		return n, nil
	}
	return nil, app_errors.ErrNodeNotFound
}

type fakeNodeRepo struct {
	repository.NodeRepository
	nodes map[uuid.UUID]*node.Node
}

func (f fakeNodeRepo) GetByID(_ context.Context, accountID, nodeID uuid.UUID) (*node.Node, error) {
	if n, ok := f.nodes[nodeID]; ok && n.AccountID == accountID {
		return n, nil
	}
	return nil, app_errors.ErrNodeNotFound
}

// --- secret service fakes (in-memory, only the attribute paths) ---

type fakeSecretRepo struct {
	repository.SecretRepository
	byID map[uuid.UUID]secret.Secret
}

func newFakeSecretRepo() *fakeSecretRepo {
	return &fakeSecretRepo{byID: make(map[uuid.UUID]secret.Secret)}
}

func (f *fakeSecretRepo) Create(_ context.Context, s *secret.Secret) error {
	f.byID[s.ID] = *s
	return nil
}

// CreateUnderCap: these tests set no cap, so it is the plain insert.
func (f *fakeSecretRepo) CreateUnderCap(ctx context.Context, s *secret.Secret, _ int) error {
	return f.Create(ctx, s)
}

func (f *fakeSecretRepo) Update(_ context.Context, s *secret.Secret) error {
	f.byID[s.ID] = *s
	return nil
}

func (f *fakeSecretRepo) GetByName(_ context.Context, accountID uuid.UUID, name string) (*secret.Secret, error) {
	for _, s := range f.byID {
		if s.AccountID == accountID && s.Name == name {
			out := s
			return &out, nil
		}
	}
	return nil, app_errors.ErrSecretNotFound
}

func (f *fakeSecretRepo) GetByIDs(_ context.Context, accountID uuid.UUID, ids []uuid.UUID) ([]secret.Secret, error) {
	var out []secret.Secret
	for _, id := range ids {
		if s, ok := f.byID[id]; ok && s.AccountID == accountID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeSecretRepo) Delete(_ context.Context, accountID, id uuid.UUID) error {
	if s, ok := f.byID[id]; ok && s.AccountID == accountID {
		delete(f.byID, id)
		return nil
	}
	return app_errors.ErrSecretNotFound
}

type fakeAttachmentRepo struct {
	repository.SecretAttachmentRepository
	atts []secret.SecretAttachment
}

func (f *fakeAttachmentRepo) Attach(_ context.Context, att *secret.SecretAttachment) error {
	f.atts = append(f.atts, *att)
	return nil
}

func (f *fakeAttachmentRepo) ListByNode(_ context.Context, accountID, nodeID uuid.UUID) ([]secret.SecretAttachment, error) {
	var out []secret.SecretAttachment
	for _, a := range f.atts {
		if a.AccountID == accountID && a.NodeID != nil && *a.NodeID == nodeID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeAttachmentRepo) DeleteBySecret(_ context.Context, accountID, secretID uuid.UUID) error {
	kept := f.atts[:0]
	for _, a := range f.atts {
		if !(a.AccountID == accountID && a.SecretID != nil && *a.SecretID == secretID) {
			kept = append(kept, a)
		}
	}
	f.atts = kept
	return nil
}

type fakeTagRepo struct {
	repository.SecretTagRepository
}

// --- test scaffolding ---

type attrTestEnv struct {
	e         *echo.Echo
	accountID uuid.UUID
	userID    uuid.UUID
	ouA       uuid.UUID
	ouB       uuid.UUID
	nodeA     uuid.UUID
	nodeB     uuid.UUID
	// audit captures what the handler recorded, so a route test can assert the
	// trail row as well as the response.
	audit *audittest.Sink
}

// bindingOU returns the OU a binding is scoped to, or nil for account-wide.
func bindingOU(ou *uuid.UUID, scoped bool) *uuid.UUID {
	if scoped {
		return ou
	}
	return nil
}

// newAttrTestEnv wires the secret handler with a real secret service over
// in-memory fakes and a real enforcer over fake membership/binding repos: the
// caller is an active member holding the given role scoped to OU A. Node A
// lives in OU A, node B in OU B (same account).
func newAttrTestEnv(t *testing.T, role authz.Role) attrTestEnv {
	t.Helper()
	return newAttrTestEnvScoped(t, role, true)
}

// newAttrTestEnvScoped is newAttrTestEnv with the binding's scope chosen.
//
// The distinction matters for anything that asks a NIL-target question — the
// plaintext gate does, and an OU-scoped binding never satisfies one, so a test
// wanting a caller who really is served values has to bind account-wide.
func newAttrTestEnvScoped(t *testing.T, role authz.Role, ouScoped bool) attrTestEnv {
	t.Helper()

	accountID := uuid.New()
	userID := uuid.New()
	ouA := uuid.New()
	ouB := uuid.New()
	nodeAID := uuid.New()
	nodeBID := uuid.New()

	nodes := map[uuid.UUID]*node.Node{
		nodeAID: {ID: nodeAID, AccountID: accountID, OUID: &ouA},
		nodeBID: {ID: nodeBID, AccountID: accountID, OUID: &ouB},
	}

	auditRecorder, _ := audittest.New()
	enforcer := authz_service.NewEnforcer(
		fakeMemberRepo{member: account_member.NewAccountMember(accountID, userID)},
		fakeBindingRepo{bindings: []role_binding.RoleBinding{
			*role_binding.NewRoleBinding(accountID, userID, role, bindingOU(&ouA, ouScoped), nil),
		}},
		fakeRoleSettingRepo{},
		fakeOURepo{ous: []organizational_unit.OrganizationalUnit{
			{ID: ouA, AccountID: accountID, Name: "A"},
			{ID: ouB, AccountID: accountID, Name: "B"},
		}},
		auditRecorder,
		discardLogger(),
	)

	svc := secret_service.NewService(
		newFakeSecretRepo(),
		&fakeAttachmentRepo{},
		fakeNodeRepo{nodes: nodes},
		fakeOURepo{},
		auditRecorder,
		discardLogger(),
	)
	tagSvc := secret_tag_service.NewService(fakeTagRepo{}, discardLogger())
	handlerRecorder, handlerSink := audittest.New()
	h := NewSecretHandler(svc, tagSvc, fakeNodeReader{nodes: nodes}, handlerRecorder, discardLogger())

	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", userID.String())
			c.Set("accountID", accountID.String())
			return next(c)
		}
	}
	h.RegisterRoutes(e, auth, enforcer)

	return attrTestEnv{e: e, accountID: accountID, userID: userID, ouA: ouA, ouB: ouB, nodeA: nodeAID, nodeB: nodeBID, audit: handlerSink}
}

func (env attrTestEnv) do(method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	return rec
}

// TestNodeAttributesCrossOUDenied404 is the cross-OU regression: an admin
// whose binding is scoped to OU A must get 404 (not data, not 403) for the
// attributes of a node in OU B — GET, PUT and DELETE alike.
func TestNodeAttributesCrossOUDenied404(t *testing.T) {
	env := newAttrTestEnv(t, authz.RoleAdmin)
	base := "/api/v1/nodes/" + env.nodeB.String() + "/attributes"

	if rec := env.do(http.MethodGet, base, ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET cross-OU attributes: got %d, want 404", rec.Code)
	}
	if rec := env.do(http.MethodPut, base+"/CI_TOKEN", `{"value":"v1"}`); rec.Code != http.StatusNotFound {
		t.Errorf("PUT cross-OU attribute: got %d, want 404", rec.Code)
	}
	if rec := env.do(http.MethodDelete, base+"/CI_TOKEN", ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE cross-OU attribute: got %d, want 404", rec.Code)
	}
}

// TestNodeAttributesOwnOUAllowed: the same OU-A-scoped admin can manage the
// attributes of a node inside OU A (PUT -> GET -> DELETE round trip).
func TestNodeAttributesOwnOUAllowed(t *testing.T) {
	env := newAttrTestEnv(t, authz.RoleAdmin)
	base := "/api/v1/nodes/" + env.nodeA.String() + "/attributes"

	if rec := env.do(http.MethodPut, base+"/CI_TOKEN", `{"value":"v1"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT own-OU attribute: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	rec := env.do(http.MethodGet, base, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET own-OU attributes: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CI_TOKEN") {
		t.Errorf("GET own-OU attributes: response does not contain the created key: %s", rec.Body.String())
	}
	if rec := env.do(http.MethodDelete, base+"/CI_TOKEN", ""); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE own-OU attribute: got %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestNodeAttributesViewerReadOnlyWrite403: a viewer scoped to OU A can read
// the attributes of an OU A node but gets an honest 403 (the node is visible
// to them, so 404 would be wrong) on writes — node.attributes_write is admin+.
func TestNodeAttributesViewerReadOnlyWrite403(t *testing.T) {
	env := newAttrTestEnv(t, authz.RoleViewer)
	base := "/api/v1/nodes/" + env.nodeA.String() + "/attributes"

	if rec := env.do(http.MethodGet, base, ""); rec.Code != http.StatusOK {
		t.Errorf("GET as viewer in own OU: got %d, want 200", rec.Code)
	}
	if rec := env.do(http.MethodPut, base+"/CI_TOKEN", `{"value":"v1"}`); rec.Code != http.StatusForbidden {
		t.Errorf("PUT as viewer: got %d, want 403", rec.Code)
	}
	if rec := env.do(http.MethodDelete, base+"/CI_TOKEN", ""); rec.Code != http.StatusForbidden {
		t.Errorf("DELETE as viewer: got %d, want 403", rec.Code)
	}
	// And the cross-OU read stays hidden for viewers too.
	if rec := env.do(http.MethodGet, "/api/v1/nodes/"+env.nodeB.String()+"/attributes", ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET cross-OU as viewer: got %d, want 404", rec.Code)
	}
}

// Serving plaintext must leave a row, and the row must be written from the ROUTE
// — the helper test in the audit package cannot catch a handler that forgets to
// call it.
//
// secret.read_value gated the plaintext and recorded nothing, so a holder could
// page an account's values and leave no trace.
func TestReadingAttributeValuesIsAudited(t *testing.T) {
	// Account-wide: the plaintext gate asks a nil-target question, which an
	// OU-scoped binding never satisfies — so an OU-scoped admin is served no
	// values and correctly writes no row.
	env := newAttrTestEnvScoped(t, authz.RoleAdmin, false)

	rec := env.do(http.MethodGet, "/api/v1/nodes/"+env.nodeA.String()+"/attributes", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET attributes = %d, want 200", rec.Code)
	}

	event, ok := env.audit.Find(audit_event.ActionSecretValuesRead)
	if !ok {
		t.Fatalf("no %s row was written; actions seen: %v",
			audit_event.ActionSecretValuesRead, env.audit.Actions())
	}
	if event.TargetID != env.nodeA.String() {
		t.Errorf("target_id = %q, want the node the values were read for", event.TargetID)
	}
}

// A caller who cannot see values writes no row: they read nothing.
func TestAViewerReadingAttributesIsNotAudited(t *testing.T) {
	env := newAttrTestEnv(t, authz.RoleViewer)

	env.do(http.MethodGet, "/api/v1/nodes/"+env.nodeA.String()+"/attributes", "")

	if _, ok := env.audit.Find(audit_event.ActionSecretValuesRead); ok {
		t.Error("a caller who was served no plaintext produced a secret-values-read row")
	}
}
