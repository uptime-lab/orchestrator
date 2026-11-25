package access

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
)

// The gap these tests pin down: an access entry is the object that grants a
// shell and sudo on every node in its scope, and every one of these
// operations must leave a record of who performed it.

// --- fakes ----------------------------------------------------------------

type auditEntryRepo struct {
	repository.AccessEntryRepository
	byID map[uuid.UUID]*access_entry.AccessEntry
}

func newAuditEntryRepo() *auditEntryRepo {
	return &auditEntryRepo{byID: map[uuid.UUID]*access_entry.AccessEntry{}}
}

func (r *auditEntryRepo) Create(_ context.Context, e *access_entry.AccessEntry) error {
	copied := *e
	r.byID[e.ID] = &copied
	return nil
}

func (r *auditEntryRepo) Update(_ context.Context, e *access_entry.AccessEntry) error {
	copied := *e
	r.byID[e.ID] = &copied
	return nil
}

func (r *auditEntryRepo) GetByID(_ context.Context, _, id uuid.UUID) (*access_entry.AccessEntry, error) {
	e, ok := r.byID[id]
	if !ok {
		return nil, app_errors.ErrAccessEntryNotFound
	}
	copied := *e
	return &copied, nil
}

func (r *auditEntryRepo) Delete(_ context.Context, _, id uuid.UUID) error {
	delete(r.byID, id)
	return nil
}

func (r *auditEntryRepo) AttachKey(_ context.Context, _, entryID, keyID uuid.UUID) error {
	if e, ok := r.byID[entryID]; ok {
		e.KeyIDs = append(e.KeyIDs, keyID)
	}
	return nil
}

func (r *auditEntryRepo) DetachKey(_ context.Context, _, entryID, keyID uuid.UUID) error {
	e, ok := r.byID[entryID]
	if !ok {
		return nil
	}
	kept := make([]uuid.UUID, 0, len(e.KeyIDs))
	for _, id := range e.KeyIDs {
		if id != keyID {
			kept = append(kept, id)
		}
	}
	e.KeyIDs = kept
	return nil
}

func (r *auditEntryRepo) ListByOU(context.Context, uuid.UUID, uuid.UUID) ([]access_entry.AccessEntry, error) {
	return nil, nil
}

func (r *auditEntryRepo) ListByOUs(context.Context, uuid.UUID, []uuid.UUID) ([]access_entry.AccessEntry, error) {
	return nil, nil
}

func (r *auditEntryRepo) ListByNode(context.Context, uuid.UUID, uuid.UUID) ([]access_entry.AccessEntry, error) {
	return nil, nil
}

type auditKeyRepo struct {
	repository.SSHKeyRepository
	keys map[uuid.UUID]*ssh_key.SSHKey
}

func (r auditKeyRepo) GetByID(_ context.Context, _, id uuid.UUID) (*ssh_key.SSHKey, error) {
	k, ok := r.keys[id]
	if !ok {
		return nil, app_errors.ErrSSHKeyNotFound
	}
	return k, nil
}

func (r auditKeyRepo) GetByIDs(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]ssh_key.SSHKey, error) {
	out := make([]ssh_key.SSHKey, 0, len(ids))
	for _, id := range ids {
		if k, ok := r.keys[id]; ok {
			out = append(out, *k)
		}
	}
	return out, nil
}

type auditOURepo struct {
	repository.OrganizationalUnitRepository
	ous map[uuid.UUID]*organizational_unit.OrganizationalUnit
}

func (r auditOURepo) GetByID(_ context.Context, _, id uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	ou, ok := r.ous[id]
	if !ok {
		return nil, app_errors.ErrOrganizationalUnitNotFound
	}
	return ou, nil
}

func (r auditOURepo) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return nil, nil
}

type auditNodeRepo struct {
	repository.NodeRepository
	nodes map[uuid.UUID]*node.Node
}

func (r auditNodeRepo) GetByID(_ context.Context, _, id uuid.UUID) (*node.Node, error) {
	n, ok := r.nodes[id]
	if !ok {
		return nil, app_errors.ErrNodeNotFound
	}
	return n, nil
}

// --- harness --------------------------------------------------------------

type auditHarness struct {
	svc       *Service
	sink      *audittest.Sink
	entries   *auditEntryRepo
	accountID uuid.UUID
	actorID   uuid.UUID
	ouID      uuid.UUID
	keyID     uuid.UUID
	nodeID    uuid.UUID
}

func newAuditHarness(t *testing.T) *auditHarness {
	t.Helper()
	accountID, ouID, keyID, nodeID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	recorder, sink := audittest.New()
	entries := newAuditEntryRepo()
	svc := NewService(
		entries,
		auditKeyRepo{keys: map[uuid.UUID]*ssh_key.SSHKey{
			keyID: {ID: keyID, AccountID: accountID, KeyType: ssh_key.KeyTypeSSH, Alias: "laptop"},
		}},
		auditOURepo{ous: map[uuid.UUID]*organizational_unit.OrganizationalUnit{
			ouID: {ID: ouID, AccountID: accountID, Name: "rack-1"},
		}},
		auditNodeRepo{nodes: map[uuid.UUID]*node.Node{
			nodeID: {ID: nodeID, AccountID: accountID},
		}},
		recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	return &auditHarness{
		svc: svc, sink: sink, entries: entries,
		accountID: accountID, actorID: uuid.New(),
		ouID: ouID, keyID: keyID, nodeID: nodeID,
	}
}

func (h *auditHarness) createUser(t *testing.T) *access_entry.AccessEntry {
	t.Helper()
	e, err := h.svc.CreateUserEntry(context.Background(), CreateUserEntryRequest{
		AccountID:   h.accountID,
		ActorUserID: h.actorID,
		OUIDs:       []uuid.UUID{h.ouID},
		Username:    "deploy",
		Sudo:        true,
		KeyIDs:      []uuid.UUID{h.keyID},
	})
	if err != nil {
		t.Fatalf("create user entry: %v", err)
	}
	return e
}

// requireEvent asserts exactly one event with the action exists and returns it.
func (h *auditHarness) requireEvent(t *testing.T, action string) audit_event.AuditEvent {
	t.Helper()
	event, ok := h.sink.Find(action)
	if !ok {
		t.Fatalf("no %q audit event; recorded %v", action, h.sink.Actions())
	}
	if event.ActorKind != audit_event.ActorUser {
		t.Errorf("%s recorded as %q, want a user actor", action, event.ActorKind)
	}
	if event.ActorUserID == nil || *event.ActorUserID != h.actorID {
		t.Errorf("%s actor = %v, want %v", action, event.ActorUserID, h.actorID)
	}
	if event.AccountID != h.accountID {
		t.Errorf("%s account = %v, want %v", action, event.AccountID, h.accountID)
	}
	return event
}

// --- tests ----------------------------------------------------------------

func TestCreateUserEntryIsAudited(t *testing.T) {
	h := newAuditHarness(t)
	e := h.createUser(t)

	event := h.requireEvent(t, audit_event.ActionAccessEntryCreateUser)
	if event.TargetType != audit_event.TargetAccessEntry || event.TargetID != e.ID.String() {
		t.Errorf("target = %s/%s, want access_entry/%s", event.TargetType, event.TargetID, e.ID)
	}
	// The three facts that decide what the entry grants.
	for _, want := range []string{"user deploy", "sudo", "keys=1", h.ouID.String()} {
		if !strings.Contains(event.Detail, want) {
			t.Errorf("detail %q is missing %q", event.Detail, want)
		}
	}
}

func TestCreateCAEntryIsAudited(t *testing.T) {
	h := newAuditHarness(t)
	caKeyID := uuid.New()
	h.svc.sshKeyRepo = auditKeyRepo{keys: map[uuid.UUID]*ssh_key.SSHKey{
		caKeyID: {ID: caKeyID, AccountID: h.accountID, KeyType: ssh_key.KeyTypeCA, Alias: "corp-ca"},
	}}

	e, err := h.svc.CreateCAEntry(context.Background(), CreateCAEntryRequest{
		AccountID:   h.accountID,
		ActorUserID: h.actorID,
		OUIDs:       []uuid.UUID{h.ouID},
		CAKeyID:     caKeyID,
	})
	if err != nil {
		t.Fatalf("create ca entry: %v", err)
	}

	event := h.requireEvent(t, audit_event.ActionAccessEntryCreateCA)
	if event.TargetID != e.ID.String() {
		t.Errorf("target id = %s, want %s", event.TargetID, e.ID)
	}
	if !strings.Contains(event.Detail, caKeyID.String()) {
		t.Errorf("detail %q does not name the trusted CA key", event.Detail)
	}
}

// Granting sudo is the privilege escalation an audit trail exists to catch, and
// the detail must say WHICH field moved: the stored entry afterwards cannot
// distinguish "sudo was granted" from "sudo was re-sent unchanged".
func TestUpdateEntryRecordsWhichFieldChanged(t *testing.T) {
	h := newAuditHarness(t)
	e := h.createUser(t)

	sudo := false
	if _, err := h.svc.UpdateEntry(context.Background(), UpdateEntryRequest{
		AccountID:   h.accountID,
		ActorUserID: h.actorID,
		EntryID:     e.ID,
		Sudo:        &sudo,
	}); err != nil {
		t.Fatalf("update entry: %v", err)
	}

	event := h.requireEvent(t, audit_event.ActionAccessEntryUpdate)
	if !strings.Contains(event.Detail, "sudo=false") {
		t.Errorf("detail %q does not record the sudo change", event.Detail)
	}
}

// A delete must be recorded from the PRE-delete row: afterwards the id resolves
// to nothing, and "which OS user stopped existing on the fleet" is the question.
func TestDeleteEntryNamesTheUserItRemoved(t *testing.T) {
	h := newAuditHarness(t)
	e := h.createUser(t)

	if err := h.svc.DeleteEntry(context.Background(), h.accountID, h.actorID, e.ID); err != nil {
		t.Fatalf("delete entry: %v", err)
	}

	event := h.requireEvent(t, audit_event.ActionAccessEntryDelete)
	if !strings.Contains(event.Detail, "user deploy") {
		t.Errorf("detail %q does not name the deleted OS user", event.Detail)
	}
}

// The key id belongs in the detail: an entry with several keys makes a bare
// "a key was attached" unactionable.
func TestKeyAttachAndDetachNameTheKey(t *testing.T) {
	h := newAuditHarness(t)
	e := h.createUser(t)
	ctx := context.Background()

	if err := h.svc.AttachKey(ctx, h.accountID, h.actorID, e.ID, h.keyID); err != nil {
		t.Fatalf("attach key: %v", err)
	}
	if err := h.svc.DetachKey(ctx, h.accountID, h.actorID, e.ID, h.keyID); err != nil {
		t.Fatalf("detach key: %v", err)
	}

	for _, action := range []string{
		audit_event.ActionAccessEntryKeyAttach,
		audit_event.ActionAccessEntryKeyDetach,
	} {
		event := h.requireEvent(t, action)
		if !strings.Contains(event.Detail, h.keyID.String()) {
			t.Errorf("%s detail %q does not name the key", action, event.Detail)
		}
	}
}

// A manual re-apply stores no new intent but changes what the DEVICE believes,
// so "nobody edited anything and the node's logins changed" needs an author.
func TestReapplyNodeIsAudited(t *testing.T) {
	h := newAuditHarness(t)
	h.svc.SetApplier(&fakeApplier{})

	if _, err := h.svc.ReapplyNode(context.Background(), h.accountID, h.actorID, h.nodeID); err != nil {
		t.Fatalf("reapply: %v", err)
	}

	event := h.requireEvent(t, audit_event.ActionAccessReapplyNode)
	if event.TargetType != audit_event.TargetNode || event.TargetID != h.nodeID.String() {
		t.Errorf("target = %s/%s, want node/%s", event.TargetType, event.TargetID, h.nodeID)
	}
}

// An entry's password is encrypted at rest under SECRETS_ENCRYPTION_KEY. A copy
// in audit_events would be a plaintext leak that no rotation and no rewrap list
// would ever find, so the detail records only that a password EXISTS.
func TestAuditDetailNeverCarriesThePassword(t *testing.T) {
	h := newAuditHarness(t)
	const password = "correct-horse-battery-staple"
	pw := password

	e, err := h.svc.CreateUserEntry(context.Background(), CreateUserEntryRequest{
		AccountID:   h.accountID,
		ActorUserID: h.actorID,
		OUIDs:       []uuid.UUID{h.ouID},
		Username:    "deploy",
		Password:    &pw,
		KeyIDs:      []uuid.UUID{h.keyID},
	})
	if err != nil {
		t.Fatalf("create user entry: %v", err)
	}
	newPassword := "hunter2-rotated"
	if _, err := h.svc.UpdateEntry(context.Background(), UpdateEntryRequest{
		AccountID:   h.accountID,
		ActorUserID: h.actorID,
		EntryID:     e.ID,
		Password:    &newPassword,
	}); err != nil {
		t.Fatalf("update entry: %v", err)
	}

	for _, event := range h.sink.Events() {
		for _, leaked := range []string{password, newPassword} {
			if strings.Contains(event.Detail, leaked) {
				t.Fatalf("%s leaked a password into audit detail: %q", event.Action, event.Detail)
			}
		}
		if !strings.Contains(event.Detail, "password") {
			t.Errorf("%s detail %q does not record that a password is set", event.Action, event.Detail)
		}
	}
}

// A recorder is required, not optional: a service that silently discards audit
// events is exactly that gap, and it must fail at composition.
func TestNewServiceRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewService(newAuditEntryRepo(), auditKeyRepo{}, auditOURepo{}, auditNodeRepo{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Guard against the actor being dropped somewhere in the chain: an event with no
// account is invisible to every read, and one with no user reads as the platform.
func TestNoAccessEventIsRecordedAsTheSystem(t *testing.T) {
	h := newAuditHarness(t)
	e := h.createUser(t)
	ctx := context.Background()
	_ = h.svc.AttachKey(ctx, h.accountID, h.actorID, e.ID, h.keyID)
	_ = h.svc.DeleteEntry(ctx, h.accountID, h.actorID, e.ID)

	events := h.sink.Events()
	if len(events) == 0 {
		t.Fatal("nothing was recorded")
	}
	for _, event := range events {
		if event.ActorKind != audit_event.ActorUser {
			t.Errorf("%s recorded as %q", event.Action, event.ActorKind)
		}
	}
}

var _ = audit.SystemActor // keep the import meaningful if the tests above change
