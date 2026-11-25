package node

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// operatorUpdateRepo records WHICH write path was taken. The embedded nil
// interface panics on anything else.
type operatorUpdateRepo struct {
	repository.NodeRepository
	stored *node.Node

	fullRowSaves int
	scopedSaves  int
	saved        *node.Node
}

func (r *operatorUpdateRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	copied := *r.stored
	return &copied, nil
}

func (r *operatorUpdateRepo) Update(_ context.Context, n *node.Node) error {
	r.fullRowSaves++
	r.saved = n
	return nil
}

func (r *operatorUpdateRepo) UpdateOperatorEditable(_ context.Context, n *node.Node) error {
	r.scopedSaves++
	r.saved = n
	return nil
}

// An operator editing Notes must not write the whole row back. The row is
// written continuously by the gRPC layer, so a full-row save from a read taken
// moments earlier reverts whatever arrived in between — including the
// `restarting` pin, which exists to survive a reboot and whose loss makes
// handleDisconnection mark the node offline instead.
//
// The assertion is on the PATH rather than on the resulting columns, because
// which columns travel is the repository's decision and this is the caller's
// half of the contract.
func TestOperatorUpdateDoesNotSaveTheWholeRow(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &operatorUpdateRepo{stored: &node.Node{
		ID: nodeID, AccountID: accountID, Name: "before", Notes: "",
		Status: node.StatusRestarting,
	}}
	svc := guardService(repo) // no websocket client wired: notifyNodeUpdated is a no-op

	notes := "rack 3, shelf 2"
	updated, err := svc.Update(context.Background(), node_update.NodeUpdateCommand{
		ID:          nodeID,
		Notes:       &notes,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if repo.fullRowSaves != 0 {
		t.Errorf("full-row saves = %d, want 0: an operator edit must not carry machine-owned columns", repo.fullRowSaves)
	}
	if repo.scopedSaves != 1 {
		t.Errorf("scoped saves = %d, want 1", repo.scopedSaves)
	}
	if updated.Notes != notes {
		t.Errorf("notes = %q, want %q", updated.Notes, notes)
	}
}
