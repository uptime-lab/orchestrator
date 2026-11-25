// Package eeprom_provision centralizes network-first EEPROM provisioning so it
// can be triggered from multiple transports (HTTP approve/on-demand, the gRPC
// first-system-info hook, and the future SSH-adopt runner) without any of them
// importing Echo or duplicating the signer+dispatch flow.
package eeprom_provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	nodeCommandDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	organizationalUnit "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/eepromsigner"
)

// NodeCommander is the subset of the node service this package needs: build a
// per-node adoption blob and dispatch a command to the connected agent.
type NodeCommander interface {
	GetByIDAndAccount(ctx context.Context, nodeID, accountID uuid.UUID) (*nodeDomain.Node, error)
	GetAdoptionInfo(ctx context.Context, accountID, nodeID uuid.UUID, platformURL, grpcEndpoint string) (string, error)
	SendCommand(ctx context.Context, cmd node_command.NodeCommandCommand) error
}

// PolicyResolver resolves a node's effective inheritable policy (node -> OU ->
// root). Satisfied by the organizational_unit service.
type PolicyResolver interface {
	ResolveEffectivePolicy(ctx context.Context, accountID, nodeID uuid.UUID) (organizationalUnit.EffectivePolicy, error)
}

// Service owns the network-first EEPROM provisioning flow. signer/policyResolver
// may be nil, which disables provisioning (callers must handle a nil-signer skip
// gracefully — see MaybeProvisionNetworkFirst).
type Service struct {
	signer         *eepromsigner.Client
	policyResolver PolicyResolver
	nodeCommander  NodeCommander
	// signKey mints the short-lived HMAC capability the agent must present to
	// download the per-device EEPROM package (which embeds the adoption token).
	signKey []byte
	logger  *slog.Logger
}

// NewService constructs the provisioning service. Dependencies are injected;
// signer and policyResolver are allowed to be nil (feature disabled). signKey
// is cdnsign.DeriveKey(cfg.CDNSigningSecret()) — used to sign per-device
// package download URLs so knowing a serial is not enough to fetch its
// adoption token.
func NewService(signer *eepromsigner.Client, policyResolver PolicyResolver, nodeCommander NodeCommander, signKey []byte, logger *slog.Logger) *Service {
	if nodeCommander == nil {
		panic("eeprom_provision.NewService: nodeCommander is required")
	}
	if logger == nil {
		panic("eeprom_provision.NewService: logger is required")
	}
	return &Service{
		signer:         signer,
		policyResolver: policyResolver,
		nodeCommander:  nodeCommander,
		signKey:        signKey,
		logger:         logger.With("service", "eeprom_provision"),
	}
}

// Enabled reports whether provisioning can run (signer + policy resolver wired).
func (s *Service) Enabled() bool {
	return s.signer != nil && s.policyResolver != nil
}

// MaybeProvisionNetworkFirst provisions the network-first EEPROM when the
// effective network_first policy is on and the node is running the recovery
// system from a provisioning card. It returns whether a flash was triggered, and an error only for a
// genuine failure (signer/dispatch) — a deliberate policy/boot-device skip
// returns (false, nil). Callers use the returned error to decide whether to keep
// a one-shot pending flag armed for a later retry.
func (s *Service) MaybeProvisionNetworkFirst(ctx context.Context, accountID, userID uuid.UUID, n *nodeDomain.Node, platformURL, grpcEndpoint string) (triggered bool, err error) {
	if !s.Enabled() || n == nil {
		return false, nil
	}

	// Only the recovery system booted from a provisioning card. The boot
	// medium alone is not the test: a device running its OWN operating system
	// from an SD card reports "sd" too, and flashing it would make its
	// bootloader ask this server first while its boot target still resolves
	// to recovery, so every later reboot would land in recovery. A managed OS
	// changes its bootloader only through an explicit provision, which also
	// sets the boot target. A netbooted device has no writable boot partition
	// for the recovery.bin flash.
	if !n.IsRecoveryBootstrap() || n.CurrentBootDevice() != "sd" {
		s.logger.Info("skipping network-first auto-provision: node is not the recovery system on a provisioning card", "node_id", n.ID, "boot_device", n.CurrentBootDevice(), "recovery_bootstrap", n.IsRecoveryBootstrap())
		return false, nil
	}

	policy, perr := s.policyResolver.ResolveEffectivePolicy(ctx, accountID, n.ID)
	if perr != nil {
		return false, fmt.Errorf("resolve effective policy: %w", perr)
	}
	if !policy.NetworkFirst.Value {
		s.logger.Info("skipping network-first auto-provision: policy off", "node_id", n.ID)
		return false, nil
	}

	if err := s.ProvisionNetworkFirstEEPROM(ctx, accountID, userID, n, nodeDomain.NetworkFirstBootOrder, platformURL, grpcEndpoint); err != nil {
		return false, err
	}
	s.logger.Info("network-first EEPROM flash triggered; device will reboot into netboot", "node_id", n.ID)
	return true, nil
}

// ProvisionNetworkFirstEEPROM asks the isolated signer to bake a per-device
// EEPROM (this node's serial in HTTP_PATH + its adoption token + the requested
// boot order, from the latest firmware base the signer holds), then commands the
// agent to fetch and flash it. The agent downloads the package from
// net_install/eeprom/by-serial/<serial>/.
//
// A node's package directory holds ONE build, and the agent fetches it as
// separate requests (manifest first, then each file), so a second bake while a
// dispatched flash is still downloading hands the device a manifest from one
// build and an image from the other — an integrity failure on a request that
// was fine. Two rules keep that from happening: bakes for one node never
// overlap (lockNodeProvision), and a node that a dispatched command has pinned
// to "restarting" is refused BEFORE anything is baked. The dispatch below
// refuses that state too, but only after the bake, which is too late: the
// refused request has already replaced the package the earlier command is
// fetching.
func (s *Service) ProvisionNetworkFirstEEPROM(ctx context.Context, accountID, userID uuid.UUID, n *nodeDomain.Node, bootOrder []string, platformURL, grpcEndpoint string) error {
	if s.signer == nil {
		return fmt.Errorf("eeprom signer is not configured")
	}
	if n == nil {
		return fmt.Errorf("node is required")
	}
	unlock := lockNodeProvision(n.ID)
	defer unlock()
	// Re-read under the lock: the caller's copy predates any dispatch that a
	// concurrent request completed while this one waited.
	if fresh, err := s.nodeCommander.GetByIDAndAccount(ctx, n.ID, accountID); err == nil && fresh != nil {
		n = fresh
	} else if err != nil {
		return fmt.Errorf("load node: %w", err)
	}
	if n.Status == nodeDomain.StatusRestarting {
		return app_errors.StateConflict("node_unavailable", fmt.Errorf(
			"node is restarting (pinned by %s); a dispatched command may still be fetching this node's EEPROM package, so it is not rebuilt now — retry once the node is back online",
			pinnedBy(n)))
	}
	serial := n.GetHardwareSerial()
	// Both facts come from the device's own reports, so a node that has not
	// reported them yet (one still awaiting its first connection) is refused
	// for its state, not for the request.
	if serial == "" {
		return app_errors.StateConflict("no_hardware_serial", errors.New("node has no hardware serial; it is reported when the device first connects"))
	}
	soc := n.SoC()
	if soc == "" {
		return app_errors.StateConflict("unknown_soc", errors.New("could not determine SoC (cm4/cm5) from node system info"))
	}
	hexOrder, err := nodeDomain.BootOrderToHex(bootOrder)
	if err != nil {
		return fmt.Errorf("invalid boot order: %w", err)
	}

	adoption, err := s.nodeCommander.GetAdoptionInfo(ctx, accountID, n.ID, platformURL, grpcEndpoint)
	if err != nil {
		return fmt.Errorf("build adoption info: %w", err)
	}

	if _, err := s.signer.Sign(ctx, eepromsigner.SignRequest{
		SoC:    soc,
		Serial: serial,
		// GetAdoptionInfo already returns base64(JSON); the EEPROM UPTIME_TOKEN is
		// exactly that single-base64 blob (the agent decodes it once). Do NOT
		// re-encode here — double base64 makes UPTIME_TOKEN unparseable on device.
		AdoptionB64: adoption,
		BootOrder:   hexOrder,
		// The package lands in this node's own directory; the capability below
		// names the node and opens nothing else.
		NodeID: n.ID.String(),
	}); err != nil {
		return fmt.Errorf("sign per-device EEPROM: %w", err)
	}

	// Trigger the flash: the agent prefers the by-serial package we just signed.
	orderParam := make([]interface{}, len(bootOrder))
	for i, d := range bootOrder {
		orderParam[i] = d
	}
	params := map[string]interface{}{"boot_order": orderParam}
	// Mint the short-lived capability the agent presents to download the
	// per-device (adoption-token-bearing) package. 15 min covers fetch + flash.
	//
	// Mandatory, not best-effort. Without it the agent cannot fetch the image we
	// just signed and falls back to a SHARED one, which overwrites this device's
	// per-serial netboot identity — so a command emitted with no capability is
	// strictly worse than no command at all. The dispatch path refuses one
	// anyway; failing here says why, at the point that can fix it.
	if len(s.signKey) == 0 {
		return fmt.Errorf("cannot dispatch update_boot_order: the CDN signing key is not configured, " +
			"so the agent could not fetch the per-serial EEPROM image just signed for this device")
	}
	exp, sig := cdnsign.SignEEPROMPackage(serial, accountID, n.ID, s.signKey, 15*time.Minute)
	params["pkg_exp"] = exp
	params["pkg_sig"] = sig
	return s.nodeCommander.SendCommand(ctx, node_command.NodeCommandCommand{
		NodeID:      n.ID,
		Command:     nodeCommandDomain.SlugUpdateBootOrder,
		Params:      params,
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
}

// nodeProvisionLocks serializes bake+dispatch per node across every Service
// instance in the process (the HTTP handler and the adoption flows each build
// their own). One small entry per node ever provisioned by this process.
var nodeProvisionLocks sync.Map // uuid.UUID -> *sync.Mutex

func lockNodeProvision(nodeID uuid.UUID) func() {
	m, _ := nodeProvisionLocks.LoadOrStore(nodeID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func pinnedBy(n *nodeDomain.Node) string {
	if n.PinnedBySlug != "" {
		return n.PinnedBySlug
	}
	return "a command"
}
