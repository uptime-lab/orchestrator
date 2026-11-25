package sql

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	nodesvc "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The WebUSB re-flash reads the node (still awaiting its first connect) and
// then re-marks it flashing. The device's first connect can land between the
// two. The re-mark must then write nothing: an unconditional write moved the
// connected, admitted device to flashing, out of the device cap's count while
// its stream stayed up. It answers the typed 409 instead.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test -p 1 ./pkg/webapp/infrastructure/database/sql/
func TestWebUSBReflashCannotUnadmitADeviceThatConnectedFirst(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	account := uuid.New()
	p := seedNode(t, repo, account, func(n *node.Node) {
		n.Status = node.StatusReadyForAdoption
		n.AwaitingFirstConnect = true
		n.Fingerprint = &node.HardwareFingerprint{HardwareSerial: "deadbeef"}
	})
	svc := capStatusService(t, repo, 5)

	// The handler's read: the placeholder is awaiting its first connect.
	read, err := repo.GetByID(ctx, account, p.ID)
	if err != nil || !read.AwaitingFirstConnect {
		t.Fatalf("fixture: %+v, %v", read, err)
	}
	// The device connects in between.
	if _, err := svc.Connect(ctx, node_connect.NodeConnectCommand{NodeID: p.ID, AccountID: account, UniqueToken: p.UniqueToken,
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "10000000deadbeef", MacAddress: "02:00:00:00:0f:0f"}}); err != nil {
		t.Fatalf("the device's connect: %v", err)
	}
	before, _ := repo.CountAdmittedNodes(ctx)

	err = svc.MarkWebUSBReflashing(ctx, p.ID, account)
	var connected *nodesvc.WebUSBDeviceConnectedError
	if !errors.As(err, &connected) {
		t.Errorf("re-marking a device that connected first: err = %v, want WebUSBDeviceConnectedError", err)
	}
	after, _ := repo.CountAdmittedNodes(ctx)
	if after != before {
		t.Errorf("the re-mark moved the admitted count from %d to %d", before, after)
	}
	row, _ := repo.GetByID(ctx, account, p.ID)
	if row == nil || row.Status != node.StatusOnline {
		t.Errorf("the connected device reads %+v after the re-mark, want online", row)
	}
}

// The re-mark still does its job on a placeholder no device has connected
// to: it goes back to flashing until the new flash commits.
func TestWebUSBReflashMarksAnUnconnectedPlaceholder(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	account := uuid.New()
	p := seedNode(t, repo, account, func(n *node.Node) {
		n.Status = node.StatusReadyForAdoption
		n.AwaitingFirstConnect = true
	})
	svc := capStatusService(t, repo, 5)
	if err := svc.MarkWebUSBReflashing(ctx, p.ID, account); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	row, _ := repo.GetByID(ctx, account, p.ID)
	if row == nil || row.Status != node.StatusFlashing {
		t.Errorf("after the re-mark the placeholder reads %+v, want flashing", row)
	}
}
