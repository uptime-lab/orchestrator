package organizational_unit

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/google/uuid"
)

// The stubs below embed their repository interface so any method the code under
// test calls unexpectedly panics on the nil embedded interface instead of
// silently returning a zero value.

// The plain OU repository no longer takes part in a delete: the guards and the
// delete moved into OUMutationRepository so they share one transaction. It stays
// wired because the cascade and the rest of the service still read through it.
type stubOURepo struct {
	repository.OrganizationalUnitRepository
}

// GetByID satisfies the best-effort name read Delete performs for its audit
// detail; "not found" exercises the read failing without failing the delete.
func (s *stubOURepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	return nil, app_errors.ErrOrganizationalUnitNotFound
}

// stubOUMutationRepo stands in for the guarded delete. The service no longer
// runs the emptiness guards itself — they moved inside the deleting
// transaction, where the check and the delete cannot be raced apart — so a unit
// test drives this repository's verdict rather than the individual counts.
type stubOUMutationRepo struct {
	repository.OUMutationRepository
	err      error
	deleted  []uuid.UUID
	assigned []uuid.UUID
}

func (r *stubOUMutationRepo) DeleteIfUnreferenced(_ context.Context, _, ouID uuid.UUID) error {
	if r.err != nil {
		return r.err
	}
	r.deleted = append(r.deleted, ouID)
	return nil
}

func (r *stubOUMutationRepo) AssignNodeOU(_ context.Context, _, nodeID uuid.UUID, _ *uuid.UUID) error {
	if r.err != nil {
		return r.err
	}
	r.assigned = append(r.assigned, nodeID)
	return nil
}

type stubNodeRepo struct {
	repository.NodeRepository
	nodes []node.Node
}

func (r *stubNodeRepo) GetAllByAccountID(_ context.Context, _ uuid.UUID) ([]node.Node, error) {
	return r.nodes, nil
}

type stubTemplateRepo struct {
	repository.WorkflowTemplateRepository
	count      int64
	deletedOUs []uuid.UUID
}

func (r *stubTemplateRepo) CountByOUID(_ context.Context, _, _ uuid.UUID) (int64, error) {
	return r.count, nil
}

func (r *stubTemplateRepo) DeleteScopeByOUID(_ context.Context, _, ouID uuid.UUID) error {
	r.deletedOUs = append(r.deletedOUs, ouID)
	return nil
}

type stubBindingRepo struct {
	repository.RoleBindingRepository
	bindings   []role_binding.RoleBinding
	deletedOUs []uuid.UUID
}

func (r *stubBindingRepo) ListByAccountID(_ context.Context, _ uuid.UUID) ([]role_binding.RoleBinding, error) {
	return r.bindings, nil
}

func (r *stubBindingRepo) DeleteByOUID(_ context.Context, _, ouID uuid.UUID) error {
	r.deletedOUs = append(r.deletedOUs, ouID)
	return nil
}

type stubInviteRepo struct {
	repository.AccountInviteRepository
	deletedOUs []uuid.UUID
}

func (r *stubInviteRepo) DeleteByOUID(_ context.Context, _, ouID uuid.UUID) error {
	r.deletedOUs = append(r.deletedOUs, ouID)
	return nil
}

type stubAttachmentRepo struct {
	repository.SecretAttachmentRepository
	deletedOUs []uuid.UUID
	err        error
}

func (r *stubAttachmentRepo) DeleteByOUID(_ context.Context, _, ouID uuid.UUID) error {
	if r.err != nil {
		return r.err
	}
	r.deletedOUs = append(r.deletedOUs, ouID)
	return nil
}

type stubMasterTokenRepo struct {
	repository.MasterTokenRepository
	deletedOUs []uuid.UUID
}

func (r *stubMasterTokenRepo) DeleteByOUID(_ context.Context, _, ouID uuid.UUID) error {
	r.deletedOUs = append(r.deletedOUs, ouID)
	return nil
}

type recordingInvalidator struct {
	users []uuid.UUID
}

func (i *recordingInvalidator) Invalidate(_, userID uuid.UUID) {
	i.users = append(i.users, userID)
}

type deleteFixture struct {
	svc         *Service
	ou          *stubOURepo
	bindings    *stubBindingRepo
	invites     *stubInviteRepo
	attachments *stubAttachmentRepo
	tokens      *stubMasterTokenRepo
	templates   *stubTemplateRepo
	nodes       *stubNodeRepo
	mutations   *stubOUMutationRepo
	invalidator *recordingInvalidator
	accountID   uuid.UUID
	ouID        uuid.UUID
}

func newDeleteFixture() *deleteFixture {
	f := &deleteFixture{
		ou:          &stubOURepo{},
		bindings:    &stubBindingRepo{},
		invites:     &stubInviteRepo{},
		attachments: &stubAttachmentRepo{},
		tokens:      &stubMasterTokenRepo{},
		templates:   &stubTemplateRepo{},
		nodes:       &stubNodeRepo{},
		mutations:   &stubOUMutationRepo{},
		invalidator: &recordingInvalidator{},
		accountID:   uuid.New(),
		ouID:        uuid.New(),
	}
	recorder, _ := testAuditRecorder()
	f.svc = &Service{
		ouRepo:          f.ou,
		ouMutationRepo:  f.mutations,
		nodeRepo:        f.nodes,
		attachmentRepo:  f.attachments,
		bindingRepo:     f.bindings,
		inviteRepo:      f.invites,
		masterTokenRepo: f.tokens,
		templateRepo:    f.templates,
		invalidator:     f.invalidator,
		audit:           recorder,
		logger:          slog.Default(),
	}
	return f
}

func (f *deleteFixture) delete() error {
	return f.svc.Delete(context.Background(), organizational_unit_delete.OrganizationalUnitDeleteCommand{
		ID:          f.ouID,
		AuthContext: commands.NewAuthContext(f.accountID, uuid.New()),
	})
}

func TestDeleteCascadesOUScopedReferences(t *testing.T) {
	f := newDeleteFixture()
	scopedUser := uuid.New()
	otherOU := uuid.New()
	f.bindings.bindings = []role_binding.RoleBinding{
		{UserID: scopedUser, OUID: &f.ouID},
		{UserID: scopedUser, OUID: &f.ouID}, // duplicate: must invalidate once
		{UserID: uuid.New(), OUID: &otherOU},
		{UserID: uuid.New(), OUID: nil}, // account-wide: untouched
	}

	if err := f.delete(); err != nil {
		t.Fatalf("delete: %v", err)
	}

	for _, c := range []struct {
		name string
		got  []uuid.UUID
	}{
		{"bindings", f.bindings.deletedOUs},
		{"invites", f.invites.deletedOUs},
		{"attachments", f.attachments.deletedOUs},
		{"master tokens", f.tokens.deletedOUs},
	} {
		if len(c.got) != 1 || c.got[0] != f.ouID {
			t.Errorf("%s: expected one cascade delete for %s, got %v", c.name, f.ouID, c.got)
		}
	}

	if len(f.invalidator.users) != 1 || f.invalidator.users[0] != scopedUser {
		t.Errorf("expected the OU-scoped member to be invalidated once, got %v", f.invalidator.users)
	}
}

func TestDeleteRefusesWhenPlaybooksAreScopedToTheUnit(t *testing.T) {
	f := newDeleteFixture()
	f.mutations.err = app_errors.ErrOrganizationalUnitHasPlaybooks

	err := f.delete()

	if !errors.Is(err, app_errors.ErrOrganizationalUnitHasPlaybooks) {
		t.Fatalf("expected ErrOrganizationalUnitHasPlaybooks, got %v", err)
	}
	if len(f.mutations.deleted) != 0 {
		t.Error("the unit must not be deleted while playbooks are scoped to it")
	}
	if len(f.bindings.deletedOUs) != 0 {
		t.Error("a refused delete must not cascade")
	}
}

func TestDeleteDoesNotCascadeWhenTheUnitItselfCannotBeDeleted(t *testing.T) {
	f := newDeleteFixture()
	f.mutations.err = errors.New("boom")

	if err := f.delete(); err == nil {
		t.Fatal("expected the repository error to propagate")
	}
	if len(f.bindings.deletedOUs) != 0 {
		t.Error("bindings must survive a failed unit delete — stripping access from a live OU is worse than a dangling row")
	}
}

func TestDeleteSucceedsWhenACascadeStepFails(t *testing.T) {
	f := newDeleteFixture()
	f.attachments.err = errors.New("boom")

	// The unit is gone, so the caller's operation did succeed; the leftover is
	// logged and is no worse than the pre-cascade behaviour.
	if err := f.delete(); err != nil {
		t.Fatalf("expected success after the unit was deleted, got %v", err)
	}
	if len(f.tokens.deletedOUs) != 1 {
		t.Error("a failing cascade step must not abort the remaining ones")
	}
}
