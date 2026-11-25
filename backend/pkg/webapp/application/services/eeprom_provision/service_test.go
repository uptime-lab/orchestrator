package eeprom_provision

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
)

type recordingCommander struct {
	mu   sync.Mutex
	sent []node_command.NodeCommandCommand
	// node, when set, is what the service reads back as the stored row; a
	// dispatch pins it to "restarting" the way command registration does.
	node *nodeDomain.Node
}

func (r *recordingCommander) GetByIDAndAccount(context.Context, uuid.UUID, uuid.UUID) (*nodeDomain.Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.node == nil {
		return nil, nil
	}
	cp := *r.node
	return &cp, nil
}

func (r *recordingCommander) GetAdoptionInfo(context.Context, uuid.UUID, uuid.UUID, string, string) (string, error) {
	return "YWRvcHRpb24=", nil
}

func (r *recordingCommander) SendCommand(_ context.Context, cmd node_command.NodeCommandCommand) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, cmd)
	if r.node != nil {
		r.node.Status = nodeDomain.StatusRestarting
		r.node.PinnedBySlug = cmd.Command
	}
	return nil
}

// The agent's capability names the node the package was baked for, and the
// signer is asked to write it into that node's own directory.
func TestProvisionBindsTheCapabilityToTheNode(t *testing.T) {
	var asked eepromsigner.SignRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&asked)
		_ = json.NewEncoder(w).Encode(eepromsigner.SignResponse{OK: true, Path: eepromsigner.NodeScopedPath(asked.Serial, asked.NodeID)})
	}))
	defer srv.Close()

	key := cdnsign.DeriveKey("test-key-for-provision")
	cmdr := &recordingCommander{}
	svc := NewService(eepromsigner.NewClient(srv.URL, ""), nil, cmdr, key, slog.New(slog.NewTextHandler(io.Discard, nil)))

	account := uuid.New()
	n := &nodeDomain.Node{
		ID:          uuid.New(),
		AccountID:   account,
		Fingerprint: &nodeDomain.HardwareFingerprint{HardwareSerial: "abcdef12"},
		SystemInfo:  &nodeDomain.SystemInfo{Hardware: &nodeDomain.HardwareInfo{CPU: &nodeDomain.CPUInfo{Model: "Raspberry Pi Compute Module 4"}}},
	}
	if err := svc.ProvisionNetworkFirstEEPROM(context.Background(), account, uuid.New(), n, nodeDomain.NetworkFirstBootOrder, "https://p", "p:1"); err != nil {
		t.Fatal(err)
	}
	if asked.NodeID != n.ID.String() {
		t.Fatalf("signer asked for node %q, want %s", asked.NodeID, n.ID)
	}
	if len(cmdr.sent) != 1 {
		t.Fatalf("dispatched %d commands", len(cmdr.sent))
	}
	exp, _ := cmdr.sent[0].Params["pkg_exp"].(string)
	sig, _ := cmdr.sent[0].Params["pkg_sig"].(string)
	gotAccount, gotNode, err := cdnsign.VerifyEEPROMPackage("abcdef12", exp, sig, key)
	if err != nil || gotAccount != account || gotNode != n.ID {
		t.Fatalf("capability names %s/%s (%v), want this account and node", gotAccount, gotNode, err)
	}
	// The path-only check the handler used to make no longer accepts it.
	if cdnsign.Verify("by-serial/abcdef12", exp, sig, key) == nil {
		t.Fatal("capability still verifies as a serial-only capability")
	}
}
