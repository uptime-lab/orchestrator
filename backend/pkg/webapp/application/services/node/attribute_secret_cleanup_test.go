package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// Node attributes are secrets named attr:<nodeID>:<key>, attached to one node
// and to nothing else. Deleting the node used to remove only the attachment,
// leaving the backing secret to count against SECRET_MAX_PER_ACCOUNT forever —
// with no facet that surfaces it as this node's leftover, because the node id
// in its reserved name resolves to nothing.

type fakeAttributeSecretRepo struct {
	// Embedded so this stub tracks the interface without restating it; only
	// DeleteNodeAttributes may be called on this path.
	repository.SecretRepository
	deleted []uuid.UUID
	err     error
}

func (f *fakeAttributeSecretRepo) DeleteNodeAttributes(_ context.Context, _ uuid.UUID, nodeID uuid.UUID) (int64, error) {
	f.deleted = append(f.deleted, nodeID)
	if f.err != nil {
		return 0, f.err
	}
	return 2, nil
}

func newAttributeDeleteService(t *testing.T, secrets *fakeAttributeSecretRepo) (*Service, uuid.UUID, uuid.UUID) {
	t.Helper()
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Hostname: "rpi-abcd0001", Status: node.StatusOnline},
	}}
	recorder, _ := audittest.New()
	svc := &Service{
		nodeRepo: repo,
		audit:    recorder,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if secrets != nil {
		svc.secretRepo = secrets
	}
	return svc, accountID, nodeID
}

func TestDeleteNodeClearsAttributeSecrets(t *testing.T) {
	secrets := &fakeAttributeSecretRepo{}
	svc, accountID, nodeID := newAttributeDeleteService(t, secrets)

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if len(secrets.deleted) != 1 || secrets.deleted[0] != nodeID {
		t.Fatalf("attribute secrets cleared for %v, want exactly [%v]", secrets.deleted, nodeID)
	}
}

// Best-effort for the same reason every sibling cleanup is: the node row is
// already gone, so failing here would leave the caller with a deleted node and
// an error, and no way to retry the part that failed.
func TestDeleteNodeSurvivesAttributeSecretCleanupFailure(t *testing.T) {
	secrets := &fakeAttributeSecretRepo{err: errors.New("secrets table unavailable")}
	svc, accountID, nodeID := newAttributeDeleteService(t, secrets)

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete must not fail because attribute secrets could not be cleared: %v", err)
	}
	if len(secrets.deleted) != 1 {
		t.Fatalf("cleanup was not attempted: %v", secrets.deleted)
	}
}

// A service with nothing wired must still delete — same nil-guard contract as
// every other optional cleanup on this path.
func TestDeleteNodeWithoutSecretRepo(t *testing.T) {
	svc, accountID, nodeID := newAttributeDeleteService(t, nil)

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete with no secret repo wired: %v", err)
	}
}

// --- forgetting the deleted-node record ---------------------------------------

type forgetFakeTombstoneRepo struct {
	repository.NodeTombstoneRepository
	t *node_tombstone.Tombstone
}

func (f *forgetFakeTombstoneRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*node_tombstone.Tombstone, error) {
	return f.t, nil
}

func (f *forgetFakeTombstoneRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }

type fakeForgetAttachmentRepo struct {
	repository.SecretAttachmentRepository
	cleared []uuid.UUID
}

func (f *fakeForgetAttachmentRepo) DeleteByNodeID(_ context.Context, _, nodeID uuid.UUID) error {
	f.cleared = append(f.cleared, nodeID)
	return nil
}

func newForgetService(t *testing.T, live bool) (*Service, *fakeAttributeSecretRepo, *fakeForgetAttachmentRepo, uuid.UUID, uuid.UUID) {
	t.Helper()
	svc, accountID, nodeID := newAttributeDeleteService(t, &fakeAttributeSecretRepo{})
	if !live {
		delete(svc.nodeRepo.(*deleteFakeRepo).nodes, nodeID)
	}
	svc.tombstoneRepo = &forgetFakeTombstoneRepo{t: &node_tombstone.Tombstone{ID: nodeID, AccountID: accountID}}
	attachments := &fakeForgetAttachmentRepo{}
	svc.attachmentRepo = attachments
	return svc, svc.secretRepo.(*fakeAttributeSecretRepo), attachments, accountID, nodeID
}

// Once the record is forgotten the node id can never come back, so whatever
// attribute secrets survived the delete's best-effort cleanup go now, or they
// sit in the Secrets list and the quota with no node to show them under.
func TestForgettingADeletedNodeClearsItsAttributeSecrets(t *testing.T) {
	svc, secrets, attachments, accountID, nodeID := newForgetService(t, false)

	if err := svc.ForgetDeleted(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if len(secrets.deleted) != 1 || secrets.deleted[0] != nodeID {
		t.Errorf("attribute secrets cleared for %v, want exactly [%v]", secrets.deleted, nodeID)
	}
	if len(attachments.cleared) != 1 || attachments.cleared[0] != nodeID {
		t.Errorf("attachments cleared for %v, want exactly [%v]", attachments.cleared, nodeID)
	}
}

// A record can be forgotten after its node was restored: the live node's
// attributes are its own and must survive.
func TestForgettingTheRecordOfARestoredNodeKeepsItsAttributes(t *testing.T) {
	svc, secrets, attachments, accountID, nodeID := newForgetService(t, true)

	if err := svc.ForgetDeleted(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if len(secrets.deleted) != 0 || len(attachments.cleared) != 0 {
		t.Errorf("a live node's secrets were cleared: attributes %v, attachments %v", secrets.deleted, attachments.cleared)
	}
}

// Best-effort like the delete's: the record is already gone, so a failed
// cleanup must not turn the forget into an error with nothing left to retry.
func TestForgetSurvivesAttributeSecretCleanupFailure(t *testing.T) {
	svc, secrets, _, accountID, nodeID := newForgetService(t, false)
	secrets.err = errors.New("secrets table unavailable")

	if err := svc.ForgetDeleted(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Fatalf("forget must not fail because attribute secrets could not be cleared: %v", err)
	}
	if len(secrets.deleted) != 1 {
		t.Fatalf("cleanup was not attempted: %v", secrets.deleted)
	}
}
