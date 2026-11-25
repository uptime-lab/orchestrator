package eeprom_provision

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
)

// A node that has not reported its serial (one still awaiting its first
// connection) is refused for its state, 409, before the signer is asked for
// anything. It answered 500 "failed to provision EEPROM".
func TestProvisioningANodeWithNoSerialIsAStateRefusal(t *testing.T) {
	cmdr := &recordingCommander{}
	svc := NewService(eepromsigner.NewClient("http://127.0.0.1:1", ""), nil, cmdr,
		cdnsign.DeriveKey("k"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	n := &nodeDomain.Node{ID: uuid.New(), AccountID: uuid.New()}

	err := svc.ProvisionNetworkFirstEEPROM(context.Background(), n.AccountID, uuid.New(), n,
		nodeDomain.NetworkFirstBootOrder, "https://p", "p:1")
	var refusal *app_errors.RequestRefusalError
	if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusConflict || refusal.ErrorCode() != "no_hardware_serial" {
		t.Fatalf("err = %v, want a typed 409 no_hardware_serial", err)
	}
	if len(cmdr.sent) != 0 {
		t.Fatalf("dispatched %d commands for a refused provision", len(cmdr.sent))
	}
}
