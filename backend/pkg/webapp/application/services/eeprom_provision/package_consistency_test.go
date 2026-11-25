package eeprom_provision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
)

// countingSigner records how many bakes it was asked for and the most that
// ran at once. Each bake takes a moment, as a real one does.
type countingSigner struct {
	calls, inFlight, maxInFlight atomic.Int32
}

func (c *countingSigner) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.calls.Add(1)
		now := c.inFlight.Add(1)
		for {
			prev := c.maxInFlight.Load()
			if now <= prev || c.maxInFlight.CompareAndSwap(prev, now) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		c.inFlight.Add(-1)
		var asked eepromsigner.SignRequest
		_ = json.NewDecoder(r.Body).Decode(&asked)
		_ = json.NewEncoder(w).Encode(eepromsigner.SignResponse{OK: true, Path: eepromsigner.NodeScopedPath(asked.Serial, asked.NodeID)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func onlineCM5Node() *nodeDomain.Node {
	return &nodeDomain.Node{
		ID:          uuid.New(),
		AccountID:   uuid.New(),
		Status:      nodeDomain.StatusOnline,
		Fingerprint: &nodeDomain.HardwareFingerprint{HardwareSerial: "abcdef12"},
		SystemInfo:  &nodeDomain.SystemInfo{Hardware: &nodeDomain.HardwareInfo{CPU: &nodeDomain.CPUInfo{Model: "Raspberry Pi Compute Module 5"}}},
	}
}

// A second provision for a node whose first flash was just dispatched must not
// rebuild the package that flash is downloading: the device fetches the
// manifest and the image as separate requests, and a rebuild between them is
// a checksum mismatch on the device. Approval auto-provisions, and an explicit
// provision a second later is the ordinary way to reach this.
func TestASecondProvisionDoesNotRebakeAPackageInFlight(t *testing.T) {
	signer := &countingSigner{}
	srv := signer.server(t)
	n := onlineCM5Node()
	cmdr := &recordingCommander{node: n}
	svc := NewService(eepromsigner.NewClient(srv.URL, ""), nil, cmdr,
		cdnsign.DeriveKey("k"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	if err := svc.ProvisionNetworkFirstEEPROM(ctx, n.AccountID, uuid.New(), n, nodeDomain.NetworkFirstBootOrder, "https://p", "p:1"); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	// The caller still holds the copy it loaded before the first dispatch.
	err := svc.ProvisionNetworkFirstEEPROM(ctx, n.AccountID, uuid.New(), onlineCopy(n), nodeDomain.NetworkFirstBootOrder, "https://p", "p:1")
	var refusal *app_errors.RequestRefusalError
	if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusConflict || refusal.ErrorCode() != "node_unavailable" {
		t.Fatalf("second provision err = %v, want a typed 409 node_unavailable", err)
	}
	if got := signer.calls.Load(); got != 1 {
		t.Fatalf("signer asked for %d bakes, want 1: the refused request rebuilt the package in flight", got)
	}
	if len(cmdr.sent) != 1 {
		t.Fatalf("dispatched %d commands, want 1", len(cmdr.sent))
	}
}

// Two provisions racing for one node never bake at the same time, and only
// one of them reaches the device.
func TestConcurrentProvisionsForOneNodeAreSerialized(t *testing.T) {
	signer := &countingSigner{}
	srv := signer.server(t)
	n := onlineCM5Node()
	cmdr := &recordingCommander{node: n}
	svc := NewService(eepromsigner.NewClient(srv.URL, ""), nil, cmdr,
		cdnsign.DeriveKey("k"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	other := NewService(eepromsigner.NewClient(srv.URL, ""), nil, cmdr,
		cdnsign.DeriveKey("k"), slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Each request loaded the node before either dispatched.
	copies := []*nodeDomain.Node{onlineCopy(n), onlineCopy(n)}
	var wg sync.WaitGroup
	for i, s := range []*Service{svc, other} {
		wg.Add(1)
		go func(s *Service, loaded *nodeDomain.Node) {
			defer wg.Done()
			_ = s.ProvisionNetworkFirstEEPROM(context.Background(), loaded.AccountID, uuid.New(), loaded, nodeDomain.NetworkFirstBootOrder, "https://p", "p:1")
		}(s, copies[i])
	}
	wg.Wait()
	if got := signer.maxInFlight.Load(); got != 1 {
		t.Fatalf("%d bakes for one node ran at once, want 1", got)
	}
	if got := signer.calls.Load(); got != 1 {
		t.Fatalf("signer asked for %d bakes, want 1", got)
	}
	if len(cmdr.sent) != 1 {
		t.Fatalf("dispatched %d commands, want 1", len(cmdr.sent))
	}
}

func onlineCopy(n *nodeDomain.Node) *nodeDomain.Node {
	cp := *n
	cp.Status = nodeDomain.StatusOnline
	cp.PinnedBySlug = ""
	return &cp
}
