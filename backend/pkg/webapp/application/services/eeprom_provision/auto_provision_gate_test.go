package eeprom_provision

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	organizationalUnit "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
)

// defaultPolicy resolves the way an account with nothing set does.
type defaultPolicy struct{}

func (defaultPolicy) ResolveEffectivePolicy(context.Context, uuid.UUID, uuid.UUID) (organizationalUnit.EffectivePolicy, error) {
	return organizationalUnit.ResolveEffectivePolicy(organizationalUnit.NodePolicyInputs{}, nil, nil, nil), nil
}

// The automatic flash at approval (and at a pre-created node's first report)
// is for a device running the recovery system from a provisioning card. A
// device that runs its own OS from an SD card keeps its bootloader: rewriting
// it to fetch from this server first would send every later reboot into
// recovery, because nothing has set the boot target to the local OS.
func TestAutoProvisionOnlyFlashesTheCardBootedRecoverySystem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var asked eepromsigner.SignRequest
		_ = json.NewDecoder(r.Body).Decode(&asked)
		_ = json.NewEncoder(w).Encode(eepromsigner.SignResponse{OK: true, Path: eepromsigner.NodeScopedPath(asked.Serial, asked.NodeID)})
	}))
	defer srv.Close()

	cases := []struct {
		name       string
		bootDevice string
		recovery   bool
		wantFlash  bool
	}{
		{"recovery system from a provisioning card", "sd", true, true},
		{"own OS from an SD card", "sd", false, false},
		{"own OS from NVMe", "nvme", false, false},
		{"netbooted recovery system", "http", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmdr := &recordingCommander{}
			svc := NewService(eepromsigner.NewClient(srv.URL, ""), defaultPolicy{}, cmdr,
				cdnsign.DeriveKey("k"), slog.New(slog.NewTextHandler(io.Discard, nil)))
			n := &nodeDomain.Node{
				ID:          uuid.New(),
				AccountID:   uuid.New(),
				Fingerprint: &nodeDomain.HardwareFingerprint{HardwareSerial: "abcdef12"},
				SystemInfo: &nodeDomain.SystemInfo{Hardware: &nodeDomain.HardwareInfo{
					CPU:  &nodeDomain.CPUInfo{Model: "Raspberry Pi 4 Model B"},
					Boot: &nodeDomain.BootInfo{CurrentBootDevice: tc.bootDevice, RecoveryBootstrap: tc.recovery},
				}},
			}
			triggered, err := svc.MaybeProvisionNetworkFirst(context.Background(), n.AccountID, uuid.New(), n, "https://p", "p:1")
			if err != nil {
				t.Fatal(err)
			}
			if triggered != tc.wantFlash || (len(cmdr.sent) == 1) != tc.wantFlash {
				t.Fatalf("triggered=%v dispatched=%d, want flash=%v", triggered, len(cmdr.sent), tc.wantFlash)
			}
		})
	}
}
