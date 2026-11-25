package sql

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	nodesvc "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The device cap bounds the TRANSITION into the admitted set, so every write
// that can carry a row from a pre-connect status into an admitted one must
// take the locked admission — not only the first connect. Without that, the
// status-only writes let a row step out
// of the count (a device-sent ready_for_adoption, or the WebUSB commit
// racing a connect), another device took the freed slot, and a plain status
// write put the first row back, leaving the install above its cap with
// nothing refused.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

func capStatusService(t *testing.T, repo *NodeRepository, cap int) *nodesvc.Service {
	t.Helper()
	recorder, _ := audittest.New()
	svc := nodesvc.NewService(repo, nil, nil, nil, nil, nil, nil, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetDeviceCapResolver(fixedDeviceCap(cap))
	return svc
}

// stepOutAndFillTheSlot builds the bypass's middle state on an install at
// cap 2: node a was admitted and has stepped out to ready_for_adoption, and
// node c was then admitted into the slot a freed. Returns a.
func stepOutAndFillTheSlot(t *testing.T, repo *NodeRepository, svc *nodesvc.Service, account uuid.UUID) *node.Node {
	t.Helper()
	ctx := context.Background()
	a := seedNode(t, repo, account, nil)
	seedNode(t, repo, account, nil)
	if err := svc.UpdateStatus(ctx, a.ID, string(node.StatusReadyForAdoption)); err != nil {
		t.Fatalf("stepping a out: %v", err)
	}
	c := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusReadyForAdoption })
	if _, err := svc.Connect(ctx, node_connect.NodeConnectCommand{NodeID: c.ID, AccountID: account, UniqueToken: c.UniqueToken,
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "1000000000000c0c", MacAddress: "02:00:00:00:0c:0c"}}); err != nil {
		t.Fatalf("admitting c into the freed slot: %v", err)
	}
	if count, _ := repo.CountAdmittedNodes(ctx); count != 2 {
		t.Fatalf("fixture: %d admitted, want the cap 2", count)
	}
	return a
}

func assertStillAtCap(t *testing.T, repo *NodeRepository, account uuid.UUID, a *node.Node) {
	t.Helper()
	ctx := context.Background()
	if count, _ := repo.CountAdmittedNodes(ctx); count != 2 {
		t.Errorf("the install holds %d admitted devices, want the cap 2: a status write re-admitted a row without the lock", count)
	}
	row, err := repo.GetByID(ctx, account, a.ID)
	if err != nil {
		t.Fatalf("reading a: %v", err)
	}
	if row.IsAdmitted() {
		t.Errorf("the refused row was admitted anyway (status %q)", row.Status)
	}
}

// The device's own "online" and the monitor's "offline" both go through
// Service.UpdateStatus; at the cap either must be refused, and the row left
// out of the count.
func TestStatusWriteCannotReadmitPastTheCap(t *testing.T) {
	for _, status := range []node.NodeStatus{node.StatusOnline, node.StatusOffline} {
		t.Run(string(status), func(t *testing.T) {
			repo := deviceCapRepo(t)
			svc := capStatusService(t, repo, 2)
			account := uuid.New()
			a := stepOutAndFillTheSlot(t, repo, svc, account)

			err := svc.UpdateStatus(context.Background(), a.ID, string(status))
			var capErr *entitlement.DeviceCapError
			if !errors.As(err, &capErr) {
				t.Errorf("re-entering the admitted set at the cap: err = %v, want the cap refusal", err)
			}
			assertStillAtCap(t, repo, account, a)
		})
	}
}

// The pinned status writes (the command service's maintenance/restarting)
// are status writes too.
func TestPinnedStatusWriteCannotReadmitPastTheCap(t *testing.T) {
	repo := deviceCapRepo(t)
	svc := capStatusService(t, repo, 2)
	account := uuid.New()
	a := stepOutAndFillTheSlot(t, repo, svc, account)

	err := svc.UpdateStatusWithAccountPinnedBy(context.Background(), a.ID, account, string(node.StatusMaintenance), "reboot")
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Errorf("a pinned write re-entering the admitted set at the cap: err = %v, want the cap refusal", err)
	}
	assertStillAtCap(t, repo, account, a)
}

// A full-row save from a copy read while the row was admitted must not put
// it back either: that is the read-modify-write shape every agent report
// (UpdateSystemInfo, the boot order) takes.
func TestFullRowSaveCannotReadmit(t *testing.T) {
	repo := deviceCapRepo(t)
	svc := capStatusService(t, repo, 2)
	account := uuid.New()
	stale := seedNode(t, repo, account, nil) // the copy a report read: online
	seedNode(t, repo, account, nil)
	if err := svc.UpdateStatus(context.Background(), stale.ID, string(node.StatusReadyForAdoption)); err != nil {
		t.Fatalf("stepping out: %v", err)
	}
	c := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusReadyForAdoption })
	if _, err := svc.Connect(context.Background(), node_connect.NodeConnectCommand{NodeID: c.ID, AccountID: account, UniqueToken: c.UniqueToken,
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "1000000000000d0d", MacAddress: "02:00:00:00:0d:0d"}}); err != nil {
		t.Fatalf("admitting c: %v", err)
	}

	err := repo.Update(context.Background(), stale)
	var required *node.AdmissionRequiredError
	if !errors.As(err, &required) {
		t.Errorf("a stale full-row save of an admitted state over a pre-connect row: err = %v, want AdmissionRequiredError", err)
	}
	assertStillAtCap(t, repo, account, stale)
}

// Moves inside the admitted set are not admissions: at the cap a device
// going offline and coming back online must not be refused.
func TestAdmittedStatusMovesPassAtTheCap(t *testing.T) {
	repo := deviceCapRepo(t)
	svc := capStatusService(t, repo, 2)
	account := uuid.New()
	a := seedNode(t, repo, account, nil)
	seedNode(t, repo, account, nil)
	for _, status := range []node.NodeStatus{node.StatusOffline, node.StatusOnline, node.StatusMaintenance, node.StatusRestarting, node.StatusOnline} {
		if err := svc.UpdateStatus(context.Background(), a.ID, string(status)); err != nil {
			t.Fatalf("%s at the cap: %v", status, err)
		}
	}
	if count, _ := repo.CountAdmittedNodes(context.Background()); count != 2 {
		t.Errorf("count %d, want 2", count)
	}
}

// Below the cap the same status write is an admission that passes — through
// the lock, not around it.
func TestStatusWriteAdmitsBelowTheCap(t *testing.T) {
	repo := deviceCapRepo(t)
	svc := capStatusService(t, repo, 5)
	account := uuid.New()
	a := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusReadyForAdoption })
	if err := svc.UpdateStatus(context.Background(), a.ID, string(node.StatusOnline)); err != nil {
		t.Fatalf("admission below the cap: %v", err)
	}
	row, _ := repo.GetByID(context.Background(), account, a.ID)
	if row == nil || row.Status != node.StatusOnline {
		t.Errorf("the admitted row reads %+v, want online", row)
	}
}

// staleFlashRepo hands CommitWebUSBFlash the copy it read before the device
// connected: still flashing, while the stored row has been admitted.
type staleFlashRepo struct {
	*NodeRepository
	stale *node.Node
}

func (r staleFlashRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	cp := *r.stale
	return &cp, nil
}

// The browser's flash commit reads "flashing" and then writes
// ready_for_adoption. A device that connected (and was admitted) in between
// must not be un-admitted by that write: its next status write would then
// re-admit it, and the slot it freed meanwhile would have gone to another
// device.
func TestWebUSBCommitCannotUnadmitAConnectedDevice(t *testing.T) {
	repo := deviceCapRepo(t)
	account := uuid.New()
	w := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusFlashing })
	staleCopy := *w

	svc := capStatusService(t, repo, 5)
	if _, err := svc.Connect(context.Background(), node_connect.NodeConnectCommand{NodeID: w.ID, AccountID: account, UniqueToken: w.UniqueToken,
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "1000000000000e0e", MacAddress: "02:00:00:00:0e:0e"}}); err != nil {
		t.Fatalf("the device's connect: %v", err)
	}
	before, _ := repo.CountAdmittedNodes(context.Background())

	recorder, _ := audittest.New()
	racing := nodesvc.NewService(staleFlashRepo{NodeRepository: repo, stale: &staleCopy}, nil, nil, nil, nil, nil, nil, recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := racing.CommitWebUSBFlash(context.Background(), w.ID, account); err != nil {
		t.Fatalf("a late commit must stay idempotent for the browser: %v", err)
	}

	after, _ := repo.CountAdmittedNodes(context.Background())
	if after != before {
		t.Errorf("the late flash commit moved the admitted count from %d to %d", before, after)
	}
	row, _ := repo.GetByID(context.Background(), account, w.ID)
	if row == nil || row.Status != node.StatusOnline {
		t.Errorf("the connected device reads %+v after the late commit, want online", row)
	}
}

// A plain approval is safe only while the row is still pre-connect: a
// pending device whose row went online after the operator's read is
// admitted by the approval, and that must be the locked write.
func TestPlainApprovalOfAConnectedRowIsRefused(t *testing.T) {
	repo := deviceCapRepo(t)
	account := uuid.New()
	p := seedNode(t, repo, account, func(n *node.Node) {
		n.Status = node.StatusOnline
		n.ApprovalStatus = node.ApprovalStatusPendingApproval
	})
	err := repo.UpdateApprovalStatus(context.Background(), p.ID, string(node.ApprovalStatusApproved))
	var required *node.AdmissionRequiredError
	if !errors.As(err, &required) {
		t.Errorf("a plain approval of a connected pending row: err = %v, want AdmissionRequiredError", err)
	}
	if count, _ := repo.CountAdmittedNodes(context.Background()); count != 0 {
		t.Errorf("the plain approval admitted the row (%d admitted)", count)
	}
}

// The commit still does its job for a fresh node: flashing becomes
// ready_for_adoption, with the bake stamped.
func TestWebUSBCommitRevealsAFreshNode(t *testing.T) {
	repo := deviceCapRepo(t)
	account := uuid.New()
	w := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusFlashing })
	svc := capStatusService(t, repo, 1)
	if err := svc.CommitWebUSBFlash(context.Background(), w.ID, account); err != nil {
		t.Fatalf("commit: %v", err)
	}
	row, _ := repo.GetByID(context.Background(), account, w.ID)
	if row == nil || row.Status != node.StatusReadyForAdoption || row.BootloaderFlashedAt == nil {
		t.Errorf("after the commit the node reads %+v, want ready_for_adoption with the flash stamped", row)
	}
}
