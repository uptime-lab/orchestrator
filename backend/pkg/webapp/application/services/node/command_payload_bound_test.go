package node

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// payloadBoundRepo returns one approved, online node for GetByID and panics for
// anything else, proving the guard rejects before it reaches the command service.
type payloadBoundRepo struct {
	repository.NodeRepository
	n *node.Node
}

func (r *payloadBoundRepo) GetByID(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*node.Node, error) {
	return r.n, nil
}

type payloadBoundComm struct {
	service.NodeCommunicationService
}

func (payloadBoundComm) IsNodeConnected(string) bool { return false }

type payloadBoundAuth struct{ accountID uuid.UUID }

func (a payloadBoundAuth) GetAccountID() uuid.UUID { return a.accountID }

// An oversized bulk field must be refused BEFORE the command is registered or
// dispatched — an >8 MiB frame drops the node's stream while the receive loop
// lives, so the node reads ONLINE forever and every later command silently
// fails. patch_file is queueable, so this reaches the guard with no
// live connection and with nodeCommandService nil: a nil-pointer panic would
// prove the guard ran too late.
func TestSendCommandRejectsOversizedBulkFieldBeforeDispatch(t *testing.T) {
	accountID := uuid.New()
	n := &node.Node{
		ID:             uuid.New(),
		AccountID:      accountID,
		Status:         node.StatusOnline,
		ApprovalStatus: node.ApprovalStatusApproved,
		AgentVersion:   "0.5.0",
	}
	s := &Service{
		nodeRepo:             &payloadBoundRepo{n: n},
		communicationService: payloadBoundComm{},
		logger:               slog.Default(),
		// nodeCommandService deliberately nil: registration must never be reached.
	}

	oversized := strings.Repeat("x", node_file.MaxContentBytes+1)
	_, _, err := s.sendCommandToNode(context.Background(), n.ID, node_command.SlugPatchFile,
		map[string]interface{}{"content": oversized, "path": "/boot/firmware/config.txt"},
		payloadBoundAuth{accountID: accountID}, nil, nil, "")

	var tooLarge *CommandPayloadTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected CommandPayloadTooLargeError, got %v", err)
	}
	if tooLarge.Field != "content" {
		t.Errorf("field = %q, want content", tooLarge.Field)
	}
	if tooLarge.Bytes <= tooLarge.Limit {
		t.Errorf("reported bytes %d should exceed limit %d", tooLarge.Bytes, tooLarge.Limit)
	}
	// That the error came back at all — rather than a nil-pointer panic from the
	// deliberately-nil nodeCommandService — is the proof the guard fires BEFORE
	// registration, which is the whole point of the fix.
}
