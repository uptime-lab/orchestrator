package node

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// An empty token is never a credential. A stored token that no configured key
// can decrypt reads back as "" with UniqueTokenUnreadable set, and a
// constant-time compare of two empty strings is equal — so the connect path
// must refuse before comparing, or a lost key authenticates anyone who sends "".

type tokenConnectRepo struct {
	repository.NodeRepository
	row     *node.Node
	updates int
}

func (r *tokenConnectRepo) GetByIDAndAccount(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	cp := *r.row
	return &cp, nil
}

func (r *tokenConnectRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	cp := *r.row
	return &cp, nil
}

// ListByExactSerial backs the first-bind serial guard: no other owner.
func (r *tokenConnectRepo) ListByExactSerial(context.Context, string) ([]*node.Node, error) {
	return nil, nil
}

func (r *tokenConnectRepo) LowWordHolderInAnotherAccount(context.Context, uuid.UUID, string) (*repository.SerialHolder, error) {
	return nil, nil
}

func (r *tokenConnectRepo) Update(context.Context, *node.Node) error {
	r.updates++
	return nil
}

func (r *tokenConnectRepo) UpdateUnderDeviceCap(context.Context, *node.Node, repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
	r.updates++
	return repository.DeviceCapAdmission{}, nil
}

func tokenConnectService(row *node.Node) (*Service, *tokenConnectRepo) {
	repo := &tokenConnectRepo{row: row}
	return &Service{nodeRepo: repo, logger: slog.New(slog.DiscardHandler)}, repo
}

func TestConnectRefusesEmptyTokenAgainstUnreadableStoredToken(t *testing.T) {
	// Bound and unbound rows both: an unbound row would take the caller's
	// fingerprint, a bound one hands over its stream.
	for _, bound := range []bool{false, true} {
		row := &node.Node{
			ID:                    uuid.New(),
			AccountID:             uuid.New(),
			UniqueToken:           "",
			UniqueTokenUnreadable: true,
			ApprovalStatus:        node.ApprovalStatusApproved,
			Status:                node.StatusOffline,
		}
		fp := node.HardwareFingerprint{HardwareSerial: "10000000abcd0001", MacAddress: "2c:cf:67:12:34:56"}
		if bound {
			stored := fp
			row.Fingerprint = &stored
		}
		svc, repo := tokenConnectService(row)

		got, err := svc.Connect(context.Background(), node_connect.NodeConnectCommand{
			NodeID: row.ID, AccountID: row.AccountID, UniqueToken: "", Fingerprint: fp,
		})
		if !errors.Is(err, app_errors.ErrNodeNotFound) || got != nil {
			t.Fatalf("bound=%v: got node=%v err=%v, want the uniform ErrNodeNotFound refusal", bound, got, err)
		}
		if repo.updates != 0 {
			t.Errorf("bound=%v: the refused connect wrote the row %d time(s)", bound, repo.updates)
		}
	}
}

func TestConnectRefusesEmptyPresentedToken(t *testing.T) {
	row := &node.Node{ID: uuid.New(), AccountID: uuid.New(), UniqueToken: "", ApprovalStatus: node.ApprovalStatusApproved}
	svc, _ := tokenConnectService(row)

	_, err := svc.Connect(context.Background(), node_connect.NodeConnectCommand{
		NodeID: row.ID, AccountID: row.AccountID, UniqueToken: "",
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "10000000abcd0001", MacAddress: "2c:cf:67:12:34:56"},
	})
	if !errors.Is(err, app_errors.ErrNodeNotFound) {
		t.Fatalf("err = %v, want ErrNodeNotFound: an empty stored token matched an empty presented one", err)
	}
}

func TestGetAdoptionInfoRefusesUnreadableToken(t *testing.T) {
	row := &node.Node{ID: uuid.New(), AccountID: uuid.New(), UniqueToken: "", UniqueTokenUnreadable: true}
	svc, _ := tokenConnectService(row)

	blob, err := svc.GetAdoptionInfo(context.Background(), row.AccountID, row.ID, "https://platform.example", "grpc.example:50051")
	if err == nil || blob != "" {
		t.Fatalf("got blob=%q err=%v, want a refusal: a blob with an empty token is a credential that never authenticates", blob, err)
	}
}
