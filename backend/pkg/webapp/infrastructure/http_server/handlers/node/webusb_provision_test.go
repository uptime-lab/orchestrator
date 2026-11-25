package node

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
)

// webusbFakeService stubs only what WebUSBProvision touches; every other call
// panics through the embedded nil interface.
//
// It owns ONE node, stamped with a short serial, in ownerAccount — the shape a
// WebUSB placeholder has before the device ever connects. foreignSerials are
// full serials some OTHER account owns.
type webusbFakeService struct {
	services.NodeService

	ownerAccount   uuid.UUID
	shortSerial    string
	ownedNode      nodeDomain.Node
	foreignSerials map[string]bool

	created    int
	lastCreate node_create.NodeCreateCommand
}

// GetByHardwareSerial mirrors the repository's symmetric suffix fallback
// (infrastructure/database/sql/node_repository.go): a stored SHORT stamp
// matches as the suffix of a FULL query serial, within the account. Kept in the
// fake because it is the lookup this route must not use.
func (f *webusbFakeService) GetByHardwareSerial(_ context.Context, accountID uuid.UUID, serial string) (*nodeDomain.Node, error) {
	if accountID != f.ownerAccount {
		return nil, app_errors.ErrNodeNotFound
	}
	if serial == f.shortSerial || strings.HasSuffix(serial, f.shortSerial) {
		n := f.ownedNode
		return &n, nil
	}
	return nil, app_errors.ErrNodeNotFound
}

func (f *webusbFakeService) ResolveSerialForBake(_ context.Context, accountID uuid.UUID, serial string) (*nodeDomain.Node, error) {
	if f.foreignSerials[serial] {
		return nil, app_errors.ErrHardwareSerialConflict
	}
	if accountID == f.ownerAccount && serial == f.shortSerial {
		n := f.ownedNode
		return &n, nil
	}
	return nil, app_errors.ErrNodeNotFound
}

func (f *webusbFakeService) Create(_ context.Context, cmd node_create.NodeCreateCommand) (*nodeDomain.Node, error) {
	f.created++
	f.lastCreate = cmd
	return &nodeDomain.Node{ID: cmd.ID, Name: cmd.Name}, nil
}

func (f *webusbFakeService) StampWebUSBPlaceholder(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}

func (f *webusbFakeService) UpdateStatusWithAccount(_ context.Context, _, _ uuid.UUID, _ string) error {
	return nil
}

func (f *webusbFakeService) GetAdoptionInfo(_ context.Context, _, _ uuid.UUID, _, _ string) (string, error) {
	return "YWRvcHRpb24=", nil
}

// webusbTestHandler wires the handler against a recording signer stub.
func webusbTestHandler(t *testing.T, svc services.NodeService) (*NodeHandler, *[]eepromsigner.SignRequest) {
	t.Helper()
	var signed []eepromsigner.SignRequest
	signer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req eepromsigner.SignRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		signed = append(signed, req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(eepromsigner.SignResponse{
			OK:     true,
			Serial: req.Serial,
			Path:   eepromsigner.NodeScopedPath(req.Serial, req.NodeID),
			Files:  []string{"pieeprom.upd", "pieeprom.sig"},
		})
	}))
	t.Cleanup(signer.Close)

	return &NodeHandler{
		nodeService: svc,
		signer:      eepromsigner.NewClient(signer.URL, ""),
		logger:      slog.Default().With("handler", "node"),
		config:      &config.Config{},
	}, &signed
}

func webusbRequest(t *testing.T, h *NodeHandler, accountID, userID uuid.UUID, serial string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"soc":"cm4","serial":"` + serial + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/eeprom/webusb-provision", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("accountID", accountID.String())
	c.Set("userID", userID.String())
	if err := h.WebUSBProvision(c); err != nil {
		t.Fatalf("handler returned a raw error: %v", err)
	}
	return rec
}

// A per-serial bake writes into a package path keyed on the serial alone, so a
// caller must never reach the "existing node" branch for a serial another
// account owns. The account here holds a placeholder stamped with the SHORT
// serial, which the repository's suffix fallback matches against the victim's
// FULL serial — that match must not be what decides the bake.
func TestWebUSBProvisionRefusesAFullSerialOwnedByAnotherAccount(t *testing.T) {
	const short = "abcdef12"
	const victimFull = "10000000abcdef12"

	attacker := uuid.New()
	svc := &webusbFakeService{
		ownerAccount:   attacker,
		shortSerial:    short,
		ownedNode:      nodeDomain.Node{ID: uuid.New(), Name: "placeholder", AccountID: attacker},
		foreignSerials: map[string]bool{victimFull: true},
	}
	h, signed := webusbTestHandler(t, svc)

	rec := webusbRequest(t, h, attacker, uuid.New(), victimFull)

	if rec.Code != http.StatusConflict {
		t.Errorf("status %d, want %d", rec.Code, http.StatusConflict)
	}
	if len(*signed) != 0 {
		t.Errorf("the signer was asked to bake %d package(s) for a foreign serial: %+v", len(*signed), *signed)
	}
	if svc.created != 0 {
		t.Errorf("created %d node(s) for a foreign serial, want 0", svc.created)
	}
}

// The ordinary re-flash still works: the account's own node, matched by its own
// exact serial, is re-baked without creating anything.
func TestWebUSBProvisionRebakesTheAccountsOwnNode(t *testing.T) {
	const short = "abcdef12"

	owner := uuid.New()
	own := nodeDomain.Node{ID: uuid.New(), Name: "Node-abcdef12", AccountID: owner}
	svc := &webusbFakeService{
		ownerAccount: owner,
		shortSerial:  short,
		ownedNode:    own,
	}
	h, signed := webusbTestHandler(t, svc)

	rec := webusbRequest(t, h, owner, uuid.New(), short)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if len(*signed) != 1 || (*signed)[0].Serial != short {
		t.Fatalf("signer calls %+v, want one bake for %q", *signed, short)
	}
	if svc.created != 0 {
		t.Errorf("created %d node(s) for an existing one, want 0", svc.created)
	}
	if !strings.Contains(rec.Body.String(), own.ID.String()) {
		t.Errorf("response does not name the existing node: %s", rec.Body.String())
	}
}

// A flash that names no login invents none: provisioning credentials are
// optional at flash time, and the server sets no default username or password.
// A node flashed with no login carries no provisioning account at all, and
// provision_connection says so instead of naming a user out of thin air.
func TestWebUSBProvisionInventsNoDefaultUsername(t *testing.T) {
	svc := &webusbFakeService{ownerAccount: uuid.New(), shortSerial: "ffffffff"}
	h, _ := webusbTestHandler(t, svc)

	rec := webusbRequest(t, h, uuid.New(), uuid.New(), "abcd1234")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if svc.created != 1 {
		t.Fatalf("created %d node(s), want 1", svc.created)
	}
	if got := svc.lastCreate.DefaultUsername; got != "" {
		t.Errorf("DefaultUsername = %q, want empty: the flash request named no login", got)
	}
	if got := svc.lastCreate.DefaultPassword; got != "" {
		t.Errorf("DefaultPassword = %q, want empty", got)
	}
}
