package access

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

type danglingNodeRepo struct {
	repository.NodeRepository
	n *node.Node
}

func (r *danglingNodeRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	return r.n, nil
}

// danglingOURepo always answers "not found", standing in for a node whose OU was
// deleted while the node still points at it.
type danglingOURepo struct {
	repository.OrganizationalUnitRepository
}

func (r danglingOURepo) GetByID(_ context.Context, _, _ uuid.UUID) (*organizational_unit.OrganizationalUnit, error) {
	return nil, app_errors.ErrOrganizationalUnitNotFound
}

type danglingEntryRepo struct {
	repository.AccessEntryRepository
	node    []access_entry.AccessEntry
	account []access_entry.AccessEntry
}

func (r *danglingEntryRepo) ListByNode(_ context.Context, _, _ uuid.UUID) ([]access_entry.AccessEntry, error) {
	return r.node, nil
}

func (r *danglingEntryRepo) ListByOU(_ context.Context, _, _ uuid.UUID) ([]access_entry.AccessEntry, error) {
	return nil, nil
}

func (r *danglingEntryRepo) ListAccountLevel(_ context.Context, _ uuid.UUID) ([]access_entry.AccessEntry, error) {
	return r.account, nil
}

// A node whose ou_id points at a deleted OU must resolve as "unassigned" — its
// node-level and account-level entries — rather than hard-failing. Failing here
// disabled all access management for the node and let an SSH-key revocation
// report success while never landing.
func TestResolveEffectiveAccessDegradesADanglingOU(t *testing.T) {
	accountID := uuid.New()
	ouID := uuid.New()
	nodeID := uuid.New()

	s := &Service{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		nodeRepo:  &danglingNodeRepo{n: &node.Node{ID: nodeID, AccountID: accountID, OUID: &ouID}},
		ouRepo:    danglingOURepo{},
		entryRepo: &danglingEntryRepo{},
	}

	if _, err := s.ResolveEffectiveAccess(context.Background(), accountID, nodeID); err != nil {
		t.Fatalf("a dangling ou_id must degrade to unassigned, got error: %v", err)
	}
}
