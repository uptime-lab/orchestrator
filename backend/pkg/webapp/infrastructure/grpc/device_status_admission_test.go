package grpc

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// statusFakeNodeService records the status writes a StatusUpdate frame
// causes; the embedded nil interface panics on anything not listed.
type statusFakeNodeService struct {
	services.NodeService
	stored node.NodeStatus
	writes []string
}

func (f *statusFakeNodeService) GetByIDAndAccount(_ context.Context, id, account uuid.UUID) (*node.Node, error) {
	return &node.Node{ID: id, AccountID: account, Status: f.stored, ApprovalStatus: node.ApprovalStatusApproved}, nil
}

func (f *statusFakeNodeService) UpdateStatus(_ context.Context, _ uuid.UUID, status string) error {
	f.writes = append(f.writes, status)
	return nil
}

func (f *statusFakeNodeService) UpdateAgentInfo(context.Context, uuid.UUID, string, string, []string, time.Time) error {
	return nil
}

// A device may report only statuses inside the admitted set. The pre-connect
// statuses are the platform's: a device that could write ready_for_adoption
// on its own row would step out of the device cap's count while its stream
// stays up, free a slot for another device, and step back in with a plain
// status write, bypassing the cap.
func TestDeviceCannotReportAPreConnectStatus(t *testing.T) {
	for _, status := range node.NotAdmittedStatuses {
		t.Run(string(status), func(t *testing.T) {
			svc := &statusFakeNodeService{stored: node.StatusOnline}
			srv := &Server{nodeService: svc, logger: slog.Default()}
			srv.handleAgentStatusUpdate(context.Background(), uuid.NewString(), uuid.NewString(), string(status), "", time.Now())
			if len(svc.writes) != 0 {
				t.Fatalf("a device-sent %q was written (%v); a pre-connect status is platform-set", status, svc.writes)
			}
		})
	}
}

// The statuses a device legitimately reports still land.
func TestDeviceReportsTheAdmittedStatuses(t *testing.T) {
	for _, status := range []node.NodeStatus{node.StatusOnline, node.StatusOffline} {
		svc := &statusFakeNodeService{stored: node.StatusOnline}
		srv := &Server{nodeService: svc, logger: slog.Default()}
		srv.handleAgentStatusUpdate(context.Background(), uuid.NewString(), uuid.NewString(), string(status), "", time.Now())
		if len(svc.writes) != 1 || svc.writes[0] != string(status) {
			t.Errorf("a device-sent %q was not written: %v", status, svc.writes)
		}
	}
}
