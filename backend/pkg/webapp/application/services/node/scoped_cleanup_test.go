package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// Secret attachments and access entries scoped to a node key on node_id alone,
// and node ids are REUSED — a tombstone restore deliberately returns the node
// under its original id. A row that survives the delete is therefore not inert:
// it is delivered again to whatever device answers to that id next.

type fakeAttachmentRepo struct {
	repository.SecretAttachmentRepository
	rows []secret.SecretAttachment
	err  error
}

func (f *fakeAttachmentRepo) ListByNode(_ context.Context, accountID, nodeID uuid.UUID) ([]secret.SecretAttachment, error) {
	out := make([]secret.SecretAttachment, 0, len(f.rows))
	for i := range f.rows {
		if f.rows[i].AccountID == accountID && f.rows[i].NodeID != nil && *f.rows[i].NodeID == nodeID {
			out = append(out, f.rows[i])
		}
	}
	return out, nil
}

func (f *fakeAttachmentRepo) DeleteByNodeID(_ context.Context, accountID, nodeID uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	kept := f.rows[:0]
	for i := range f.rows {
		if f.rows[i].AccountID == accountID && f.rows[i].NodeID != nil && *f.rows[i].NodeID == nodeID {
			continue
		}
		kept = append(kept, f.rows[i])
	}
	f.rows = kept
	return nil
}

type fakeAccessEntryRepo struct {
	repository.AccessEntryRepository
	entries []access_entry.AccessEntry
	err     error
}

func (f *fakeAccessEntryRepo) ListByNode(_ context.Context, accountID, nodeID uuid.UUID) ([]access_entry.AccessEntry, error) {
	out := make([]access_entry.AccessEntry, 0, len(f.entries))
	for i := range f.entries {
		e := f.entries[i]
		if e.AccountID == accountID && e.NodeID != nil && *e.NodeID == nodeID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeAccessEntryRepo) DetachNode(_ context.Context, accountID, nodeID uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	for i := range f.entries {
		e := &f.entries[i]
		if e.AccountID == accountID && e.NodeID != nil && *e.NodeID == nodeID {
			e.NodeID = nil
		}
	}
	return nil
}

// restoreFakeNodeRepo is deleteFakeRepo plus Create, which the restore path
// needs to put the node row back under its original id.
type restoreFakeNodeRepo struct {
	repository.NodeRepository
	nodes map[uuid.UUID]*node.Node
	// serialLookupErr makes ListByExactSerial fail, which is a different answer
	// from "nobody holds it" and must stay tellable apart from one.
	serialLookupErr error
	// plainInserts / lockedInserts count Create and CreateUnderDeviceCap.
	plainInserts, lockedInserts int
	// capCheckErr makes the gated insert's lock or count fail, as the SQL
	// repository answers it while a cap applies.
	capCheckErr error
}

func (f *restoreFakeNodeRepo) GetByID(_ context.Context, _, id uuid.UUID) (*node.Node, error) {
	n, ok := f.nodes[id]
	if !ok {
		return nil, app_errors.ErrNodeNotFound
	}
	return n, nil
}

func (f *restoreFakeNodeRepo) Delete(_ context.Context, _, id uuid.UUID) error {
	delete(f.nodes, id)
	return nil
}

func (f *restoreFakeNodeRepo) Create(_ context.Context, n *node.Node) error {
	f.store(n)
	f.plainInserts++
	return nil
}

// store keeps a copy stamped the way the database stamps an insert, so a
// caller that answers with the value it built instead of the stored row is
// visible.
func (f *restoreFakeNodeRepo) store(n *node.Node) {
	stored := *n
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = time.Now()
		stored.UpdatedAt = stored.CreatedAt
	}
	f.nodes[n.ID] = &stored
}

// CreateUnderDeviceCap is the gated insert a restore of a previously
// admitted node must take, as the SQL repository makes it: refused when the
// admitted rows already fill cap, counted apart from Create so a test can
// tell which one a restore used.
func (f *restoreFakeNodeRepo) CreateUnderDeviceCap(_ context.Context, n *node.Node, limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	cap := limits.InstanceCap
	if cap > 0 && f.capCheckErr != nil {
		return repository.DeviceCapAdmission{}, &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: f.capCheckErr}
	}
	count := f.countAdmitted(uuid.Nil)
	if cap > 0 && count >= int64(cap) {
		return repository.DeviceCapAdmission{Count: count}, &entitlement.DeviceCapError{Cap: cap, Count: count}
	}
	if l := limits.Account; limits.AccountID != uuid.Nil && l.Applies() {
		if own := f.countAdmitted(limits.AccountID); own >= int64(l.Limit) {
			return repository.DeviceCapAdmission{Count: count, AccountCount: own}, &entitlement.AccountDeviceLimitError{
				AccountID: limits.AccountID, Limit: l.Limit, Count: own, Plan: l.Plan, Until: l.Until, Contact: l.Contact}
		}
	}
	f.store(n)
	f.lockedInserts++
	return repository.DeviceCapAdmission{Count: count}, nil
}

func (f *restoreFakeNodeRepo) countAdmitted(accountID uuid.UUID) int64 {
	var count int64
	for _, n := range f.nodes {
		if n.IsAdmitted() && (accountID == uuid.Nil || n.AccountID == accountID) {
			count++
		}
	}
	return count
}

func (f *restoreFakeNodeRepo) CountAdmittedNodesForAccount(_ context.Context, accountID uuid.UUID) (int64, error) {
	return f.countAdmitted(accountID), nil
}

// ListByExactSerial backs the uniqueness guard a restore runs before it rebinds
// a serial: a serial re-adopted by another account in the meantime must keep its
// new owner rather than being shadowed by the restored row.
func (f *restoreFakeNodeRepo) ListByExactSerial(_ context.Context, serial string) ([]*node.Node, error) {
	if f.serialLookupErr != nil {
		return nil, f.serialLookupErr
	}
	var owners []*node.Node
	for _, n := range f.nodes {
		if n.Fingerprint != nil && node.NormalizeSerial(n.Fingerprint.HardwareSerial) == serial {
			owners = append(owners, n)
		}
	}
	return owners, nil
}

func (f *restoreFakeNodeRepo) LowWordHolderInAnotherAccount(_ context.Context, accountID uuid.UUID, serial string) (*repository.SerialHolder, error) {
	if f.serialLookupErr != nil {
		return nil, f.serialLookupErr
	}
	rows := make([]*node.Node, 0, len(f.nodes))
	for _, n := range f.nodes {
		rows = append(rows, n)
	}
	return lowWordHolderAmong(rows, accountID, serial), nil
}

// restoreFakeTombstoneRepo keeps the record a delete writes so the same test can
// restore from it.
type restoreFakeTombstoneRepo struct {
	repository.NodeTombstoneRepository
	created *node_tombstone.Tombstone
}

func (f *restoreFakeTombstoneRepo) Create(_ context.Context, t *node_tombstone.Tombstone) error {
	copied := *t
	f.created = &copied
	return nil
}

func (f *restoreFakeTombstoneRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node_tombstone.Tombstone, error) {
	if f.created == nil {
		return nil, app_errors.ErrNotFound
	}
	return f.created, nil
}

func (f *restoreFakeTombstoneRepo) MarkRestored(_ context.Context, _, _ uuid.UUID) error { return nil }

// newScopedCleanupService returns a delete-capable service with nothing scoped
// wired; each test assigns the repositories it wants exercised.
func newScopedCleanupService(t *testing.T) (*Service, *restoreFakeNodeRepo, uuid.UUID, uuid.UUID) {
	t.Helper()
	accountID, nodeID := uuid.New(), uuid.New()
	nodes := &restoreFakeNodeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Hostname: "rpi-abcd0001", Status: node.StatusOffline, UniqueToken: "the-device-token"},
	}}
	recorder, _ := audittest.New()
	svc := &Service{
		nodeRepo:      nodes,
		tombstoneRepo: &restoreFakeTombstoneRepo{},
		audit:         recorder,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return svc, nodes, accountID, nodeID
}

func TestDeleteDropsNodeScopedSecretAttachmentsAndDetachesAccessEntries(t *testing.T) {
	otherNodeID := uuid.New()
	svc, _, accountID, nodeID := newScopedCleanupService(t)
	secretID, otherSecretID := uuid.New(), uuid.New()
	attachments := &fakeAttachmentRepo{rows: []secret.SecretAttachment{
		{ID: uuid.New(), AccountID: accountID, SecretID: &secretID, NodeID: &nodeID},
		{ID: uuid.New(), AccountID: accountID, SecretID: &otherSecretID, NodeID: &otherNodeID},
	}}
	entries := &fakeAccessEntryRepo{entries: []access_entry.AccessEntry{
		{ID: uuid.New(), AccountID: accountID, Type: access_entry.TypeUser, Username: "ops", NodeID: &nodeID},
		{ID: uuid.New(), AccountID: accountID, Type: access_entry.TypeUser, Username: "fleet", AccountWide: true},
	}}
	svc.attachmentRepo = attachments
	svc.accessEntryRepo = entries

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}

	left, _ := attachments.ListByNode(context.Background(), accountID, nodeID)
	if len(left) != 0 {
		t.Errorf("the deleted node still carries %d secret attachment(s)", len(left))
	}
	if kept, _ := attachments.ListByNode(context.Background(), accountID, otherNodeID); len(kept) != 1 {
		t.Errorf("another node's attachment was removed too: %d left", len(kept))
	}
	stillScoped, _ := entries.ListByNode(context.Background(), accountID, nodeID)
	if len(stillScoped) != 0 {
		t.Errorf("%d access entr(ies) still apply to the deleted node", len(stillScoped))
	}
	// Detached, not destroyed: the entry carries a password and a key set
	// someone authored, so it degrades to unassigned like the OU cascade does.
	if len(entries.entries) != 2 {
		t.Fatalf("access entries were deleted rather than detached: %d left", len(entries.entries))
	}
	if entries.entries[0].TargetScope() != "unassigned" {
		t.Errorf("the node-scoped entry ended up %q, want unassigned", entries.entries[0].TargetScope())
	}
	if entries.entries[1].TargetScope() != "account" {
		t.Errorf("the account-wide entry was re-scoped to %q", entries.entries[1].TargetScope())
	}
}

// A restore reuses the ORIGINAL id, which is what makes the leftovers dangerous
// rather than untidy: without the cleanup the returned node is handed back the
// secrets and shell users the delete was supposed to have taken away.
func TestRestoringADeletedNodeDoesNotResurrectItsSecretsOrShellUsers(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	secretID := uuid.New()
	attachments := &fakeAttachmentRepo{rows: []secret.SecretAttachment{
		{ID: uuid.New(), AccountID: accountID, SecretID: &secretID, NodeID: &nodeID},
	}}
	entries := &fakeAccessEntryRepo{entries: []access_entry.AccessEntry{
		{ID: uuid.New(), AccountID: accountID, Type: access_entry.TypeUser, Username: "ops", NodeID: &nodeID},
	}}
	svc.attachmentRepo = attachments
	svc.accessEntryRepo = entries

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.ID != nodeID {
		t.Fatalf("restore returned id %s, want the original %s", restored.ID, nodeID)
	}
	if _, back := nodes.nodes[nodeID]; !back {
		t.Fatal("the node row was not restored")
	}

	if delivered, _ := attachments.ListByNode(context.Background(), accountID, nodeID); len(delivered) != 0 {
		t.Errorf("the restored node is delivered %d secret attachment(s) from before the delete", len(delivered))
	}
	if users, _ := entries.ListByNode(context.Background(), accountID, nodeID); len(users) != 0 {
		t.Errorf("the restored node is granted %d shell user(s) from before the delete", len(users))
	}
}

// Best-effort for the same reason every sibling cleanup on this path is: the
// node row is already gone, so failing here would leave the caller with a
// deleted node, an error, and no way to retry the part that failed.
func TestDeleteSurvivesScopedCleanupFailures(t *testing.T) {
	svc, _, accountID, nodeID := newScopedCleanupService(t)
	svc.attachmentRepo = &fakeAttachmentRepo{err: errors.New("secret_attachments unavailable")}
	svc.accessEntryRepo = &fakeAccessEntryRepo{err: errors.New("access_entries unavailable")}

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete must not fail because a scoped cleanup failed: %v", err)
	}
}

// A service with neither repo wired must still delete — every other optional
// cleanup here is nil-guarded, and an install that never calls the setter would
// otherwise panic on the most destructive call in the API.
func TestDeleteWithoutTheScopedCleanupReposWired(t *testing.T) {
	svc, _, accountID, nodeID := newScopedCleanupService(t)

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete with no scoped cleanup repos wired: %v", err)
	}
}
