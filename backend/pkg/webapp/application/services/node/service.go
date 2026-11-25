package node

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/node_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/node_commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/adoption_code"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

// patchMarkerRe bounds patch_file block ids: one line, shell-safe charset.
// The agent matches marker lines per line, so a marker with an interior
// newline never matches its own pair and every apply appends a fresh block.
var patchMarkerRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// Service handles node-related business logic
type Service struct {
	nodeRepo             repository.NodeRepository
	sshKeyRepo           repository.SSHKeyRepository
	communicationService service.NodeCommunicationService
	fileService          services.FileService
	nodeCommandService   services.NodeCommandService
	nodeCommandClient    node_commands.NodeCommandClient
	// trackedFileRepo is wired after construction (SetTrackedFileRepository).
	trackedFileRepo repository.NodeTrackedFileRepository
	// accessGeneration resolves the DESIRED access hash so a managed-OS connect
	// can compare it with what the device confirmed (SetAccessGenerationResolvers).
	// Optional: without it the comparison is skipped and the older, procedural
	// self-heal layers still work.
	accessGeneration *accessGenerationDeps
	// nodeFileLimits are the install's tracked-file quotas (SetNodeFileLimits).
	// nil means nothing wired them, and the service falls back to the shipped
	// defaults rather than to unlimited.
	nodeFileLimits  *node_file.Limits
	websocketClient websockets.WebsocketClient
	// adoptionCodeRepo backs the one-time adoption-link exchange (optional;
	// wired via SetAdoptionCodeRepo).
	adoptionCodeRepo repository.AdoptionCodeRepository
	// anomalyRepo records operator-facing signals (optional; wired via
	// SetAnomalyRepo). Optional because an anomaly is a report about a
	// decision, never part of one — the service works identically without it.
	anomalyRepo repository.InstanceAnomalyRepository
	// imageService backs the capture_image command: a capture creates a pending
	// image record whose id the command carries (optional; wired via
	// SetImageService). Capture is refused when it is not configured.
	imageService services.ImageService
	// executionAbandoner settles workflow runs pinned to a node being deleted
	// (SetExecutionAbandoner). See the Delete comment.
	executionAbandoner ExecutionAbandoner
	// utilityStateCleaner drops the node's utility ladder on delete
	// (SetUtilityStateCleaner).
	utilityStateCleaner UtilityStateCleaner
	// utilityReconciler self-heals the node's utilities on a managed-OS
	// report (SetUtilityReconciler).
	utilityReconciler UtilityReconciler
	// identify/button registry (identify_state.go). Guarded by its own mutex:
	// it is touched from HTTP dispatch and the gRPC event path.
	identifyStateMu  sync.Mutex
	identifyStates   map[uuid.UUID]*identifyEntry
	identifyDuration IdentifyDurationSource
	// deviceProfiles is the loaded hardware registry and deviceProfileResolver
	// walks the node -> OU -> account inheritance (both wired via setters in
	// main.go). Optional in the wiring sense only: every device-profile method
	// refuses fail-closed without them.
	deviceProfiles        *device_profile.Catalog
	deviceProfileResolver DeviceProfileResolver
	// favoriteRepo clears the per-user favorite marks of a deleted node
	// (SetNodeFavoriteRepo). Optional in the wiring sense only — see Delete.
	favoriteRepo repository.NodeFavoriteRepository
	// attachmentRepo drops the secret attachments targeting a deleted node
	// (SetSecretAttachmentRepo). Optional in the wiring sense only — see Delete.
	attachmentRepo repository.SecretAttachmentRepository
	// secretRepo removes a deleted node's attribute-backing secrets
	// (SetSecretRepo). Optional in the wiring sense only — see Delete.
	secretRepo repository.SecretRepository
	// accessEntryRepo detaches the access entries scoped to a deleted node
	// (SetAccessEntryRepo). Optional in the wiring sense only — see Delete.
	accessEntryRepo repository.AccessEntryRepository
	// tombstoneRepo records what a deleted node was, so its device can be taken
	// back (SetNodeTombstoneRepo). Optional in the wiring sense only: without it
	// a delete behaves exactly as it did before tombstones existed, which is to
	// say it strands the device.
	tombstoneRepo repository.NodeTombstoneRepository
	// unitAssigner puts a restored node back into its recorded unit, under the
	// same lock an ordinary assignment takes (SetUnitAssigner). Without it a
	// restored node comes back unassigned: the recorded unit cannot be checked,
	// and a reference to a unit that no longer exists is worse than none.
	unitAssigner UnitAssigner

	// maxNodesPerAccount is the per-account fleet cap (SetMaxNodesPerAccount,
	// 0 = unlimited). A setter rather than a constructor argument because this
	// service already takes nine and the value is optional by design.
	maxNodesPerAccount int
	// bootHost is this install's network-boot host (SetBootHost); a node whose
	// bootloader names another cannot be sent to this install's recovery.
	bootHost string
	// nodeQuotaCounter is the ONE repository capability the fleet cap needs. It
	// is nodeRepo in production; naming it separately is what lets the guard be
	// tested against a two-line fake instead of a forty-method one.
	nodeQuotaCounter nodeCounter
	// deviceCapResolver is the device entitlement (SetDeviceCapResolver). Nil
	// means the FREE cap applies, not "unlimited" — see deviceCapPolicy.
	deviceCapResolver DeviceCapResolver
	// capRefusalRecords throttles the record of a device-driven cap refusal
	// (underDeviceCap). The zero value is ready.
	capRefusalRecords refusalThrottle
	// capUnverifiedRecords throttles the ERROR line of a device-driven
	// admission refused because the cap could not be checked. Its own map,
	// so a database fault never suppresses a cap refusal's audit row.
	capUnverifiedRecords refusalThrottle
	// accountLimitResolver answers the per-account device limit
	// (SetAccountDeviceLimitResolver). Nil means no per-account limit, which
	// is also what an install with ACCOUNT_DEVICE_LIMIT=0 answers.
	accountLimitResolver AccountDeviceLimitResolver
	// accountLimitRefusalRecords throttles the record of a device-driven
	// refusal at an account's limit, like capRefusalRecords.
	accountLimitRefusalRecords refusalThrottle
	audit                      *audit.Recorder
	logger                     *slog.Logger
}

// SetNodeFavoriteRepo wires per-user favorite cleanup into node deletion.
func (s *Service) SetNodeFavoriteRepo(repo repository.NodeFavoriteRepository) {
	s.favoriteRepo = repo
}

// SetSecretAttachmentRepo wires node-scoped secret attachment cleanup into node
// deletion.
func (s *Service) SetSecretAttachmentRepo(repo repository.SecretAttachmentRepository) {
	s.attachmentRepo = repo
}

// SetSecretRepo wires attribute-backing secret cleanup into node deletion.
func (s *Service) SetSecretRepo(repo repository.SecretRepository) {
	s.secretRepo = repo
}

// SetAccessEntryRepo wires node-scoped access entry detachment into node
// deletion.
func (s *Service) SetAccessEntryRepo(repo repository.AccessEntryRepository) {
	s.accessEntryRepo = repo
}

// SetNodeTombstoneRepo wires deleted-node records into node deletion and enables
// the restore path.
func (s *Service) SetNodeTombstoneRepo(repo repository.NodeTombstoneRepository) {
	s.tombstoneRepo = repo
}

// UnitAssigner points a node at an organizational unit while holding a share
// lock on the unit, answering app_errors.ErrOrganizationalUnitNotFound when the
// unit does not exist. repository.OUMutationRepository satisfies it.
type UnitAssigner interface {
	AssignNodeOU(ctx context.Context, accountID, nodeID uuid.UUID, ouID *uuid.UUID) error
}

// SetUnitAssigner wires the guarded unit assignment the restore path uses.
func (s *Service) SetUnitAssigner(a UnitAssigner) {
	s.unitAssigner = a
}

// SetImageService wires the image service used to create the pending image
// record for a capture_image command.
func (s *Service) SetImageService(svc services.ImageService) {
	s.imageService = svc
}

// SetAdoptionCodeRepo wires the one-time adoption-code store.
func (s *Service) SetAdoptionCodeRepo(repo repository.AdoptionCodeRepository) {
	s.adoptionCodeRepo = repo
}

// SetAnomalyRepo wires operator-facing anomaly recording.
func (s *Service) SetAnomalyRepo(repo repository.InstanceAnomalyRepository) {
	s.anomalyRepo = repo
}

// ExecutionAbandoner settles the workflow runs of a node that no longer exists.
// A narrow local interface wired by a setter, because the workflow execution
// service already depends on this one — taking the concrete type would be an
// import cycle.
type ExecutionAbandoner interface {
	AbandonNodeExecutions(ctx context.Context, accountID, nodeID uuid.UUID) (int, error)
}

// SetExecutionAbandoner wires workflow-run cleanup into node deletion.
//
// Optional only in the wiring sense: without it a delete leaves the node's
// workflow runs active forever, which is the bug. main.go must set it.
func (s *Service) SetExecutionAbandoner(a ExecutionAbandoner) {
	s.executionAbandoner = a
}

// UtilityStateCleaner drops a deleted node's utility ladder. Same shape and
// reasons as ExecutionAbandoner: the utility service depends on this one, so
// the concrete type would be an import cycle. A tombstone restore deliberately
// gets a CLEAN ladder — the reconcile loop re-converges it against reality
// rather than trusting rows recorded before the device left.
type UtilityStateCleaner interface {
	CleanupNode(ctx context.Context, accountID, nodeID uuid.UUID) error
}

// SetUtilityStateCleaner wires utility-ladder cleanup into node deletion.
func (s *Service) SetUtilityStateCleaner(u UtilityStateCleaner) {
	s.utilityStateCleaner = u
}

// UtilityReconciler converges a node's utilities when the ladder disagrees
// with what the assignments resolve to (docs/UTILITIES-PLAN.md §3.5, phase 2).
// Same narrow-interface-by-setter shape as UtilityStateCleaner and for the
// same reason: the utility service depends on this one.
type UtilityReconciler interface {
	ReconcileNode(ctx context.Context, accountID, nodeID uuid.UUID)
}

// SetUtilityReconciler wires the utility reconcile loop into the managed-OS
// system-info report.
func (s *Service) SetUtilityReconciler(u UtilityReconciler) {
	s.utilityReconciler = u
}

// NewService creates a new node service with all dependencies
// Optional dependencies can be nil if not available
func NewService(
	nodeRepo repository.NodeRepository,
	sshKeyRepo repository.SSHKeyRepository,
	communicationService service.NodeCommunicationService,
	fileService services.FileService,
	nodeCommandService services.NodeCommandService,
	nodeCommandClient node_commands.NodeCommandClient,
	websocketClient websockets.WebsocketClient,
	auditRecorder *audit.Recorder,
	logger *slog.Logger,
) *Service {
	if auditRecorder == nil {
		panic("node.Service requires an audit recorder")
	}
	return &Service{
		nodeRepo:             nodeRepo,
		nodeQuotaCounter:     nodeRepo,
		sshKeyRepo:           sshKeyRepo,
		communicationService: communicationService,
		fileService:          fileService,
		nodeCommandService:   nodeCommandService,
		nodeCommandClient:    nodeCommandClient,
		websocketClient:      websocketClient,
		audit:                auditRecorder,
		logger:               logger,
	}
}

// nodeCounter is the narrow view of the node repository the fleet cap uses.
type nodeCounter interface {
	CountByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error)
}

// SetMaxNodesPerAccount wires the per-account fleet cap (0 = unlimited).
func (s *Service) SetMaxNodesPerAccount(max int) { s.maxNodesPerAccount = max }

// SetBootHost wires this install's network-boot host ("" = unknown, which
// refuses nothing).
func (s *Service) SetBootHost(host string) { s.bootHost = host }

// assertNodeQuota refuses a new node once the account is at its fleet cap
// (quota.max_nodes_per_account, 0 = unlimited).
//
// It fails OPEN if the count query fails, matching every other quota guard
// here: a database hiccup must not become "this account can no longer adopt
// hardware". The cap exists to stop a runaway, and a runaway is not what a
// failed COUNT looks like.
//
// Deliberately NOT called from the tombstone restore path. A restore returns a
// node that was already part of this fleet, and refusing it would strand a real
// device holding an identity nothing else can accept — the exact failure the
// deleted-node record exists to prevent.
func (s *Service) assertNodeQuota(ctx context.Context, accountID uuid.UUID) error {
	max := s.maxNodesPerAccount
	if max <= 0 {
		return nil
	}
	if s.nodeQuotaCounter == nil {
		return nil
	}
	count, err := s.nodeQuotaCounter.CountByAccountID(ctx, accountID)
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to count the account's nodes; allowing the create",
			"account_id", accountID, "error", err)
		return nil
	}
	if count >= int64(max) {
		return fmt.Errorf("%w (max %d)", app_errors.ErrNodeLimitReached, max)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, cmd node_create.NodeCreateCommand) (*node.Node, error) {
	if err := s.assertNodeQuota(ctx, cmd.AuthContext.AccountID); err != nil {
		return nil, err
	}

	// Neither provisioning credential is required any more. They describe ONE
	// optional thing — a local console login — and access to a managed node
	// comes from access entries, applied by the agent after it connects. A node
	// created with neither is not broken: it has no console account until
	// provision_connection is dispatched, and that command refuses to run
	// without a password or a key rather than creating a dead end.
	//
	// The OU and account-policy levels still fill blanks, so an account that
	// wants a console account on every node sets it once there.

	// Resolve the SSH key: use an explicit one if given, otherwise fall back to
	// the account default. A missing default is NOT an error — a node adopts via
	// its token, not SSH, so it can be created keyless (SSHKeyID stays nil) and
	// the operator can attach a key later. This keeps token-adoption flows
	// (WebUSB flash, one-command install) working on fresh accounts with no keys.
	var sshKeyID *uuid.UUID
	switch {
	case cmd.SSHKeyID != uuid.Nil:
		// An explicit key must resolve inside the caller's account — the same
		// check Update performs. Without it a foreign or fabricated UUID is
		// stored and provisioning later degrades to "no key" with only a log
		// line to explain it.
		if _, err := s.sshKeyRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.SSHKeyID); err != nil {
			return nil, fmt.Errorf("invalid SSH key ID: %w", err)
		}
		id := cmd.SSHKeyID
		sshKeyID = &id
	default:
		defaultKey, err := s.sshKeyRepo.GetDefaultByAccountID(ctx, cmd.AuthContext.AccountID)
		switch {
		case err == nil:
			sshKeyID = &defaultKey.ID
		case errors.Is(err, app_errors.ErrSSHKeyNotFound):
			// No default key on the account — leave the node keyless.
		default:
			return nil, fmt.Errorf("failed to resolve default SSH key: %w", err)
		}
	}

	// Create new node with minimal information
	// Hardware details will be populated during agent registration
	newNode := &node.Node{
		ID:              cmd.ID,
		AccountID:       cmd.AuthContext.AccountID,
		Name:            cmd.Name,
		Hostname:        "",  // Will be set during registration
		IPv4:            "",  // Will be set during registration
		MacAddress:      "",  // Will be set during registration
		Fingerprint:     nil, // Will be set during registration
		UniqueToken:     uuid.New().String(),
		Status:          node.StatusReadyForAdoption,
		SSHKeyID:        sshKeyID,
		DefaultUsername: cmd.DefaultUsername,
		DefaultPassword: cmd.DefaultPassword,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// Opt-in network-first provisioning: pin a per-node override (so the
	// auto-EEPROM guard passes regardless of OU/account default) and arm the
	// one-shot flag consumed on the first system-info report.
	if cmd.ProvisionNetworkFirst {
		networkFirst := true
		newNode.NetworkFirst = &networkFirst
		newNode.AutoProvisionEEPROMPending = true
	}

	// Under the device cap: the courtesy check every row-creating path
	// shares (admit), so a typed refusal here reaches the operator's create,
	// the browser flash and the SSH adoption alike.
	if err := s.admit(ctx, newNode, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID), ""); err != nil {
		return nil, err
	}

	// Emit websocket event
	s.notifyNodeCreated(*newNode)

	return newNode, nil
}

// Update updates an existing node if it belongs to the account
func (s *Service) Update(ctx context.Context, cmd node_update.NodeUpdateCommand) (*node.Node, error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}

	// Get existing node to verify ownership
	existingNode, err := s.nodeRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}

	// Update fields if provided
	// Note: Hostname and IPv4 are intentionally excluded - they can only be set during node registration
	if cmd.Name != nil {
		existingNode.Name = *cmd.Name
	}
	// Handle SSH key: if provided, update it
	// When frontend sends zero UUID (00000000-0000-0000-0000-000000000000), it will be parsed as uuid.Nil
	// This allows clearing the key by sending the zero UUID string
	if cmd.SSHKeyID != nil {
		if *cmd.SSHKeyID == uuid.Nil {
			// Zero UUID means clear the SSH key link
			existingNode.SSHKeyID = nil
		} else {
			// Verify the SSH key exists and belongs to the account
			_, err := s.sshKeyRepo.GetByID(ctx, cmd.AuthContext.AccountID, *cmd.SSHKeyID)
			if err != nil {
				return nil, fmt.Errorf("invalid SSH key ID: %w", err)
			}
			existingNode.SSHKeyID = cmd.SSHKeyID
		}
	}
	if cmd.DefaultUsername != nil {
		existingNode.DefaultUsername = *cmd.DefaultUsername
	}
	if cmd.DefaultPassword != nil {
		existingNode.DefaultPassword = *cmd.DefaultPassword
		// The caller explicitly supplied the field (a replacement or an empty
		// clear), so the "preserve undecryptable ciphertext for a future key
		// restore" guard in the repository must stand down — with the flag
		// still set, an explicit CLEAR of an unreadable password answered 200
		// and changed nothing.
		existingNode.DefaultPasswordUnreadable = false
	}
	if cmd.Notes != nil {
		existingNode.Notes = *cmd.Notes
	}

	existingNode.UpdatedAt = time.Now()

	// Write the operator-editable columns only. Saving the whole row back would
	// revert everything the gRPC layer wrote between the read above and here —
	// most consequentially the `restarting` pin, which exists to survive a
	// reboot and whose loss makes handleDisconnection mark the node offline.
	err = s.nodeRepo.UpdateOperatorEditable(ctx, existingNode)
	if err != nil {
		return nil, err
	}

	// Emit websocket event
	s.notifyNodeUpdated(*existingNode)

	return existingNode, nil
}

// GetByID retrieves a node by ID if it belongs to the account
func (s *Service) GetByID(ctx context.Context, query node_get.NodeGetQuery) (*node.Node, error) {
	node, err := s.nodeRepo.GetByID(ctx, query.AuthContext.AccountID, query.ID)
	if err != nil {
		return nil, err
	}

	return node, nil
}

// GetAllByAccountID lists all nodes for the account
func (s *Service) GetAllByAccountID(ctx context.Context, query node_list.NodeListQuery) ([]node.Node, error) {
	nodes, err := s.nodeRepo.GetAllByAccountID(ctx, query.AuthContext.AccountID)
	if err != nil {
		return nil, err
	}

	return nodes, nil
}

// Delete deletes a node if it belongs to the account
func (s *Service) Delete(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID, force bool) error {
	// Get node before deletion for websocket event
	nodeToDelete, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}

	// A node whose device is ONLINE is refused without an explicit force.
	//
	// This is the one moment the platform can still reach the device, and
	// deleting it here is what strands it: the row goes, the agent keeps its
	// adoption file, and it retries into nothing forever. Refusing makes the
	// operator choose — shut the device down first, or force and let the platform
	// tell the agent to release its identity on the way out.
	//
	// Keyed on the connected STATES rather than on "has a live stream"
	// deliberately: a node in recovery netboot reports online too, and releasing
	// there is just as right. maintenance and restarting are connected too — a
	// node wedged mid-install sits there with a live stream — so they must gate
	// the delete as well, or the most common strand slips straight through.
	if nodeToDelete.IsInConnectedState() && !force {
		return app_errors.ErrNodeOnlineDeleteRequiresForce
	}

	// Tell the agent to forget it was adopted, BEFORE the row disappears — after
	// that the dispatch path has no node to authorize against.
	//
	// Best-effort on purpose, and the fallback is the tombstone written just
	// below: an agent that predates the command is refused by its version floor,
	// a device that never receives it stays stranded exactly as before, and in
	// both cases a restore still works. What must not happen is the reverse — the
	// delete failing because the device could not be told, leaving the operator
	// with neither the node nor a retry.
	releaseRequested := false
	if force && nodeToDelete.IsInConnectedState() {
		if err := s.requestIdentityRelease(ctx, nodeToDelete, actorUserID); err != nil {
			s.logger.WarnContext(ctx, "could not ask the node to release its adoption identity; the device will be stranded until someone restores the node or re-provisions it",
				"node_id", nodeID, "agent_version", nodeToDelete.AgentVersion, "error", err)
		} else {
			releaseRequested = true
		}
	}

	// The tombstone is written BEFORE the row is deleted, and its failure ABORTS
	// the delete. Both are deliberate, and both are the opposite of the
	// best-effort cleanups below.
	//
	// Everything after the delete is tidying up things that would otherwise be
	// orphaned; failing the request for those would leave the operator with
	// neither the node nor a retry. This is the reverse: it is the only record of
	// how to take the device back, and after the row is gone it cannot be
	// reconstructed from anything the platform holds. A delete that "worked"
	// while silently failing to record that is how a device ends up powered, on
	// the network and invisible.
	if s.tombstoneRepo != nil {
		tombstone := tombstoneFor(nodeToDelete, actorUserID)
		// Recorded so the restore offer stays honest: if the device took the
		// release, its adoption file is gone and restoring the row brings nothing
		// back. "Requested", not "confirmed" — the command's terminal status
		// arrives after the node row it belongs to is gone.
		tombstone.IdentityReleaseRequested = releaseRequested
		// The delete settles an in-flight install_os as failed, and the
		// failed-install rule applies: the disk may hold a partial image, so a
		// restore must bring the node back pointed at netboot, not at the boot
		// target it had before the install began. The row is gone by the time
		// the command is settled, so the rule is applied to the record here.
		if inFlight, ierr := s.OSInstallInFlight(ctx, nodeID); ierr != nil {
			s.logger.WarnContext(ctx, "could not tell whether an install was in flight on a deleted node; its boot target is recorded as it was",
				"node_id", nodeID, "error", ierr)
		} else if inFlight {
			netboot := false
			tombstone.BootToOS = &netboot
		}
		if err := s.tombstoneRepo.Create(ctx, tombstone); err != nil {
			return fmt.Errorf("recording the deleted node so its device can be restored: %w", err)
		}
	}

	err = s.nodeRepo.Delete(ctx, accountID, nodeID)
	if err != nil {
		return err
	}

	// Revoke any outstanding adoption codes for the gone node (best effort).
	if s.adoptionCodeRepo != nil {
		if err := s.adoptionCodeRepo.DeleteByNodeID(ctx, nodeID); err != nil {
			s.logger.Warn("failed to delete adoption codes for removed node", "node_id", nodeID, "error", err)
		}
	}

	// Tracked files and their version history go with the node. There is no FK
	// cascade on either table, so deleting a node used to leave its file
	// contents — arbitrary device files, now encrypted but still there — in the
	// database indefinitely, keyed to an id that could be reused.
	if s.trackedFileRepo != nil {
		if err := s.trackedFileRepo.DeleteByNodeID(ctx, nodeID); err != nil {
			s.logger.Warn("failed to delete tracked files for removed node", "node_id", nodeID, "error", err)
		}
	}
	if s.fileService != nil {
		if err := s.fileService.DeleteVersionsByNodeID(ctx, nodeID); err != nil {
			s.logger.Warn("failed to delete file versions for removed node", "node_id", nodeID, "error", err)
		}
	}

	// Favorites are per USER, so they are the one leftover no account-scoped
	// query can ever surface: the rows key on node_id alone and outlive the node
	// silently. That matters because node ids are reused — the replace flow
	// deliberately keeps the logical node across a hardware swap — so a stale
	// row does not stay inert, it re-attaches the old owner's star to whatever
	// device next answers to that id. Same cleanup class as the tracked files
	// above, and it was the only member of it with no caller at all.
	if s.favoriteRepo != nil {
		if err := s.favoriteRepo.DeleteByNode(ctx, nodeID); err != nil {
			s.logger.Warn("failed to delete favorites for removed node", "node_id", nodeID, "error", err)
		}
	}

	// Node-scoped secret attachments and access entries are the same class of
	// leftover, and the id reuse above is what makes them worse than clutter: a
	// tombstone restore deliberately brings the node back under its ORIGINAL id,
	// so a surviving row does not stay inert — it hands the device back the
	// credential material this delete was supposed to have taken away.
	//
	// The attachment row goes; an ordinary secret behind it does not, because it
	// is an account-level object that may be attached elsewhere. Attribute-backing
	// secrets (attr:<nodeID>:<key>) are the exception and go WITH the node: they
	// exist only as this node's attributes, their reserved name means nothing
	// else ever attaches them, and left behind they count against
	// SECRET_MAX_PER_ACCOUNT forever with no facet that surfaces them as this
	// node's leftovers once the node id resolves to nothing.
	if s.attachmentRepo != nil {
		if err := s.attachmentRepo.DeleteByNodeID(ctx, accountID, nodeID); err != nil {
			s.logger.WarnContext(ctx, "failed to delete secret attachments for removed node",
				"node_id", nodeID, "error", err)
		}
	}
	if s.secretRepo != nil {
		if deleted, err := s.secretRepo.DeleteNodeAttributes(ctx, accountID, nodeID); err != nil {
			s.logger.WarnContext(ctx, "failed to delete attribute secrets for removed node",
				"node_id", nodeID, "error", err)
		} else if deleted > 0 {
			s.logger.InfoContext(ctx, "deleted attribute secrets of removed node",
				"node_id", nodeID, "deleted", deleted)
		}
	}
	// The access entry is DETACHED, not deleted, mirroring the OU cascade: it
	// carries a password and a key set someone authored, so it degrades to
	// unassigned (applies to no node, still listed under scope=unassigned)
	// rather than being destroyed by a delete aimed at something else.
	if s.accessEntryRepo != nil {
		if err := s.accessEntryRepo.DetachNode(ctx, accountID, nodeID); err != nil {
			s.logger.WarnContext(ctx, "failed to detach access entries from removed node",
				"node_id", nodeID, "error", err)
		}
	}

	// AUDIT before the websocket event, from the pre-delete copy: once the row is
	// gone the id resolves to nothing, and the hostname and serial are what an
	// operator remembers a node by. This also takes its tracked files, its file
	// version history and its adoption codes with it, so it is the most
	// destructive single call in the API.
	//
	// Fingerprint is NULLABLE and routinely nil here — a WebUSB placeholder or a
	// node created but never connected has none, and the rollback paths in the
	// adoption flows delete exactly those. Dereferencing it would turn an audit
	// row into a panic on the most common delete there is.
	serial := ""
	if nodeToDelete.Fingerprint != nil {
		serial = nodeToDelete.Fingerprint.HardwareSerial
	}
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionNodeDelete, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("hostname=%q serial=%q status=%s",
			nodeToDelete.Hostname, serial, nodeToDelete.Status))

	// Workflow runs targeting this node would otherwise stay active FOREVER:
	// an execution node's only settler runs on reconnect, which a
	// deleted node will never do, so the execution keeps completed_at NULL, counts
	// against the per-account active cap and is never swept by retention — which
	// keys on completed_at. Twenty of those end playbooks for the account.
	//
	// Best-effort like the cleanups above, and for the same reason: the node row
	// is already gone, so failing the request here would leave the operator with
	// neither the node nor a retry. The operator-facing remedy if this does fail
	// is POST /workflow-executions/:id/cancel.
	if s.executionAbandoner != nil {
		if settled, err := s.executionAbandoner.AbandonNodeExecutions(ctx, accountID, nodeID); err != nil {
			s.logger.WarnContext(ctx, "failed to settle workflow runs for a removed node",
				"node_id", nodeID, "error", err)
		} else if settled > 0 {
			s.logger.InfoContext(ctx, "settled workflow runs for a removed node",
				"node_id", nodeID, "execution_nodes", settled)
		}
	}

	// The node's own outstanding commands go with it, AFTER the workflow runs
	// above have settled theirs. A queued command left behind is delivered if
	// the node is restored — under its original id, hours later — and an
	// in_progress one reads "in progress" for as long as the row exists. The
	// release_identity this delete just sent stays live: it is the one command
	// that is meant to outlive the row. Best-effort, like the cleanups above.
	if s.nodeCommandService != nil {
		if settled, err := s.nodeCommandService.SettleCommandsForDeletedNode(ctx, nodeID, node_command.SlugReleaseIdentity); err != nil {
			s.logger.WarnContext(ctx, "failed to settle outstanding commands of a removed node",
				"node_id", nodeID, "error", err)
		} else if settled > 0 {
			s.logger.InfoContext(ctx, "settled outstanding commands of a removed node",
				"node_id", nodeID, "commands", settled)
		}
	}

	// Utility ladder rows die with the node — best-effort like the cleanups
	// above; orphans are also swept with the account at purge.
	if s.utilityStateCleaner != nil {
		if err := s.utilityStateCleaner.CleanupNode(ctx, accountID, nodeID); err != nil {
			s.logger.WarnContext(ctx, "failed to drop utility state for a removed node",
				"node_id", nodeID, "error", err)
		}
	}

	// Emit websocket event
	s.notifyNodeDeleted(*nodeToDelete)

	return nil
}

// stripCredentialFields removes credential material from a command payload so it
// is never persisted, pushed over WebSocket, or returned by the node-command API.
// Credentials are re-resolved from the node's effective provisioning when the
// proto command is built.
func stripCredentialFields(payload map[string]interface{}) {
	delete(payload, "username")
	delete(payload, "password")
	delete(payload, "ssh_key")
}

// parseListParam normalizes a package/item list param that may arrive as a JSON
// array, or as a string with newline/comma/space separators (the workflow
// editor renders it as a textarea). Empty tokens are dropped.
func parseListParam(v interface{}) []string {
	var raw []string
	switch t := v.(type) {
	case []interface{}:
		for _, item := range t {
			if str, ok := item.(string); ok {
				raw = append(raw, str)
			}
		}
	case []string:
		raw = t
	case string:
		raw = strings.FieldsFunc(t, func(r rune) bool {
			return r == '\n' || r == '\r' || r == ',' || r == ' ' || r == '\t'
		})
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// A node registers over gRPC (`ConnectionInit`), never over HTTP. The
// `Register` use case that used to live here was reachable from nothing: its
// only caller was an unrouted handler, and it carried a serial-uniqueness guard
// added "so a future re-wiring cannot reopen a bypass" — a guard on a door with
// no building attached. Adoption is `Connect` plus the gRPC path; if an HTTP
// registration surface is ever wanted again it must be designed against the
// current adoption model, not resurrected from this.

// Connect handles node connection with fingerprint validation
func (s *Service) Connect(ctx context.Context, cmd node_connect.NodeConnectCommand) (*node.Node, error) {
	// Fetch node by ID and AccountID
	foundNode, err := s.nodeRepo.GetByIDAndAccount(ctx, cmd.NodeID, cmd.AccountID)
	if err != nil {
		return nil, err
	}

	// Validate unique token matches. An empty token never matches anything:
	// a stored token no configured key can decrypt reads back as "", and a
	// constant-time compare of two empty strings is equal, so without this
	// a lost key would let any caller presenting "" in as that node.
	if foundNode.UniqueTokenUnreadable || foundNode.UniqueToken == "" || cmd.UniqueToken == "" ||
		!services.SecureTokenEqual(foundNode.UniqueToken, cmd.UniqueToken) {
		return nil, app_errors.ErrNodeNotFound // Don't reveal that token is invalid
	}

	// Check if node already has a fingerprint. A WebUSB placeholder is stamped
	// with the SHORT serial and AwaitingFirstConnect; on its first real connect
	// the agent reports the FULL serial, so treat it like a first connection
	// (rebind the reported fingerprint) instead of failing the exact match.
	//
	// The connect of a row that is not yet admitted (an operator-created node,
	// a placeholder) is its ADMISSION, so that write runs under the device
	// cap; refused, the row is left exactly as it was and the device can be
	// admitted once a slot is free. A node restored admitted (it was admitted
	// when deleted) rebinds with a plain write; one restored non-admitted
	// takes the admission like any first connect.
	wasAdmitted := foundNode.IsAdmitted()
	if foundNode.Fingerprint == nil || foundNode.AwaitingFirstConnect {
		// Cross-account serial uniqueness — only on first bind; reconnects of
		// an already-fingerprinted node never re-run this lookup.
		if err := s.guardSerialBinding(ctx, cmd.AccountID, cmd.Fingerprint.HardwareSerial); err != nil {
			return nil, err
		}

		// First connection - set the fingerprint
		foundNode.MacAddress = cmd.Fingerprint.MacAddress
		foundNode.Fingerprint = &cmd.Fingerprint
		// Set to online when connection is established
		foundNode.Status = node.StatusOnline
		foundNode.AwaitingFirstConnect = false
		foundNode.UpdatedAt = time.Now()

		// A full-row save, and deliberately one of the few left.
		// This is the agent reporting its whole picture on connect — fingerprint,
		// MAC, status, agent version, boot state — so the row IS what this path
		// owns, and enumerating twenty columns would be a list to keep in sync
		// rather than a narrowing. The rule the rest of the service now follows is
		// for writers that own a handful of columns and read the row a moment
		// earlier; this one writes what it just built.
		if !wasAdmitted && foundNode.IsAdmitted() {
			err = s.admitTransition(ctx, foundNode, audit.SystemActor(cmd.AccountID))
		} else {
			err = s.nodeRepo.Update(ctx, foundNode)
		}
		if err != nil {
			return nil, err
		}

		// Emit websocket event
		s.notifyNodeUpdated(*foundNode)
	} else {
		// Node already has fingerprint - validate it matches
		if foundNode.Fingerprint.MacAddress != cmd.Fingerprint.MacAddress ||
			foundNode.Fingerprint.HardwareSerial != cmd.Fingerprint.HardwareSerial {
			return nil, app_errors.ErrNodeNotFound // Don't reveal fingerprint mismatch
		}
		// A bound, approved row still in a pre-connect status: the stream
		// this connect opens sets it online, which admits it. Take that
		// status under the cap here, before the stream does.
		if !wasAdmitted && foundNode.IsApproved() {
			foundNode.Status = node.StatusOnline
			foundNode.UpdatedAt = time.Now()
			if err := s.admitTransition(ctx, foundNode, audit.SystemActor(cmd.AccountID)); err != nil {
				return nil, err
			}
			s.notifyNodeUpdated(*foundNode)
		}
	}

	return foundNode, nil
}

// GetAdoptionInfo returns adoption file content for a node (base64-encoded JSON)
func (s *Service) GetAdoptionInfo(ctx context.Context, accountID, nodeID uuid.UUID, platformURL, grpcEndpoint string) (string, error) {
	// Fetch node to verify ownership
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return "", err
	}
	// A blob carrying an empty token is a credential that can never
	// authenticate; refuse with the cause rather than hand one out.
	if foundNode.UniqueTokenUnreadable || foundNode.UniqueToken == "" {
		return "", fmt.Errorf("this node's stored credential is empty or cannot be decrypted with any configured key (a SECRETS_ENCRYPTION_KEY problem) — restore the key, then retry")
	}

	// Create adoption config struct
	adoptionConfig := struct {
		NodeID       string `json:"node_id"`
		AccountID    string `json:"account_id"`
		UniqueToken  string `json:"unique_token"`
		PlatformURL  string `json:"platform_url"`
		GRPCEndpoint string `json:"grpc_endpoint"`
		CreatedAt    int64  `json:"created_at"`
	}{
		NodeID:       foundNode.ID.String(),
		AccountID:    foundNode.AccountID.String(),
		UniqueToken:  foundNode.UniqueToken,
		PlatformURL:  platformURL,
		GRPCEndpoint: grpcEndpoint,
		CreatedAt:    foundNode.CreatedAt.Unix(),
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(adoptionConfig)
	if err != nil {
		return "", fmt.Errorf("failed to marshal adoption config: %w", err)
	}

	// Encode to base64
	base64Data := base64.StdEncoding.EncodeToString(jsonData)

	return base64Data, nil
}

// CreateAdoptionCode mints the one-time exchange code behind the node's
// one-command install link (the link no longer carries the adoption blob —
// account id, topology, reusable credential — only this opaque handle).
// Re-minting revokes any previous code for the node.
func (s *Service) CreateAdoptionCode(ctx context.Context, accountID, nodeID uuid.UUID) (string, time.Time, error) {
	if s.adoptionCodeRepo == nil {
		return "", time.Time{}, fmt.Errorf("adoption codes are not configured")
	}
	// Ownership check; also fails for foreign/unknown nodes.
	if _, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err != nil {
		return "", time.Time{}, err
	}
	// Opportunistic sweep + one-active-code-per-node invariant.
	if err := s.adoptionCodeRepo.DeleteExpired(ctx); err != nil {
		s.logger.Warn("failed to sweep expired adoption codes", "error", err)
	}
	if err := s.adoptionCodeRepo.DeleteByNodeID(ctx, nodeID); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to revoke previous adoption codes: %w", err)
	}
	entity, code, err := adoption_code.New(accountID, nodeID)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := s.adoptionCodeRepo.Create(ctx, entity); err != nil {
		return "", time.Time{}, fmt.Errorf("failed to store adoption code: %w", err)
	}
	return code, entity.ExpiresAt, nil
}

// ExchangeAdoptionCode consumes a one-time code and returns the node's
// adoption blob. Every failure mode (unknown/expired/used/deleted node) maps
// to ErrAdoptionCodeInvalid — the endpoint is unauthenticated and must not
// leak which codes exist.
func (s *Service) ExchangeAdoptionCode(ctx context.Context, code, platformURL, grpcEndpoint string) (string, error) {
	if s.adoptionCodeRepo == nil {
		return "", app_errors.ErrAdoptionCodeInvalid
	}
	if !adoption_code.LooksLikeCode(code) {
		return "", app_errors.ErrAdoptionCodeInvalid
	}
	claimed, err := s.adoptionCodeRepo.ClaimByHash(ctx, adoption_code.Hash(code))
	if err != nil {
		// An invalid/expired/used code is the normal negative path (silent).
		// A raw DB error is masked as invalid to the caller (no oracle) but
		// must not vanish — the handler never sees it (fail-closed 404).
		if !errors.Is(err, app_errors.ErrAdoptionCodeInvalid) {
			s.logger.Error("adoption code claim failed", "error", err)
		}
		return "", app_errors.ErrAdoptionCodeInvalid
	}
	blob, err := s.GetAdoptionInfo(ctx, claimed.AccountID, claimed.NodeID, platformURL, grpcEndpoint)
	if err != nil {
		s.logger.Warn("adoption code claimed but blob mint failed", "node_id", claimed.NodeID, "error", err)
		return "", app_errors.ErrAdoptionCodeInvalid
	}
	s.logger.Info("adoption code exchanged", "node_id", claimed.NodeID, "account_id", claimed.AccountID)
	return blob, nil
}

// UpdateSystemInfo updates the system information (storage, network, hardware, OS) for a node
func (s *Service) UpdateSystemInfo(ctx context.Context, nodeID, accountID uuid.UUID, systemInfo *node.SystemInfo) error {
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}

	// Boot device of the PREVIOUS report, captured before the overwrite: the
	// recovery→managed-OS transition below is only observable here.
	// Whether the node WAS in the recovery bootstrap, by the same test the
	// release path below uses. Captured before the incoming report overwrites it.
	prevRecovery := foundNode.IsRecoveryBootstrap()

	// Power/thermal facts of the PREVIOUS report, for the same reason: the change
	// is only observable here.
	prevPower := foundNode.PowerFacts()

	// The columns this report writes, named as they are set below. Only
	// these reach the row: see the write at the end.
	columns := []string{"system_info"}

	// Update system info
	foundNode.SystemInfo = systemInfo

	// Stamp the boot moment from THIS report, while the uptime it is computed
	// from is fresh — a later read pairing a stale uptime with a current clock
	// would drift the answer forward.
	if systemInfo != nil && systemInfo.Hardware != nil {
		foundNode.LastBootAt = nextLastBootAt(foundNode.LastBootAt, systemInfo.Hardware.Boot, time.Now())
		columns = append(columns, "last_boot_at")
	}

	// Also update StorageDevices for backward compatibility
	if systemInfo != nil && systemInfo.StorageDevices != nil {
		foundNode.StorageDevices = systemInfo.StorageDevices
		columns = append(columns, "storage_devices")
	}

	// Agent-reported running EEPROM version wins for the top-level bootloader
	// fields (which the Overview and freshness badge read): the agent reports it
	// on every connect regardless of boot path, whereas the httpboot handler
	// only captures it during an actual netboot — and never behind NAT. Only
	// overwrite when the agent actually reported a value, so a node that cannot
	// read it (non-Pi hardware, or vcgencmd failing) doesn't blank a previously
	// captured value. Note the recovery bootstrap is NOT such a node: it ships
	// vcgencmd and reports the full 40-hex hash, so a netbooted node updates this
	// field like any other.
	if systemInfo != nil && systemInfo.Hardware != nil && systemInfo.Hardware.Boot != nil &&
		systemInfo.Hardware.Boot.BootloaderVersion != "" {
		// Store just the hash token: vcgencmd's version line carries a channel
		// suffix (" (release)") that would defeat the freshness comparison.
		foundNode.BootloaderVersion = node.NormalizeBootloaderVersion(systemInfo.Hardware.Boot.BootloaderVersion)
		// Set the timestamp from the SAME report so version and build date never
		// come from different sources; clear it when the agent didn't report one
		// rather than leaving a stale prior capture paired with a new version.
		if ts := systemInfo.Hardware.Boot.BootloaderTimestamp; ts > 0 {
			t := time.Unix(ts, 0)
			foundNode.BootloaderTimestamp = &t
		} else {
			foundNode.BootloaderTimestamp = nil
		}
		columns = append(columns, "bootloader_version", "bootloader_timestamp")
	}

	// Power/thermal throttling, logged only when the mask CHANGES. The sticky
	// half stays set until the node reboots, so logging it on every periodic
	// report would repeat one line for hours — and a signal that is always lit
	// stops being a signal. Under-voltage is worth a WARN because it explains
	// failures nothing else on the node can explain, and it silently corrupts
	// installs and captures rather than failing them cleanly.
	if newPower := foundNode.PowerFacts(); newPower != nil {
		if prevPower == nil || prevPower.ThrottledFlags != newPower.ThrottledFlags {
			switch {
			case newPower.ThrottledFlags != 0:
				s.logger.WarnContext(ctx, "node reports power/thermal throttling",
					"node_id", nodeID,
					"throttled_flags", fmt.Sprintf("0x%x", newPower.ThrottledFlags),
					"under_voltage_now", newPower.UnderVoltageNow,
					"under_voltage_since_boot", newPower.UnderVoltageSinceBoot,
					"thermal", newPower.HasThermalProblem())
			case prevPower != nil:
				// Only interesting as a transition: on a fresh node the first
				// clean reading is the normal case and says nothing.
				s.logger.InfoContext(ctx, "node power/thermal throttling cleared",
					"node_id", nodeID)
			}
		}
	}

	// Update Hostname and IPv4 directly from network info
	if systemInfo != nil && systemInfo.Network != nil {
		if systemInfo.Network.Hostname != "" {
			foundNode.Hostname = systemInfo.Network.Hostname
			columns = append(columns, "hostname")
		}
		if systemInfo.Network.IP != "" {
			foundNode.IPv4 = systemInfo.Network.IP
			columns = append(columns, "ipv4")
		}
	}

	// Complete a pending reboot-to-recovery: once the node reports it booted
	// over HTTP (the bootstrap), restore the boot target it had before so the
	// NEXT reboot brings the local OS back. Restoring only on an http report
	// keeps a premature system-info push (still disk-booted) from reverting
	// the override before the reboot actually happened.
	//
	// The pending flag and the previous value stay as read until the write
	// below, which completes the revert only on a row that still carries
	// that same pending revert.
	completeRecoveryRevert := false
	if foundNode.RecoveryRevertPending &&
		systemInfo != nil && systemInfo.Hardware != nil && systemInfo.Hardware.Boot != nil &&
		systemInfo.Hardware.Boot.CurrentBootDevice == "http" {
		switch foundNode.RecoveryPrevBootToOS {
		case "on":
			v := true
			foundNode.BootToOS = &v
		case "off":
			v := false
			foundNode.BootToOS = &v
		default:
			foundNode.BootToOS = nil
		}
		completeRecoveryRevert = true
	}

	// Boot-loop failsafe: a confirmed OS boot means the node is healthy again, so
	// clear the withheld-attempt counter and the failsafe flag. Only on an explicit
	// OS boot device (never empty/unknown/http), so a partial or recovery report
	// can't clear a real loop. Targeted reset (the full-row Save below omits these
	// columns), so it can't race a concurrent increment.
	// The recovery test is not redundant with the device switch below: an
	// SD-PROVISIONED bootstrap reports "sd", so without it a device sitting in
	// recovery on its provisioning card would clear a real boot loop — exactly
	// what the comment above says must not happen.
	if systemInfo != nil && systemInfo.Hardware != nil && systemInfo.Hardware.Boot != nil &&
		!node.BootInfoIsRecoveryBootstrap(systemInfo.Hardware.Boot) &&
		(foundNode.BootAttemptCount > 0 || foundNode.BootFailsafeAt != nil) {
		switch systemInfo.Hardware.Boot.CurrentBootDevice {
		case "nvme", "sd", "usb":
			if err := s.nodeRepo.ResetBootFailsafe(ctx, nodeID); err != nil {
				s.logger.WarnContext(ctx, "failed to reset boot-loop failsafe on OS boot",
					"node_id", nodeID, "error", err)
			} else {
				foundNode.BootAttemptCount = 0
				foundNode.BootFailsafeAt = nil
			}
		}
	}

	foundNode.UpdatedAt = time.Now()

	// Only the columns the report owns. A report arrives on every connect and
	// on demand, and a full-row save from the read above would put back
	// whatever an operator wrote in between — the SSH password policy, the
	// boot target, the OU, the status pin — silently.
	reverted, err := s.nodeRepo.UpdateSystemReport(ctx, foundNode, columns, completeRecoveryRevert)
	if err != nil {
		return fmt.Errorf("failed to update system info: %w", err)
	}
	if reverted {
		s.logger.InfoContext(ctx, "reboot-to-recovery completed; boot target restored",
			"node_id", nodeID, "restored_to", node.BoolPolicyString(foundNode.BootToOS))
	}

	// Emit websocket event from the row as it now stands, so the browser
	// shows the operator columns this report did not write rather than the
	// copy read before it. The copy is the fallback when the re-read fails.
	notified := foundNode
	if fresh, ferr := s.nodeRepo.GetByID(ctx, accountID, nodeID); ferr == nil && fresh != nil {
		notified = fresh
	}
	s.notifyNodeUpdated(*notified)

	// A system-info report is the only moment the node's CURRENT boot mode is
	// fresh (at connect time the stored value still describes the previous
	// boot). A non-recovery report releases queued commands that must run on a
	// managed OS; the recovery→OS transition additionally self-heals access
	// config whose last apply died or predates a reinstall.
	//
	// Not for a watcher node: its managed OS runs an agent that
	// executes nothing, so redelivering would strand commands "sent" and the
	// access self-heals would enqueue applies that can never land. Its access
	// path is the recovery-mode offline apply instead.
	if systemInfo != nil && systemInfo.Hardware != nil && systemInfo.Hardware.Boot != nil &&
		!node.BootInfoIsRecoveryBootstrap(systemInfo.Hardware.Boot) && !foundNode.IsWatcher() {
		if s.nodeCommandService != nil {
			if err := s.nodeCommandService.RedeliverManagedOSQueuedCommandsForNode(ctx, nodeID); err != nil {
				s.logger.Warn("failed to redeliver managed-OS queued commands", "node_id", nodeID, "error", err)
			}
		}
		if prevRecovery {
			s.reapplyAccessAfterRecovery(ctx, accountID, nodeID)
		}
		// And on EVERY managed-OS report, not just the recovery transition:
		// what the device confirmed applying can go stale for reasons no
		// command history shows — an access entry edited while the node was
		// offline long enough for the queued command to expire, a node restored
		// from a backup image taken before the change.
		s.reconcileAccessGeneration(ctx, accountID, nodeID, foundNode.AccessAppliedGeneration)
		// Utilities self-heal on the same signal and for the same reasons —
		// an assignment edited while the node was away, a node that joined an
		// OU that already had assignments, a reimaged node whose ladder
		// install_os dropped. Phase 2 of docs/UTILITIES-PLAN.md; the loop
		// decides for itself whether anything actually drifted, and it never
		// fails this update.
		if s.utilityReconciler != nil {
			s.utilityReconciler.ReconcileNode(ctx, accountID, nodeID)
		}
	}

	return nil
}

// reapplyAccessAfterRecovery re-enqueues apply_access_config after a node
// returns from the recovery bootstrap to a managed OS, when the last apply can
// no longer be trusted: it terminally failed/expired/was skipped (e.g. the
// queued command was delivered into the bootstrap before the recovery guard
// existed), or a successful install_os is NEWER than the last successful apply
// (the disk was rewritten, so the applied users are gone). A queued or
// in-flight apply is left alone — redelivery handles it. Best-effort: access
// self-healing must never fail a system-info update.
func (s *Service) reapplyAccessAfterRecovery(ctx context.Context, accountID, nodeID uuid.UUID) {
	if s.nodeCommandService == nil {
		return
	}
	// A bounded scan of recent history is enough: if the last apply is older
	// than this window, missing the re-apply degrades to today's behaviour (the
	// operator re-touches access).
	recent, err := s.nodeCommandService.GetNodeCommandsByNodeID(ctx, accountID, nodeID, 100, 0)
	if err != nil {
		s.logger.Warn("failed to inspect command history for access re-apply", "node_id", nodeID, "error", err)
		return
	}
	if !accessReapplyNeeded(recent) {
		return
	}
	s.logger.Info("re-applying access config after recovery", "node_id", nodeID)
	if err := s.EnqueueApplyAccessConfig(ctx, accountID, nodeID); err != nil {
		s.logger.Warn("failed to re-apply access config after recovery", "node_id", nodeID, "error", err)
	}
}

// accessReapplyNeeded decides, from a node's recent command history (newest
// first), whether the last access apply can no longer be trusted after a
// recovery→OS transition. True when the last apply terminally failed / expired
// / was skipped, or when a successful install_os postdates the last successful
// apply (the disk was rewritten). False when access was never applied, or an
// apply is queued/in flight (the redelivery path owns it).
func accessReapplyNeeded(recent []node_command.NodeCommand) bool {
	var lastApply *node_command.NodeCommand
	var lastInstallSuccess *node_command.NodeCommand
	for i := range recent {
		cmd := &recent[i]
		if lastApply == nil && cmd.Slug == node_command.SlugApplyAccessConfig {
			lastApply = cmd
		}
		if lastInstallSuccess == nil && cmd.Slug == node_command.SlugInstallOS && cmd.Status == node_command.StatusSuccess {
			lastInstallSuccess = cmd
		}
		if lastApply != nil && lastInstallSuccess != nil {
			break
		}
	}
	if lastApply == nil {
		// Access management never touched this node; nothing to heal.
		return false
	}
	switch lastApply.Status {
	case node_command.StatusRegistered, node_command.StatusSent, node_command.StatusInProgress:
		// Queued or in flight — the managed-OS redelivery path owns it.
		return false
	case node_command.StatusSuccess:
		// A reinstall postdating the apply means the on-disk users are gone.
		return lastInstallSuccess != nil && lastInstallSuccess.CreatedAt.After(lastApply.CreatedAt)
	}
	return true
}

// Boot-loop failsafe tuning.
const (
	// bootFailsafeWindow: consecutive attempts older than this start a fresh series
	// (an occasional reboot is not a loop; a real crash-loop reboots within it).
	bootFailsafeWindow = 30 * time.Minute
	// bootFailsafeDebounce: collapse a single boot's duplicate/retry requests.
	bootFailsafeDebounce = 45 * time.Second
	// bootFailsafeLiveness: suppress counting while the node was recently alive (a
	// real crash-loop keeps no agent connected). Also stops an attacker spoofing
	// boot.sig for a healthy ONLINE node from forcing it into recovery.
	bootFailsafeLiveness = 5 * time.Minute
)

// RegisterNetbootWithhold is called by the httpboot handler when it is about to
// WITHHOLD the bootstrap (boot_to_os) for a boot.sig request. It counts the
// withheld attempt and returns true when the boot-loop failsafe should instead
// SERVE recovery — i.e. the node keeps re-netbooting without ever booting its OS.
// globalThreshold is the config default; a per-node override wins; 0 disables.
func (s *Service) RegisterNetbootWithhold(ctx context.Context, n *node.Node, globalThreshold int) (bool, error) {
	threshold := n.EffectiveBootFailsafeThreshold(globalThreshold)
	if threshold <= 0 {
		return false, nil
	}
	// A live/recent agent means the node is not crash-looping (and blocks a spoofed
	// forced-recovery of a healthy node) — don't count.
	if n.LastHeartbeat != nil && time.Since(*n.LastHeartbeat) < bootFailsafeLiveness {
		return false, nil
	}
	count, _, err := s.nodeRepo.IncrementBootAttempt(ctx, n.ID, bootFailsafeWindow, bootFailsafeDebounce)
	if err != nil {
		return false, err
	}
	if count < threshold {
		return false, nil
	}
	// Threshold reached: serve recovery, flag it, and one-shot-reset the counter to 0
	// so the NEXT boot withholds again and gives the OS another chance.
	now := time.Now()
	if err := s.nodeRepo.SetBootFailsafe(ctx, n.ID, now, 0); err != nil {
		return false, err
	}
	n.BootFailsafeAt = &now
	n.BootAttemptCount = 0
	s.logger.WarnContext(ctx, "boot-loop failsafe triggered: node returned repeatedly without a successful OS boot; serving recovery",
		"node_id", n.ID, "name", n.Name, "count", count, "threshold", threshold)
	s.notifyNodeUpdated(*n)
	return true, nil
}

// ClearBootFailsafe clears the boot-loop failsafe counter and flag (operator
// acknowledge). Account-scoped.
func (s *Service) ClearBootFailsafe(ctx context.Context, accountID, nodeID uuid.UUID) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if err := s.nodeRepo.ResetBootFailsafe(ctx, nodeID); err != nil {
		return fmt.Errorf("failed to clear boot failsafe: %w", err)
	}
	n.BootAttemptCount = 0
	n.BootFailsafeAt = nil
	s.notifyNodeUpdated(*n)
	return nil
}

// RebootToRecovery performs a one-shot netboot: force the node's boot target
// to the network bootstrap, reboot, and restore the previous target once the
// node reconnects netbooted (see UpdateSystemInfo).
func (s *Service) RebootToRecovery(ctx context.Context, accountID, userID, nodeID uuid.UUID) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	// Refuse before touching anything. The pin below is undone only by the
	// device reporting a network boot, so a reboot that cannot be delivered,
	// or one that cannot end in a network boot, leaves an override nobody
	// asked for.
	if n.Status != node.StatusOnline && n.Status != node.StatusMaintenance {
		return &RecoveryRebootRefusedError{
			NodeID: nodeID,
			Code:   "node_unavailable",
			Reason: fmt.Sprintf("the node is %s; it must be online to receive the reboot", n.Status),
		}
	}
	// A node already running the recovery bootstrap over the network has
	// proven the path; otherwise its reported boot order must reach http.
	if n.CurrentBootDevice() != "http" {
		if ok, reason := node.BootOrderCanReturnToNetboot(n.ReportedBootOrder()); !ok {
			return &RecoveryRebootRefusedError{NodeID: nodeID, Code: "recovery_unreachable", Reason: reason}
		}
	}
	// A bootloader that fetches from ANOTHER install boots that install's
	// recovery, so this one never sees the network boot that completes the
	// one-shot. Checked here, not only in the HTTP route, so a playbook's
	// Reboot to recovery block is refused the same way. The other host is not
	// named: it is another deployment's identity.
	if n.BootHostMismatch(s.bootHost) {
		return &RecoveryRebootRefusedError{NodeID: nodeID, Code: "recovery_unreachable",
			Reason: "the node's bootloader fetches from a different boot server; update its bootloader for this install first"}
	}
	prevBootToOS := n.BootToOS
	prevRevertPending := n.RecoveryRevertPending
	prevSaved := n.RecoveryPrevBootToOS
	// Keep the originally saved target if a one-shot is already in flight
	// (re-triggering must not overwrite it with the forced value).
	if !n.RecoveryRevertPending {
		n.RecoveryRevertPending = true
		n.RecoveryPrevBootToOS = node.BoolPolicyString(n.BootToOS)
	}
	forced := false
	n.BootToOS = &forced
	// Named columns: this owns the boot override and the one-shot bookkeeping and
	// nothing else. A full-row save from a row read a moment ago carries back
	// whatever the gRPC layer wrote in between — the `restarting` pin above all,
	// which exists to survive exactly the reboot this is about to trigger.
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"boot_to_os":               n.BootToOS,
		"recovery_revert_pending":  n.RecoveryRevertPending,
		"recovery_prev_boot_to_os": n.RecoveryPrevBootToOS,
	}); err != nil {
		return fmt.Errorf("failed to set netboot target: %w", err)
	}
	s.notifyNodeUpdated(*n)

	sendErr := s.SendCommand(ctx, node_command_cmd.NodeCommandCommand{
		NodeID:      nodeID,
		Command:     "reboot",
		Params:      map[string]interface{}{"delay_seconds": 0},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if sendErr == nil {
		return nil
	}
	// The reboot was not dispatched, so nothing will ever complete the
	// one-shot: put back exactly what was there. A failure here is logged;
	// the dispatch error is what the caller needs.
	n.BootToOS = prevBootToOS
	n.RecoveryRevertPending = prevRevertPending
	n.RecoveryPrevBootToOS = prevSaved
	if rerr := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"boot_to_os":               prevBootToOS,
		"recovery_revert_pending":  prevRevertPending,
		"recovery_prev_boot_to_os": prevSaved,
	}); rerr != nil {
		s.logger.ErrorContext(ctx, "reboot-to-recovery was not dispatched and the boot target could not be restored",
			"node_id", nodeID, "error", rerr)
	} else {
		s.notifyNodeUpdated(*n)
	}
	return sendErr
}

// ForceNetbootAfterFailedInstall points a node back at netboot after an
// install_os failure, because the agent deliberately leaves the target
// unbootable when the image digest does not match (it never commits the image
// head). Booting that disk would drop the node into an initramfs prompt where
// nothing can reach it; netbooting hands it back to the platform.
//
// It must ALSO clear the reboot-to-recovery one-shot. That flow saves the
// previous boot target and restores it on the first system-info report showing
// an http boot — if that report lands after this failure, the restore would
// overwrite boot_to_os back to "on" and send the node at the disk we just
// invalidated. A failed install voids the "put back what you had" contract:
// what they had was a bootable disk.
func (s *Service) ForceNetbootAfterFailedInstall(ctx context.Context, accountID, nodeID uuid.UUID) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if err := s.forceNetboot(ctx, n); err != nil {
		return fmt.Errorf("failed to force netboot after a failed install: %w", err)
	}
	s.logger.WarnContext(ctx, "install failed; node forced back to netboot",
		"node_id", nodeID, "account_id", accountID)
	return nil
}

// ForceNetbootAfterDiskErase keeps a node on recovery after a successful
// erase_drive run from the recovery system. The erased disk may be the one
// the node boots, and nothing else would move it off the local OS: the
// reboot-to-recovery one-shot restores "on" as soon as the node reports from
// recovery, so the next reboot would loop on an empty disk, and with the boot
// failsafe off nothing serves it recovery again. Erasing a data disk errs the
// same way, towards recovery, which the operator leaves by setting the
// target. From a running OS the device refuses to erase the disk it runs
// from, so a node not in recovery keeps its target.
func (s *Service) ForceNetbootAfterDiskErase(ctx context.Context, accountID, nodeID uuid.UUID) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if !n.IsRecoveryBootstrap() {
		return nil
	}
	if err := s.forceNetboot(ctx, n); err != nil {
		return fmt.Errorf("failed to keep the node on recovery after an erase: %w", err)
	}
	s.logger.InfoContext(ctx, "disk erased from recovery; node kept on recovery",
		"node_id", nodeID, "account_id", accountID)
	return nil
}

// forceNetboot sets an explicit recovery boot target and voids the
// reboot-to-recovery one-shot, whose restore would otherwise put the old
// target back on the node's next report from recovery.
func (s *Service) forceNetboot(ctx context.Context, n *node.Node) error {
	accountID, nodeID := n.AccountID, n.ID
	forced := false
	n.BootToOS = &forced
	n.RecoveryRevertPending = false
	n.RecoveryPrevBootToOS = ""
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"boot_to_os":               n.BootToOS,
		"recovery_revert_pending":  false,
		"recovery_prev_boot_to_os": "",
	}); err != nil {
		return err
	}
	s.notifyNodeUpdated(*n)
	return nil
}

// OSInstallInFlight reports whether an install_os to this node has been
// registered and not yet settled. While it has, the disk being written is not
// a boot target: the HTTP-boot decision serves recovery whatever boot_to_os
// says, so a device reset mid-write comes back to the platform instead of
// booting a partial image. Device-exclusive commands never overlap, so the one
// active device command is the install when there is one.
func (s *Service) OSInstallInFlight(ctx context.Context, nodeID uuid.UUID) (bool, error) {
	if s.nodeCommandService == nil {
		return false, nil
	}
	active, err := s.nodeCommandService.FindActiveDeviceCommand(ctx, nodeID)
	if err != nil {
		return false, err
	}
	return active != nil && active.Slug == node_command.SlugInstallOS, nil
}

// ClearAutoProvisionEEPROMPending clears the one-shot network-first EEPROM flag
// (internal use) so a successful/deliberately-skipped auto-provision never
// retriggers on later system-info reports (which would cause a reflash loop).
func (s *Service) ClearAutoProvisionEEPROMPending(ctx context.Context, nodeID, accountID uuid.UUID) error {
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if !foundNode.AutoProvisionEEPROMPending {
		return nil
	}
	foundNode.AutoProvisionEEPROMPending = false
	// One flag. UpdateNodeColumns stamps updated_at itself, so the manual bump
	// this used to carry is gone with the full-row save.
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"auto_provision_eeprom_pending": false,
	}); err != nil {
		return fmt.Errorf("failed to clear auto-provision eeprom flag: %w", err)
	}
	s.notifyNodeUpdated(*foundNode)
	return nil
}

// guardSerialBinding enforces cross-account hardware-serial uniqueness at
// serial-bind time (the only tokenless serial lookup is the public netboot
// endpoint, so a foreign account claiming a device's serial could drive that
// device's boot policy). Rules: a full 16-hex non-degenerate serial is refused
// when another account holds it exactly; the agent's "unknown" sentinel and
// repeated-nibble serials are shared by design and pass; a match in the
// caller's OWN account always passes (re-adopt/reconnect must keep working even
// if a stale foreign duplicate exists — dual ownership is logged for operators
// instead of locking both tenants out).
//
// The LOW WORD is guarded too, in both directions, because the netboot lookup
// for an eight-hex path collects every row whose serial ends in it, across
// accounts: a full serial is refused when another account holds a full serial
// sharing its low word or a short stamp it ends in, and a short stamp when
// another account holds a full serial ending in it. Without that, any account
// could bind a full serial ending in another tenant's low word and put its row
// — and the readback its own agent reports — beside that tenant's device on
// the path its EEPROM asks for. Two short stamps do not collide: unrelated
// devices legitimately share a low word, and neither is an adopted device.
func (s *Service) guardSerialBinding(ctx context.Context, accountID uuid.UUID, rawSerial string) error {
	normalized := node.NormalizeSerial(rawSerial)
	if !node.IsUniqueFullSerial(normalized) {
		return s.guardLowWord(ctx, accountID, normalized)
	}
	owners, err := s.nodeRepo.ListByExactSerial(ctx, normalized)
	if err != nil {
		return fmt.Errorf("serial uniqueness lookup: %w", err)
	}
	var foreign *node.Node
	ownAccountHasIt := false
	for _, owner := range owners {
		if owner.AccountID == accountID {
			ownAccountHasIt = true
		} else if foreign == nil {
			foreign = owner
		}
	}
	if foreign == nil {
		if ownAccountHasIt {
			// A rebind of a serial this account already holds — a reconnect,
			// a re-adopt. It claims nothing new, and refusing it would lock an
			// adopted device out over a collision that predates it.
			return nil
		}
		return s.guardLowWord(ctx, accountID, normalized)
	}
	// The verdict itself lives in the domain, so a read path that wants to tell an
	// operator "this bind would be refused" cannot drift from the guard that does
	// the refusing. Behaviour here is unchanged: at this point the serial is
	// already known to be unique-full and foreign-held, so the predicate turns on
	// the own-account exemption alone.
	if !node.SerialConflictsAcrossAccounts(normalized, ownAccountHasIt, true) {
		s.logger.WarnContext(ctx, "hardware serial owned by multiple accounts; allowing own-account binding — de-dup required",
			"serial", normalized, "account_id", accountID, "other_account_id", foreign.AccountID, "other_node_id", foreign.ID)
		s.recordAnomaly(ctx, instance_anomaly.New(
			instance_anomaly.KindSerialDuplicated,
			instance_anomaly.SerialConflictKey(normalized, accountID, foreign.AccountID),
			instance_anomaly.SeverityWarning,
			"The same hardware serial is registered in two accounts at once. The binding was allowed because the requesting account is one of them, but one of the two node records is stale and should be removed.",
			map[string]interface{}{
				"hardware_serial":       normalized,
				"requesting_account_id": accountID.String(),
				"other_account_id":      foreign.AccountID.String(),
				"other_node_id":         foreign.ID.String(),
			},
			&accountID,
		))
		return nil
	}
	s.logger.WarnContext(ctx, "rejected serial binding: hardware serial registered to another account",
		"serial", normalized, "requesting_account_id", accountID,
		"owner_account_id", foreign.AccountID, "owner_node_id", foreign.ID)
	return s.refuseSerialBinding(ctx, accountID, normalized, foreign.AccountID, foreign.ID,
		"A device was refused adoption because its hardware serial is already registered to another account. If both accounts belong to the same owner this is a move: delete the node from the owning account and the device will adopt on its next retry.")
}

// guardLowWord is the low-word half of guardSerialBinding.
func (s *Service) guardLowWord(ctx context.Context, accountID uuid.UUID, normalized string) error {
	if !node.LowWordIsGuarded(normalized) {
		return nil
	}
	holder, err := s.nodeRepo.LowWordHolderInAnotherAccount(ctx, accountID, normalized)
	if err != nil {
		return fmt.Errorf("serial low-word lookup: %w", err)
	}
	if holder == nil {
		return nil
	}
	s.logger.WarnContext(ctx, "rejected serial binding: its low word collides with a serial registered to another account",
		"serial", normalized, "requesting_account_id", accountID,
		"owner_account_id", holder.AccountID, "owner_node_id", holder.NodeID)
	return s.refuseSerialBinding(ctx, accountID, normalized, holder.AccountID, holder.NodeID,
		"A device was refused adoption because the last eight characters of its hardware serial match a serial registered to another account, and a device that boots by those eight characters could not be told apart. If both accounts belong to the same owner, delete the node from the other account and the device will adopt on its next retry.")
}

// refuseSerialBinding records and announces a refused bind, and returns the
// typed refusal.
func (s *Service) refuseSerialBinding(ctx context.Context, accountID uuid.UUID, normalized string, ownerAccountID, ownerNodeID uuid.UUID, message string) error {
	// Record it for the operator. A refusal is correct — it is what stops a
	// serial being claimed out of someone else's account — but it is also
	// indistinguishable, from the user's side, from the product being broken:
	// the device just retries forever and nothing appears anywhere. Without this
	// record the only trace would be a log line an operator rarely sees.
	s.recordAnomaly(ctx, instance_anomaly.New(
		instance_anomaly.KindSerialBindingConflict,
		instance_anomaly.SerialConflictKey(normalized, accountID, ownerAccountID),
		instance_anomaly.SeverityWarning,
		message,
		map[string]interface{}{
			"hardware_serial":       normalized,
			"requesting_account_id": accountID.String(),
			"owner_account_id":      ownerAccountID.String(),
			"owner_node_id":         ownerNodeID.String(),
		},
		&accountID,
	))

	// Tell the tenant who is standing next to the device why nothing happened.
	//
	// The gRPC error deliberately stays vague (it crosses a tenant boundary and
	// must not describe another account's state), and the device just retries —
	// so without this the whole event is invisible to the person waiting on the
	// Add Node screen. The serial is safe to name: they are holding the
	// hardware. The OWNING account is not named, for the same reason the gRPC
	// error is not specific.
	s.notifyAdoptionRefused(accountID, normalized)
	return app_errors.ErrHardwareSerialConflict
}

// notifyAdoptionRefused pushes an account-wide notice about a refused device.
//
// Account-wide rather than OU-scoped on purpose: the device belongs to no OU
// yet — that is the whole problem — so there is no unit to scope it to. It is
// gated on node.create because `GET /nodes/adoption-refusals` is: the notice
// carries a device serial, and whoever may start an adoption may learn why one
// is not finishing — nobody else.
func (s *Service) notifyAdoptionRefused(accountID uuid.UUID, serial string) {
	if s.websocketClient == nil {
		return
	}
	err := s.websocketClient.Send(websockets3.NewAccountMessage(accountID, authz.ActionNodeCreate, websockets3.MessageWrapper{
		Type: "node_adoption_refused",
		Payload: map[string]string{
			"hardware_serial": serial,
			"reason":          "serial_registered_to_another_account",
		},
	}))
	if err != nil && s.logger != nil {
		s.logger.Warn("failed to push adoption-refused notice", "error", err)
	}
}

// recordAnomaly persists an operator-facing signal. Best-effort by design: an
// anomaly is a report ABOUT a decision, never part of making it, so a failure
// to record must not change what the caller returns.
func (s *Service) recordAnomaly(ctx context.Context, anomaly *instance_anomaly.Anomaly) {
	if s.anomalyRepo == nil {
		return
	}
	if err := s.anomalyRepo.Record(ctx, anomaly); err != nil {
		s.logger.WarnContext(ctx, "failed to record instance anomaly", "error", err, "kind", anomaly.Kind)
	}
}

// ResolveSerialForBake settles both questions a per-serial EEPROM bake asks, in
// one call so neither can be asked without the other: may this account bind
// this serial at all (the cross-account guard), and which of its own nodes
// already carries it. A bake writes a package carrying this account's adoption
// token into a path keyed on the serial alone, so the guard has to run for the
// serial the caller ASKED for, whichever node the lookup then lands on.
//
// A FULL serial resolves only to a node carrying it exactly. GetByHardwareSerial
// would also match this account's SHORT stamp for some other device as the
// suffix of the requested serial — that arm answers "which node is this device"
// for an agent or SSH read, and answering a bake with it is how a caller reached
// the existing-node branch for a serial it does not own. A short serial keeps
// the reconcile arm: a WebUSB harvest only ever sees the low word.
//
// Returns app_errors.ErrHardwareSerialConflict when the serial belongs to
// another account, app_errors.ErrNodeNotFound when this account has no node
// carrying it (the caller creates one).
func (s *Service) ResolveSerialForBake(ctx context.Context, accountID uuid.UUID, rawSerial string) (*node.Node, error) {
	normalized := node.NormalizeSerial(rawSerial)
	if err := s.guardSerialBinding(ctx, accountID, normalized); err != nil {
		return nil, err
	}
	if len(normalized) < node.FullSerialLen {
		// A short serial is the low word of some device's full serial. When a
		// device adopted in ANOTHER account carries a full serial ending in
		// it, that device is almost certainly the one being flashed; the guard
		// above has already refused that (its low-word half), so what is left
		// is this account's own reconcile.
		return s.nodeRepo.GetByHardwareSerial(ctx, accountID, normalized)
	}
	owners, err := s.nodeRepo.ListByExactSerial(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("exact serial lookup: %w", err)
	}
	for _, owner := range owners {
		if owner.AccountID == accountID {
			return owner, nil
		}
	}
	return nil, app_errors.ErrNodeNotFound
}

// StampWebUSBPlaceholder marks a freshly-created WebUSB-flashed node: it stamps
// the short (low-word) serial so the node is findable for the HTTP-boot serve
// decision and reconcilable when the device later connects with its full serial,
// and flags it as awaiting its first connect (drives the distinct UI). The
// short-serial fingerprint is provisional — the real full fingerprint is bound
// on first connect (see Connect / ConnectWithMasterToken).
func (s *Service) StampWebUSBPlaceholder(ctx context.Context, nodeID, accountID uuid.UUID, shortSerial string) error {
	// The stamped serial may be FULL 16-hex (SSH adopt always, WebUSB when the
	// operator pasted a full serial) — guard those; short low words pass
	// through inside the guard.
	if err := s.guardSerialBinding(ctx, accountID, shortSerial); err != nil {
		return err
	}
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if foundNode.Fingerprint == nil {
		foundNode.Fingerprint = &node.HardwareFingerprint{HardwareSerial: shortSerial}
	}
	foundNode.AwaitingFirstConnect = true
	foundNode.UpdatedAt = time.Now()
	// Still a full-row save, deliberately: `fingerprint` is a jsonb column the
	// MODEL serializes, and writing it through UpdateNodeColumns would mean
	// marshalling it here — a second copy of a conversion that already exists,
	// which is a worse bug than the write width. Same for boot_order below. The
	// narrow-write rule applies to scalar columns; a jsonb column
	// wants a repository method of its own if this ever matters.
	if err := s.nodeRepo.Update(ctx, foundNode); err != nil {
		return fmt.Errorf("failed to stamp webusb placeholder: %w", err)
	}
	s.notifyNodeUpdated(*foundNode)
	return nil
}

// CommitWebUSBFlash finalizes a WebUSB flash: a node created in the "flashing"
// state (EEPROM write in progress) is revealed as ready_for_adoption once the
// browser confirms the flash sequence completed. Idempotent — a node that has
// already advanced past flashing (e.g. the device connected first, flipping it
// online) is left untouched, so a late or duplicate commit never regresses it.
func (s *Service) CommitWebUSBFlash(ctx context.Context, nodeID, accountID uuid.UUID) error {
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if foundNode.Status != node.StatusFlashing {
		return nil
	}
	// A narrow write, conditional on the stored status IN the statement. This
	// runs from a browser callback while the device may already be
	// connecting: the read above can be stale by the time the write lands,
	// and an unconditional write would move a device admitted in between
	// back to ready_for_adoption — out of the device cap's count while it
	// stays connected, freeing its slot for another device. Not committed
	// means the device got there first, which is the idempotent no-op, not
	// an error: the browser's flash succeeded. The flash stamp is what later
	// proves the EEPROM carries THIS node's identity (the tombstone's
	// EEPROMBakeConfirmed); a browser flash is as much a bake as an
	// update_boot_order.
	flashedAt := time.Now()
	committed, err := s.nodeRepo.CommitWebUSBFlash(ctx, accountID, nodeID, flashedAt)
	if err != nil {
		return fmt.Errorf("failed to commit webusb flash: %w", err)
	}
	if !committed {
		return nil
	}
	foundNode.Status = node.StatusReadyForAdoption
	foundNode.BootloaderFlashedAt = &flashedAt
	s.notifyNodeUpdated(*foundNode)
	return nil
}

// MarkWebUSBReflashing puts a WebUSB placeholder back into flashing for a
// re-flash, so the list shows "preparing" until the new flash commits.
//
// The write is conditional in SQL on the row still awaiting its first
// connect. The handler decides to re-mark from a row it read a moment
// earlier, and the device's first connect can land in between: an
// unconditional write then moved a connected, admitted device to flashing —
// out of the device cap's count while its stream stayed up, and the later
// commit turned it to ready_for_adoption. Not marked means the device got
// there first, which is a typed 409, not a silent success: the operator is
// flashing an EEPROM whose identity a running device just presented.
func (s *Service) MarkWebUSBReflashing(ctx context.Context, nodeID, accountID uuid.UUID) error {
	marked, err := s.nodeRepo.MarkWebUSBReflashing(ctx, accountID, nodeID)
	if err != nil {
		return fmt.Errorf("re-marking the node flashing: %w", err)
	}
	if marked {
		if n, gerr := s.nodeRepo.GetByID(ctx, accountID, nodeID); gerr == nil {
			s.notifyNodeUpdated(*n)
		}
		return nil
	}
	if _, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err != nil {
		return err
	}
	return &WebUSBDeviceConnectedError{NodeID: nodeID}
}

// DefaultBootToOSOn sets the node's boot_to_os override to "on" only when it is
// still unset (inherit). Called after an EEPROM provision so the apply-reboot
// lands on the installed OS instead of netbooting into recovery every boot
// (effective netboot-first would otherwise loop a freshly-provisioned node). An
// explicit on/off override is never touched.
//
// The default needs an OS to point at. A node reporting from the recovery
// system has one only if the most recent command that wrote its disk was a
// successful install_os — not merely any install on record: an erase_drive
// after it leaves the disk empty again. Without that, "on" withholds recovery
// from a node whose disk is empty or holds a system this platform never
// installed, and with the boot failsafe off nothing serves it recovery again.
// Such a node keeps inheriting, so its next boot is recovery; an install
// record pruned by retention errs the same way, which is the direction an
// operator can always recover from.
func (s *Service) DefaultBootToOSOn(ctx context.Context, nodeID, accountID uuid.UUID) error {
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if foundNode.BootToOS != nil {
		return nil // explicit choice already made; respect it
	}
	if foundNode.IsRecoveryBootstrap() {
		installed := false
		if s.nodeCommandService != nil {
			latest, lerr := s.nodeCommandService.LatestSucceededCommand(ctx, accountID, nodeID,
				[]string{node_command.SlugInstallOS, node_command.SlugEraseDrive})
			if lerr != nil {
				return fmt.Errorf("could not check for an installed OS; boot target left to inherit: %w", lerr)
			}
			installed = latest == node_command.SlugInstallOS
		}
		if !installed {
			s.logger.InfoContext(ctx, "boot target left to inherit: node runs the recovery system and no OS install is on record since its disk was last erased",
				"node_id", nodeID, "account_id", accountID)
			return nil
		}
	}
	on := true
	foundNode.BootToOS = &on
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"boot_to_os": &on,
	}); err != nil {
		return fmt.Errorf("failed to default boot_to_os on: %w", err)
	}
	s.notifyNodeUpdated(*foundNode)
	return nil
}

// UpdateBootOrderFromAgent updates the boot order for a node from agent report (internal use)
func (s *Service) UpdateBootOrderFromAgent(ctx context.Context, nodeID, accountID uuid.UUID, bootOrder []string) error {
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}

	// Update boot order (store as array directly)
	foundNode.BootOrder = bootOrder
	foundNode.UpdatedAt = time.Now()

	err = s.nodeRepo.Update(ctx, foundNode)
	if err != nil {
		return fmt.Errorf("failed to update boot order: %w", err)
	}

	// Emit websocket event
	s.notifyNodeUpdated(*foundNode)

	return nil
}

// SendCommand sends a command to a node after verifying ownership, status, and connection
// This method now uses SendBatchCommand internally for code reuse
func (s *Service) SendCommand(ctx context.Context, cmd node_command_cmd.NodeCommandCommand) error {
	// Convert single command to batch command format
	batchCmd := node_command_cmd.BatchNodeCommandCommand{
		NodeIDs:     []uuid.UUID{cmd.NodeID},
		Command:     cmd.Command,
		Params:      cmd.Params,
		AuthContext: cmd.AuthContext,
	}

	// Call batch command method
	batchResult, err := s.SendBatchCommand(ctx, batchCmd)
	if err != nil {
		return err
	}

	// Check if the single node command succeeded
	if len(batchResult.Results) == 0 {
		return fmt.Errorf("no result returned for node command")
	}

	result := batchResult.Results[0]
	if !result.Success {
		// Return the original typed error when the batch path preserved it, so the
		// handler's errors.As mapping (CommandPayloadTooLargeError -> 400,
		// AgentTooOldError, ...) still works; fall back to the flattened string.
		if result.Err != nil {
			return result.Err
		}
		return fmt.Errorf("%s", result.Error)
	}

	return nil
}

// ScanOSUsers dispatches the fixed OS-user inventory script to a node and
// returns the command id. The result is fetched asynchronously via
// GetOSUserScanResult — there is no synchronous send-and-wait in the command
// path (dispatch returns an id; a gRPC StatusUpdate drives completion), so a
// blocking in-handler poll is avoided. Online-only; run_script has no agent
// version floor.
func (s *Service) ScanOSUsers(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, error) {
	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs: []uuid.UUID{nodeID},
		Command: node_command.SlugRunScript,
		Params: map[string]interface{}{
			"script":          node.OSUserScanScript,
			"timeout_seconds": float64(15),
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return uuid.Nil, err
	}
	item := batch.Results[0]
	if !item.Success {
		return uuid.Nil, item.FailureError()
	}
	if item.CommandID == nil {
		return uuid.Nil, fmt.Errorf("the node is offline, so the OS user scan cannot run now; bring it online and try again")
	}
	return *item.CommandID, nil
}

// GetOSUserScanResult reads a dispatched scan command and, once the agent has
// reported, parses its stdout into the login-account inventory. The command is
// verified to belong to the node and to be the scan script, so an arbitrary
// command id cannot be read through this endpoint.
func (s *Service) GetOSUserScanResult(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (*node.OSUserScanResult, error) {
	if s.nodeCommandService == nil {
		return nil, fmt.Errorf("node command service not available")
	}
	cmd, err := s.nodeCommandService.GetNodeCommandByID(ctx, accountID, commandID)
	if err != nil {
		return nil, err
	}
	// Bind the poll to a command THIS endpoint dispatched: same node, and the
	// exact fixed scan script. Without the script check a node.credentials user
	// (who lacks secret.read_value) could pass the id of any prior run_script on
	// the node and read its decrypted stdout, bypassing workflow-output redaction.
	if cmd.NodeID != nodeID || cmd.Slug != node_command.SlugRunScript ||
		scriptOf(cmd) != node.OSUserScanScript {
		return nil, app_errors.ErrNodeNotFound
	}
	switch cmd.Status {
	case node_command.StatusSuccess:
		return &node.OSUserScanResult{
			Status: "completed",
			Users:  node.ParseOSUserInventory(cmd.Result["stdout"]),
		}, nil
	case node_command.StatusFailed, node_command.StatusExpired, node_command.StatusSkipped, node_command.StatusSuperseded:
		detail := cmd.Details
		if detail == "" {
			detail = "the scan did not complete"
		}
		return &node.OSUserScanResult{Status: "failed", Detail: detail}, nil
	default:
		return &node.OSUserScanResult{Status: "running"}, nil
	}
}

// scriptOf returns the run_script payload's script string (empty if absent), so
// a scan poll can verify the command it reads is the fixed scan it dispatched
// and not some other run_script on the same node.
func scriptOf(cmd *node_command.NodeCommand) string {
	if cmd == nil || cmd.Payload == nil {
		return ""
	}
	s, _ := cmd.Payload["script"].(string)
	return s
}

// ScanEEPROMConfig dispatches the fixed rpi-eeprom-config read to a node and
// returns the command id, polled via GetEEPROMConfigScanResult. Surfaces the
// ACTUALLY-flashed EEPROM config (BOOT_ORDER, NET_INSTALL_*, HTTP_HOST/PATH),
// rather than values inferred from node fields. Online-only; RPi only in
// practice (a non-Pi returns a failed command, reported as such).
func (s *Service) ScanEEPROMConfig(ctx context.Context, accountID, userID, nodeID uuid.UUID) (uuid.UUID, error) {
	batch, err := s.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs: []uuid.UUID{nodeID},
		Command: node_command.SlugRunScript,
		Params: map[string]interface{}{
			"script":          node.EEPROMConfigScanScript,
			"timeout_seconds": float64(15),
		},
		AuthContext: commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return uuid.Nil, err
	}
	item := batch.Results[0]
	if !item.Success {
		return uuid.Nil, item.FailureError()
	}
	if item.CommandID == nil {
		return uuid.Nil, fmt.Errorf("the node is offline, so the EEPROM config scan cannot run now; bring it online and try again")
	}
	return *item.CommandID, nil
}

// GetEEPROMConfigScanResult reads a dispatched EEPROM-config scan and parses its
// output server-side. The command is verified to belong to the node and be a
// run_script, so an arbitrary command id cannot be read through this endpoint.
func (s *Service) GetEEPROMConfigScanResult(ctx context.Context, accountID, nodeID, commandID uuid.UUID) (*node.EEPROMConfigScanResult, error) {
	if s.nodeCommandService == nil {
		return nil, fmt.Errorf("node command service not available")
	}
	cmd, err := s.nodeCommandService.GetNodeCommandByID(ctx, accountID, commandID)
	if err != nil {
		return nil, err
	}
	if cmd.NodeID != nodeID || cmd.Slug != node_command.SlugRunScript ||
		scriptOf(cmd) != node.EEPROMConfigScanScript {
		return nil, app_errors.ErrNodeNotFound
	}
	switch cmd.Status {
	case node_command.StatusSuccess:
		return &node.EEPROMConfigScanResult{
			Status:  "completed",
			Entries: node.ParseEEPROMConfig(cmd.Result["stdout"]),
		}, nil
	case node_command.StatusFailed, node_command.StatusExpired, node_command.StatusSkipped, node_command.StatusSuperseded:
		detail := cmd.Details
		if detail == "" {
			detail = "the scan did not complete"
		}
		return &node.EEPROMConfigScanResult{Status: "failed", Detail: detail}, nil
	default:
		return &node.EEPROMConfigScanResult{Status: "running"}, nil
	}
}

// internalAuth carries just the account id for platform-initiated commands
// (e.g. auto-applying access config after an access mutation) that have no HTTP
// caller. sendCommandToNode only needs GetAccountID from its auth context.
type internalAuth struct{ accountID uuid.UUID }

func (a internalAuth) GetAccountID() uuid.UUID { return a.accountID }

// EnqueueApplyAccessConfig dispatches an apply_access_config for a node so an
// access change actually reaches the device. apply_access_config is queueable,
// so an offline node gets it on reconnect and a re-dispatch supersedes the prior
// one; a node whose agent is too old is skipped, not failed, and so is a node
// still awaiting approval: it takes no commands yet, and its approval applies
// its access (ApproveNode). All of those are non-fatal to the caller — an
// access edit succeeds regardless of any single node's reachability.
func (s *Service) EnqueueApplyAccessConfig(ctx context.Context, accountID, nodeID uuid.UUID) error {
	_, _, err := s.sendCommandToNode(ctx, nodeID, node_command.SlugApplyAccessConfig, nil, internalAuth{accountID: accountID}, nil, nil, "")
	if err != nil {
		var tooOld *AgentTooOldError
		if errors.As(err, &tooOld) {
			// Expected until every node runs an agent that understands the
			// command; not an error worth propagating.
			return nil
		}
		var unavailable *NodeUnavailableError
		if errors.As(err, &unavailable) && unavailable.NotApproved {
			s.logger.DebugContext(ctx, "access apply skipped: the node awaits approval and gets its access when approved",
				"node_id", nodeID)
			return nil
		}
		return err
	}
	return nil
}

// convergeOnApproval sends a just-approved node what it was refused while it
// waited: its access configuration and its utilities. Both normally converge
// from the node's managed-OS system-info report, but a pending node's report
// had every dispatch refused for want of approval, and nothing reports again
// until the agent reconnects — so without this an approved node ran without
// the access an operator had given it. The same reconcilers decide whether
// anything is due, so a node with nothing to apply gets nothing, and an apply
// already queued is left alone. Best-effort, like the system-info path: it
// never fails the approval.
func (s *Service) convergeOnApproval(ctx context.Context, accountID uuid.UUID, n *node.Node) {
	if n == nil || !n.IsAdmitted() || n.IsWatcher() || n.IsRecoveryBootstrap() {
		return
	}
	s.reconcileAccessGeneration(ctx, accountID, n.ID, n.AccessAppliedGeneration)
	if s.utilityReconciler != nil {
		s.utilityReconciler.ReconcileNode(ctx, accountID, n.ID)
	}
}

// ReapplyAccessConfig re-dispatches apply_access_config for one node on an
// explicit operator request. Unlike EnqueueApplyAccessConfig it surfaces
// errors (agent too old, node not approved) instead of swallowing them, so the
// UI can tell the operator why nothing will happen. queued=true means the node
// is offline or in recovery and the command waits for a managed-OS boot.
func (s *Service) ReapplyAccessConfig(ctx context.Context, accountID, nodeID uuid.UUID) (bool, error) {
	_, queued, err := s.sendCommandToNode(ctx, nodeID, node_command.SlugApplyAccessConfig, nil, internalAuth{accountID: accountID}, nil, nil, "")
	return queued, err
}

// ApplyAccessConfigOffline dispatches apply_access_config_offline: the node,
// netbooted into recovery, mounts the target disk and writes the resolved
// access config into the installed rootfs — how OU-assigned credentials
// reach a node whose live-OS agent never runs. The netboot
// precondition is enforced in the registration switch; here the target disk is
// resolved when the caller did not name one.
//
// The auto-pick refuses ambiguity rather than guessing: this command writes
// users onto a disk, and "the wrong disk" is not a smaller version of the
// intent. Exactly one reported storage device → use it; zero or several →
// name them and make the operator choose.
func (s *Service) ApplyAccessConfigOffline(ctx context.Context, accountID, nodeID uuid.UUID, targetDevice string, rootPartition int) error {
	targetDevice = strings.TrimSpace(targetDevice)
	if targetDevice == "" {
		n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
		if err != nil {
			return err
		}
		resolved, rerr := soleReportedDisk(n)
		if rerr != nil {
			return rerr
		}
		targetDevice = resolved
	}
	params := map[string]interface{}{
		"target_device":  targetDevice,
		"root_partition": float64(rootPartition),
	}
	_, _, err := s.sendCommandToNode(ctx, nodeID, node_command.SlugApplyAccessConfigOffline, params, internalAuth{accountID: accountID}, nil, nil, "")
	return err
}

// soleReportedDisk names the node's only disk, and refuses to choose when
// there is more than one.
//
// Ambiguity is REFUSED rather than resolved by a rule, because every rule that
// picks for you ("the biggest", "the first") is wrong on the node where it
// matters — and both callers are about to write to whatever it returns. The
// error names the candidates so the operator can pass one.
func soleReportedDisk(n *node.Node) (string, error) {
	if n == nil || n.SystemInfo == nil || len(n.SystemInfo.StorageDevices) == 0 {
		return "", fmt.Errorf("the node has not reported any storage devices; specify target_device explicitly")
	}
	if len(n.SystemInfo.StorageDevices) > 1 {
		names := make([]string, 0, len(n.SystemInfo.StorageDevices))
		for name := range n.SystemInfo.StorageDevices {
			names = append(names, name)
		}
		slices.Sort(names)
		return "", fmt.Errorf("the node reports %d storage devices (%s); specify target_device explicitly",
			len(names), strings.Join(names, ", "))
	}
	for name := range n.SystemInfo.StorageDevices {
		return name, nil
	}
	return "", fmt.Errorf("the node has not reported any storage devices; specify target_device explicitly")
}

// clampDelaySeconds bounds a reboot/shutdown delay at dispatch, the choke point
// every caller (HTTP, workflow engine, scheduler) passes through — the agent
// sleeps the value on its single command goroutine, and the proto field is a
// uint32 a negative float64 would wrap through. Clamped here rather than at
// save time so playbooks saved before the bound existed are covered too, and
// logged when it binds so the first caller to meet the limit is visible rather
// than silently slowed down (the http_request timeout clamp's shape).
func (s *Service) clampDelaySeconds(ctx context.Context, nodeID uuid.UUID, slug string, delay float64) float64 {
	clamped, bound := node_command.ClampDelaySeconds(delay)
	if bound {
		s.logger.WarnContext(ctx, "clamping a command delay to its bounds",
			"node_id", nodeID, "slug", slug,
			"requested_seconds", delay, "clamped_seconds", clamped,
			"ceiling_seconds", node_command.MaxDelaySeconds)
	}
	return clamped
}

// sendCommandToNode is a helper function that processes a command for a single node
// It validates the node, builds the command payload, registers it, and queues it for sending.
// The returned bool reports that the command was accepted but NOT dispatched: the
// node is offline and the command waits for reconnect. Callers must surface that
// distinction rather than reporting it as applied.
func (s *Service) sendCommandToNode(ctx context.Context, nodeID uuid.UUID, command string, params map[string]interface{}, authContext interface{}, workflowExecutionNodeID *uuid.UUID, secretDerivedParams []string, workflowBlockID string) (*node_command.NodeCommand, bool, error) {
	// Extract accountID from authContext
	var accountID uuid.UUID
	if authCtx, ok := authContext.(interface{ GetAccountID() uuid.UUID }); ok {
		accountID = authCtx.GetAccountID()
	} else {
		return nil, false, fmt.Errorf("invalid auth context: missing account ID")
	}

	// 1. Verify node exists and belongs to account
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, false, err
	}

	// 1a. A watcher node executes NO commands. Refused before
	// every other check — "this node is watcher-only" is the strongest
	// precondition there is, and this is the single choke point the HTTP
	// handlers, the workflow engine, the scheduler and the platform's own
	// self-heals all pass through. The agent-side half of the guarantee is
	// structural: the watcher binary carries no command-execution code at all.
	//
	// The one exception is the recovery-mode access apply: in recovery the
	// machine runs the FULL recovery agent, not the watcher that reported the
	// kind — refusing it would cut off exactly the management path watcher
	// nodes are supposed to use.
	resolvedSlug := node_command.ResolveSlug(command)
	// A known parameter of the wrong shape is refused before any check below
	// reads it and before anything is written; the payload builder would
	// otherwise drop it and run the command without it.
	if err := validateCommandParamKinds(resolvedSlug, params); err != nil {
		return nil, false, err
	}
	if foundNode.IsWatcher() && resolvedSlug != node_command.SlugApplyAccessConfigOffline {
		return nil, false, &WatcherNodeError{NodeID: nodeID, Slug: resolvedSlug}
	}
	// A missing required value is the operator's to fix, so it is refused
	// before any refusal about the node's state (agent version, approval,
	// boot mode, connectivity) answers instead of it — and before anything is
	// written. Only the watcher gate, which refuses every command whatever its
	// params, comes first.
	if err := validateRequiredCommandParams(resolvedSlug, params); err != nil {
		return nil, false, err
	}
	// The same holds for a value that is wrong in itself — a refused path, an
	// unknown action, a malformed checksum or headers value.
	if err := validateCommandPayload(resolvedSlug, nodeID, params); err != nil {
		return nil, false, err
	}

	// 1a-bis. A write whose whole point is surviving a reboot is refused on an
	// overlay root: it would land in the tmpfs upper layer, read back as
	// applied — the recorded hash then SUPPRESSES the re-apply — and evaporate
	// at the next boot. A key revocation that evaporates is the family's worst
	// member, which is why this refuses rather than warns. "" (an agent below
	// the reporting floor, or recovery's RAM root) changes nothing: the fact
	// is ungated for old agents, and recovery's own managed-OS hold already
	// governs there.
	if node_command.RequiresPersistentRoot(resolvedSlug) && foundNode.RootPersistenceFact() == node.RootPersistenceOverlay {
		return nil, false, &OverlayRootError{NodeID: nodeID, Slug: resolvedSlug}
	}

	// 1b. Enforce the agent-version floor before anything else. "Your agent is too
	// old" is more actionable than "not connected", so it must win when both are
	// true — and an old agent cannot decode the command anyway.
	if minVersion, required := node_command.MinAgentVersion(resolvedSlug); required {
		if !node_command.MeetsMinAgentVersion(foundNode.AgentVersion, minVersion) {
			return nil, false, &AgentTooOldError{
				NodeID:  nodeID,
				Slug:    resolvedSlug,
				Have:    foundNode.AgentVersion,
				Require: minVersion,
			}
		}
	}

	// 2b. Check node is approved (for master token authenticated nodes)
	if foundNode.ApprovalStatus != node.ApprovalStatusApproved {
		return nil, false, &NodeUnavailableError{NodeID: nodeID, Slug: resolvedSlug, State: string(foundNode.ApprovalStatus), NotApproved: true}
	}

	// 2c. One writer per disk. install_os, erase_drive and capture_image each
	// take a block device for themselves, and without this nothing refuses a
	// second one: two installs can hold the same device open O_WRONLY and
	// interleave writes for the whole download. The agent's own "device has
	// partitions" check does not cover it — it holds the image's first 4 MiB in
	// RAM until the digest matches, so for the entire download the target has no
	// partition table for that check to see.
	//
	// Scoped to those slugs rather than to the node: commands are accepted in
	// `maintenance` on purpose (it is how a stuck node is recovered), and a
	// node-wide lock would also block the install_agent and reboot that
	// legitimately follow an install.
	//
	// Fails OPEN on a query error, like the other count-based guards here: a
	// database blip must not make the fleet un-installable.
	// apply_access_config_offline mounts and rewrites the disk the machine
	// would otherwise be running from, so both its preconditions are checked
	// before any dispatch machinery: the node must actually BE netbooted (the
	// agent enforces the same gate on arrival — this refusal is the one that
	// can tell the operator what to do instead), and the target disk must be
	// named (the service's auto-pick resolves it before this point; an empty
	// value here means nobody chose a disk to write users onto).
	// The offline patch shares the access-apply's precondition and its remedy
	// wording: both mount a disk the node is not running from, and both are
	// meaningless — silently, on the wrong file — anywhere else.
	if resolvedSlug == node_command.SlugPatchFileOffline && !foundNode.IsRecoveryBootstrap() {
		return nil, false, &RecoveryRequiredError{NodeID: nodeID, Slug: resolvedSlug, Alternative: "or patch_file on a live OS"}
	}
	// capture_image shrinks the filesystem it reads, which cannot be done to a
	// live root, and the agent refuses it there — but only after the image
	// record below exists, leaving a "capturing" record nothing will fill. The
	// same predicate the managed-OS hold uses, inverted.
	if resolvedSlug == node_command.SlugCaptureImage && !foundNode.IsRecoveryBootstrap() {
		return nil, false, &RecoveryRequiredError{NodeID: nodeID, Slug: resolvedSlug}
	}
	// A capture names its disk. Without one the device falls back to a fixed
	// device name, which does not exist on a board that boots from eMMC, SD
	// or USB, and is silently the WRONG disk on one that runs from SD beside
	// an NVMe data disk — a backup nobody can tell is of the other disk until
	// it is restored. The recovery root is RAM, so there is no "disk the node
	// runs from" to fall back to either: the only answer the server can stand
	// behind is the node's sole reported disk, and several are refused with
	// their names.
	if resolvedSlug == node_command.SlugCaptureImage {
		if td, _ := params["target_device"].(string); strings.TrimSpace(td) == "" {
			resolved, rerr := soleReportedDisk(foundNode)
			if rerr != nil {
				return nil, false, app_errors.OperatorRefusal(rerr)
			}
			params["target_device"] = resolved
		}
	}
	// Overwriting a used disk is a recovery-system operation. From a running
	// OS the device refuses only a disk that is mounted or busy, so an
	// unmounted second disk — data, or an earlier system — would be erased by
	// a run the operator meant for recovery. Installing onto a BLANK second
	// disk from the running OS stays allowed: without wipe the device refuses
	// any disk that already has partitions. Refused here, not only on the
	// device, so the rule holds whatever agent the node runs.
	if resolvedSlug == node_command.SlugInstallOS && !foundNode.IsRecoveryBootstrap() {
		if wipe, _ := params["wipe"].(bool); wipe {
			return nil, false, &RecoveryRequiredError{NodeID: nodeID, Slug: resolvedSlug + " with wipe", Alternative: "or install onto a blank disk without it"}
		}
	}
	if resolvedSlug == node_command.SlugApplyAccessConfigOffline {
		if !foundNode.IsRecoveryBootstrap() {
			return nil, false, &RecoveryRequiredError{NodeID: nodeID, Slug: resolvedSlug, Alternative: "or apply_access_config on a live OS"}
		}
		// An empty target resolves from the node's own reported inventory,
		// exactly as patch_file_offline's does below: the dedicated HTTP route
		// pre-resolves it in the access service, but the workflow engine and
		// the generic dispatch land here with the params as authored, and
		// refusing them forced every playbook to hardcode a device name.
		// soleReportedDisk still refuses to choose among several disks.
		if targetDevice, _ := params["target_device"].(string); strings.TrimSpace(targetDevice) == "" {
			resolved, rerr := soleReportedDisk(foundNode)
			if rerr != nil {
				return nil, false, app_errors.OperatorRefusal(rerr)
			}
			params["target_device"] = resolved
		}
	}

	// install_agent's variant selects which agent lands on the disk: full
	// (default) or the telemetry-only watcher/Watcher. Validated up front, and
	// the watcher floor is CONDITIONAL (strip_identity precedent): a recovery
	// agent below it ignores the unknown proto field and silently installs the
	// FULL agent — the opposite of what an operator choosing Watcher asked
	// for — so that dispatch is refused rather than degraded.
	if resolvedSlug == node_command.SlugInstallAgent {
		if rawVariant, ok := params["variant"].(string); ok && rawVariant != "" {
			switch rawVariant {
			case node_command.AgentVariantFull, node_command.AgentVariantWatcher:
			default:
				return nil, false, app_errors.OperatorRefusal(fmt.Errorf("install_agent: unknown variant %q (allowed: full, watcher)", rawVariant))
			}
			if rawVariant == node_command.AgentVariantWatcher {
				if !node_command.MeetsMinAgentVersion(foundNode.AgentVersion, node_command.MinAgentVersionWatcherInstall) {
					return nil, false, &AgentTooOldError{
						NodeID:  nodeID,
						Slug:    "install_agent (variant=watcher)",
						Have:    foundNode.AgentVersion,
						Require: node_command.MinAgentVersionWatcherInstall,
					}
				}
				// A Watcher node executes NO commands, so the only route back to
				// managing it is a recovery boot — which happens only if the
				// device's own EEPROM asks the platform for a boot image. Both
				// halves are checked before the door closes, because nothing on
				// the platform side can reopen it:
				//
				//   1. the node must BE netbooted right now, which is the direct
				//      evidence that its EEPROM reaches this install at all; and
				//   2. its reported boot order must still reach http once a
				//      bootable OS is on the disk — the state the install itself
				//      creates.
				//
				// (1) alone is not enough: a device can netboot today because its
				// disk is empty and still never netboot again once the install
				// gives nvme something to boot.
				//
				// "Netbooted" has to be asked as CurrentBootDevice == http, NOT
				// as IsRecoveryBootstrap. The same bootstrap image reports `http`
				// when it netbooted and `sd` when it came up from a PROVISIONING
				// CARD — its own doc comment says so — and it sets
				// recovery_bootstrap either way. A card a human inserted is no
				// evidence at all that this device's EEPROM reaches this
				// platform, and that is the entire question here. (Asking for
				// the flag as well would add nothing: `http` already implies it.)
				if foundNode.CurrentBootDevice() != "http" {
					return nil, false, &WatcherInstallUnsafeError{
						NodeID: nodeID,
						Reason: "the node did not netboot into recovery (it is running from local media), so the platform has no evidence its bootloader can reach this install",
					}
				}
				if ok, reason := node.BootOrderCanReturnToNetboot(foundNode.ReportedBootOrder()); !ok {
					return nil, false, &WatcherInstallUnsafeError{NodeID: nodeID, Reason: reason}
				}
			}
		}
	}

	if node_command.IsDeviceExclusive(resolvedSlug) {
		active, qerr := s.nodeCommandService.FindActiveDeviceCommand(ctx, nodeID)
		if qerr != nil {
			s.logger.WarnContext(ctx, "could not check for an in-flight device command; allowing the dispatch",
				"node_id", nodeID, "slug", resolvedSlug, "error", qerr)
		} else if active != nil {
			return nil, false, &DeviceBusyError{
				NodeID:    nodeID,
				Slug:      resolvedSlug,
				ActiveID:  active.ID,
				ActiveCmd: active.Slug,
			}
		}
	}

	// 3. Verify gRPC connection exists
	if s.communicationService == nil {
		return nil, false, errors.New("communication service not available")
	}

	// A queueable command may be registered for a node that is offline or
	// otherwise unavailable: it waits and is delivered on reconnect. Anything
	// else still fails fast — an imperative command applied hours late is a
	// surprise, not a convenience.
	queueForLater := node_command.IsQueueable(resolvedSlug)

	// 2. Check node status is "online" or "maintenance" (commands can be sent to nodes in maintenance)
	if !queueForLater && foundNode.Status != node.StatusOnline && foundNode.Status != node.StatusMaintenance {
		return nil, false, &NodeUnavailableError{NodeID: nodeID, Slug: resolvedSlug, State: string(foundNode.Status)}
	}

	nodeIDStr := nodeID.String()
	connected := s.communicationService.IsNodeConnected(nodeIDStr)
	if !connected && !queueForLater {
		return nil, false, &NodeNotConnectedError{NodeID: nodeID}
	}

	// 4. Determine slug and build payload based on command type
	var slug string
	payload := make(map[string]interface{})
	// The capture record created below, when this is a capture_image: a
	// refusal after it exists must settle it, or it reads "capturing" with no
	// command that will ever fill it.
	var captureImageID *uuid.UUID

	switch command {
	case node_command.SlugReboot, "restart":
		slug = node_command.SlugReboot
		if params != nil {
			if delay, ok := params["delay_seconds"].(float64); ok {
				payload["delay_seconds"] = s.clampDelaySeconds(ctx, nodeID, slug, delay)
			}
		}

	case node_command.SlugShutdown:
		slug = node_command.SlugShutdown
		if params != nil {
			if delay, ok := params["delay_seconds"].(float64); ok {
				payload["delay_seconds"] = s.clampDelaySeconds(ctx, nodeID, slug, delay)
			}
		}

	case node_command.SlugStatusCheck:
		slug = node_command.SlugStatusCheck
		// No payload needed

	case node_command.SlugInstallOS:
		slug = node_command.SlugInstallOS
		if params == nil {
			return nil, false, app_errors.OperatorRefusal(errors.New("install_os command requires parameters"))
		}
		// Copy all params to payload
		for k, v := range params {
			payload[k] = v
		}
		// The image is checked before a row exists: an immediate, actionable
		// refusal beats a command that fails a moment after the request was
		// answered success. The authoritative fail-closed checks run again at
		// proto-build time; see checkInstallImage for what defers to them.
		if err := s.checkInstallImage(ctx, accountID, foundNode, params); err != nil {
			return nil, false, err
		}
		// Credentials (username/password/ssh_key) are intentionally NOT stored in
		// the persisted payload: they are resolved from the node's effective
		// provisioning when the proto command is built (see
		// buildProtoCommandFromNodeCommand), so the password never lands in the
		// command record, WebSocket events, or the node-command API.
		stripCredentialFields(payload)

	case node_command.SlugInstallAgent:
		slug = node_command.SlugInstallAgent
		if params == nil {
			return nil, false, app_errors.OperatorRefusal(errors.New("install_agent command requires parameters"))
		}
		// The variant precondition (validity + the watcher floor) was checked
		// before the dispatch machinery, next to the offline-apply gates.
		// Copy all params to payload
		for k, v := range params {
			payload[k] = v
		}

	case node_command.SlugUpdateAgent:
		slug = node_command.SlugUpdateAgent
		// Self-update of the running managed agent. Only the version is needed;
		// the agent resolves the CDN URL and installs over the live OS.
		// allow_downgrade is the anti-rollback override: the agent refuses a
		// package older than its running version unless it rides along.
		update, perr := parseUpdateAgentParams(params)
		if perr != nil {
			return nil, false, perr
		}
		// Naming the version the node already runs reinstalls the same package
		// and restarts the agent, abandoning whatever else it is executing.
		// Refused for a direct dispatch unless the operator asks for exactly
		// that. A playbook block is exempt: a refusal there fails the block,
		// and "make sure the fleet runs X" is a reasonable playbook to run
		// against nodes that already do.
		if workflowExecutionNodeID == nil && !update.Reinstall && sameAgentVersion(update.Version, foundNode.AgentVersion) {
			return nil, false, &AgentAlreadyCurrentError{NodeID: nodeID, Version: foundNode.AgentVersion}
		}
		// reinstall means "this same version again", and nothing else: the
		// wire's only force flag also lifts the agent's anti-rollback check, so
		// a reinstall naming any other version — or "latest", which the
		// platform cannot compare — would be a way to move a device backwards
		// without asking for allow_downgrade. Refused here so the operator
		// learns it; the send re-checks against the version the node runs at
		// that moment, because a queued command can be delivered after the
		// node has moved on. A playbook block is not refused, for the reason
		// above; the send-time check is what keeps its flag harmless.
		if workflowExecutionNodeID == nil && update.Reinstall && !sameAgentVersion(update.Version, foundNode.AgentVersion) {
			running := foundNode.AgentVersion
			if strings.TrimSpace(running) == "" {
				running = "unknown"
			}
			return nil, false, app_errors.OperatorRefusal(fmt.Errorf(
				"update_agent: reinstall applies only to the version the node runs (%s); name that version, or use allow_downgrade to move to an older one",
				running))
		}
		payload["agent_version"] = update.Version
		payload["allow_downgrade"] = update.AllowDowngrade
		if update.Reinstall {
			payload["reinstall"] = true
		}

	case node_command.SlugSetHostname:
		slug = node_command.SlugSetHostname
		if params == nil {
			return nil, false, app_errors.OperatorRefusal(errors.New("set_hostname command requires a hostname"))
		}
		hostname, ok := params["hostname"].(string)
		if !ok || strings.TrimSpace(hostname) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("set_hostname command requires a non-empty hostname"))
		}
		normalized, herr := node_command.NormalizeHostname(hostname)
		if herr != nil {
			return nil, false, fmt.Errorf("set_hostname: %w", herr)
		}
		payload["hostname"] = normalized

	case node_command.SlugProvisionConnection:
		slug = node_command.SlugProvisionConnection
		// Credentials are resolved from the node's effective provisioning at
		// proto-build time and are intentionally not persisted here (see the
		// install_os case above). What the build would refuse — no username,
		// no credential — is refused here, before a row exists.
		if err := s.checkProvisionConnection(ctx, accountID, foundNode); err != nil {
			return nil, false, err
		}

		// Add params (target_device, root_partition)
		if params != nil {
			if targetDevice, ok := params["target_device"].(string); ok && targetDevice != "" {
				payload["target_device"] = targetDevice
			}
			if rootPartition, ok := params["root_partition"].(float64); ok {
				payload["root_partition"] = int(rootPartition)
			} else {
				payload["root_partition"] = 0
			}
		} else {
			payload["root_partition"] = 0
		}

	case node_command.SlugUpdateBootOrder:
		slug = node_command.SlugUpdateBootOrder
		if params == nil {
			return nil, false, app_errors.OperatorRefusal(errors.New("update_boot_order command requires parameters"))
		}

		// Extract boot_order from params
		bootOrderRaw, ok := params["boot_order"]
		if !ok {
			return nil, false, app_errors.OperatorRefusal(errors.New("boot_order parameter is required"))
		}

		// Convert to []string
		bootOrderInterface, ok := bootOrderRaw.([]interface{})
		if !ok {
			return nil, false, app_errors.OperatorRefusal(errors.New("boot_order must be an array"))
		}

		bootOrder := make([]string, 0, len(bootOrderInterface))
		for _, item := range bootOrderInterface {
			if str, ok := item.(string); ok {
				bootOrder = append(bootOrder, str)
			} else {
				return nil, false, app_errors.OperatorRefusal(errors.New("boot_order array must contain only strings"))
			}
		}

		// Validate boot order array
		if err := node.ValidateBootOrderArray(bootOrder); err != nil {
			return nil, false, app_errors.OperatorRefusal(fmt.Errorf("invalid boot order: %w", err))
		}

		payload["boot_order"] = bootOrder
		// Forward the signed-download capability for the per-device EEPROM
		// package (the agent presents it on by-serial requests).
		pkgExp, _ := params["pkg_exp"].(string)
		pkgSig, _ := params["pkg_sig"].(string)
		// REQUIRED, not optional. Without the capability the agent cannot fetch
		// the per-serial package and falls back to a SHARED EEPROM image, which
		// overwrites this device's per-serial netboot identity (its embedded
		// HTTP_PATH and adoption token) — a green run that costs the node its
		// ability to netboot at all.
		//
		// The capability is only minted after eeprom_provision has actually
		// baked and signed an image for this serial, so its presence is the
		// proof that the bake happened. That makes this the enforcement of "the
		// per-serial bake is mandatory": the slug stays dispatchable from the
		// generic command API and from a playbook block, and both are refused
		// here with the route that does it properly.
		if err := requireEEPROMCapability(nodeID, pkgExp, pkgSig); err != nil {
			return nil, false, app_errors.OperatorRefusal(err)
		}
		payload["pkg_exp"] = pkgExp
		payload["pkg_sig"] = pkgSig

	case node_command.SlugUpdateFile:
		slug = node_command.SlugUpdateFile
		if params == nil {
			return nil, false, app_errors.OperatorRefusal(errors.New("update_file command requires parameters"))
		}
		path, ok := params["path"].(string)
		if !ok || strings.TrimSpace(path) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("update_file command requires a non-empty path"))
		}
		content, ok := params["content"].(string)
		if !ok {
			return nil, false, app_errors.OperatorRefusal(errors.New("update_file command requires content"))
		}
		payload["path"] = path
		payload["content"] = content
		// The agent verifies content integrity by hash; compute it here when the
		// caller (e.g. a workflow step) did not provide one.
		hash, _ := params["hash"].(string)
		if hash == "" {
			hashBytes := sha256.Sum256([]byte(content))
			hash = hex.EncodeToString(hashBytes[:])
		}
		payload["hash"] = hash
		if version, ok := params["version"].(float64); ok {
			payload["version"] = version
		}

	case node_command.SlugPatchFile:
		slug = node_command.SlugPatchFile
		if params == nil {
			return nil, false, app_errors.OperatorRefusal(errors.New("patch_file command requires parameters"))
		}
		content, ok := params["content"].(string)
		if !ok || strings.TrimSpace(content) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("patch_file command requires non-empty content"))
		}
		payload["content"] = content
		if path, ok := params["path"].(string); ok && strings.TrimSpace(path) != "" {
			// Same policy as read_file/delete_file: patch_file is reachable
			// through the generic /command endpoint and workflow blocks, and
			// it REWRITES the file it is pointed at. The refused list (proc,
			// sys, shadow, host keys, the agent's own binary) applies; the
			// protected list does not — config.txt being editable is the whole
			// point of the command.
			cleanPatch, perr := node_file.ValidateTrackPath(strings.TrimSpace(path))
			if perr != nil {
				return nil, false, app_errors.OperatorRefusal(perr)
			}
			payload["path"] = cleanPatch
		} else {
			payload["path"] = "/boot/firmware/config.txt"
		}
		if marker, ok := params["marker"].(string); ok && strings.TrimSpace(marker) != "" {
			// The agent matches marker lines per line: a marker carrying an
			// interior newline can never match its own begin/end pair, so every
			// apply APPENDS a fresh block instead of replacing — unbounded
			// file growth reported as success. Same charset the block ids use.
			m := strings.TrimSpace(marker)
			if !patchMarkerRe.MatchString(m) {
				return nil, false, app_errors.OperatorRefusal(fmt.Errorf("patch_file marker must match %s", patchMarkerRe.String()))
			}
			payload["marker"] = m
		} else {
			payload["marker"] = "orchestrator"
		}

	case node_command.SlugPatchFileOffline:
		slug = node_command.SlugPatchFileOffline
		if params == nil {
			return nil, false, app_errors.OperatorRefusal(errors.New("patch_file_offline command requires parameters"))
		}
		content, ok := params["content"].(string)
		if !ok || strings.TrimSpace(content) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("patch_file_offline command requires non-empty content"))
		}
		payload["content"] = content
		// The SAME path policy as the live patch_file. It is if anything more
		// important here: the target is a filesystem the node is not running,
		// so a refused path (shadow, host keys, the agent's own binary) cannot
		// even be argued about as "the node's own business".
		if path, ok := params["path"].(string); ok && strings.TrimSpace(path) != "" {
			cleanPatch, perr := node_file.ValidateTrackPath(strings.TrimSpace(path))
			if perr != nil {
				return nil, false, app_errors.OperatorRefusal(perr)
			}
			payload["path"] = cleanPatch
		} else {
			payload["path"] = "/boot/firmware/config.txt"
		}
		if marker, ok := params["marker"].(string); ok && strings.TrimSpace(marker) != "" {
			m := strings.TrimSpace(marker)
			if !patchMarkerRe.MatchString(m) {
				return nil, false, app_errors.OperatorRefusal(fmt.Errorf("patch_file_offline marker must match %s", patchMarkerRe.String()))
			}
			payload["marker"] = m
		} else {
			payload["marker"] = "orchestrator"
		}
		if v, ok := params["root_partition"].(float64); ok {
			payload["root_partition"] = v
		}
		// The target disk is resolved the same way the offline access apply
		// resolves it, from the node's own reported inventory: the agent never
		// guesses a disk it is about to write to, and asking the operator to
		// type one is how the wrong device gets named.
		if targetDevice, ok := params["target_device"].(string); ok && strings.TrimSpace(targetDevice) != "" {
			payload["target_device"] = strings.TrimSpace(targetDevice)
		} else {
			resolved, rerr := soleReportedDisk(foundNode)
			if rerr != nil {
				return nil, false, app_errors.OperatorRefusal(rerr)
			}
			payload["target_device"] = resolved
		}

	case node_command.SlugReadFile:
		slug = node_command.SlugReadFile
		path, _ := params["path"].(string)
		if strings.TrimSpace(path) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("read_file command requires a path"))
		}
		// Same policy as delete_file: read_file is also reachable through the
		// generic /command endpoint and through workflow blocks, and an unvetted
		// path there is a blocking or oversized read on the device.
		cleanRead, rerr := node_file.ValidateTrackPath(path)
		if rerr != nil {
			return nil, false, app_errors.OperatorRefusal(rerr)
		}
		payload["path"] = cleanRead
		if pattern, ok := params["pattern"].(string); ok && pattern != "" {
			payload["pattern"] = pattern
		}
		if maxBytes, ok := params["max_bytes"].(float64); ok && maxBytes > 0 {
			// Clamp rather than refuse, and clamp HERE so it also covers
			// playbooks saved before the ceiling existed (the http_request
			// timeout precedent): max_bytes is caller-supplied, the agent
			// reads up to that many bytes into memory before anything
			// truncates, and an unbounded value is an OOM in the RAM-rooted
			// recovery initramfs or a stream-killing frame. The agent has its
			// own ceiling; this is the half that reaches every device
			// regardless of its agent version.
			if ceiling := float64(node_file.MaxContentBytes); maxBytes > ceiling {
				s.logger.WarnContext(ctx, "read_file max_bytes clamped",
					"node_id", nodeID, "requested", int64(maxBytes), "ceiling", int64(ceiling))
				maxBytes = ceiling
			}
			payload["max_bytes"] = maxBytes
		}
		// A read the tracked-file flow dispatched carries who asked, so the
		// result settle can record the file without a client polling for it.
		// Taken from the context ReadNodeFile sets, never from params: no
		// caller of the generic route or a playbook can mark a read.
		if userID, ok := trackFileReadUser(ctx); ok {
			payload[results.TrackFileForKey] = userID.String()
		}

	case node_command.SlugDeleteFile:
		slug = node_command.SlugDeleteFile
		path, _ := params["path"].(string)
		if strings.TrimSpace(path) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("delete_file command requires a path"))
		}
		// The path policy is enforced here, not only in the files handler: this
		// is also the path the generic /command endpoint and any future block
		// take, and a guard that lives in one handler is not a guard.
		clean, perr := node_file.ValidateTrackPath(path)
		if perr != nil {
			return nil, false, app_errors.OperatorRefusal(perr)
		}
		if node_file.IsProtected(clean) {
			return nil, false, app_errors.OperatorRefusal(fmt.Errorf("%w: %s", node_file.ErrProtectedPath, clean))
		}
		payload["path"] = clean
		if backup, ok := params["backup"].(bool); ok {
			payload["backup"] = backup
		}

	case node_command.SlugWaitForFile:
		slug = node_command.SlugWaitForFile
		path, _ := params["path"].(string)
		pattern, _ := params["pattern"].(string)
		if strings.TrimSpace(path) == "" || strings.TrimSpace(pattern) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("wait_for_file command requires path and pattern"))
		}
		payload["path"] = path
		payload["pattern"] = pattern
		if v, ok := params["interval_seconds"].(float64); ok {
			payload["interval_seconds"] = v
		}
		if v, ok := params["timeout_seconds"].(float64); ok {
			payload["timeout_seconds"] = v
		}

	case node_command.SlugRunScript:
		slug = node_command.SlugRunScript
		script, _ := params["script"].(string)
		if strings.TrimSpace(script) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("run_script command requires a script"))
		}
		if err := validateRunScriptBody(script); err != nil {
			return nil, false, err
		}
		payload["script"] = script
		if v, ok := params["timeout_seconds"].(float64); ok {
			payload["timeout_seconds"] = v
		}

	case node_command.SlugEnableEEPROM:
		slug = node_command.SlugEnableEEPROM
		// No payload needed

	case node_command.SlugEraseDrive:
		slug = node_command.SlugEraseDrive
		if params == nil {
			return nil, false, app_errors.OperatorRefusal(errors.New("erase_drive command requires parameters"))
		}

		// Extract target_device from params
		targetDevice, ok := params["target_device"].(string)
		if !ok || targetDevice == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("target_device parameter is required"))
		}

		// Check if target device is the boot drive
		if foundNode.SystemInfo != nil && foundNode.SystemInfo.StorageDevices != nil {
			device, exists := foundNode.SystemInfo.StorageDevices[targetDevice]
			if exists && device != nil {
				// Check if any partition has mount_point == "/" (root filesystem)
				for _, partition := range device.Partitions {
					if partition.MountPoint == "/" {
						return nil, false, &BootDriveEraseError{NodeID: nodeID, Device: targetDevice}
					}
				}
			}
		}

		payload["target_device"] = targetDevice

	case node_command.SlugApplyAccessConfig:
		slug = node_command.SlugApplyAccessConfig
		// Deliberately no payload: the access set — including passwords — is
		// resolved fresh at proto-build time, so a queued command always applies
		// current intent and no credential is ever persisted in the command
		// record, its WebSocket events or the node-command API.

	case node_command.SlugApplyAccessConfigOffline:
		slug = node_command.SlugApplyAccessConfigOffline
		// Same rule as apply_access_config: only the target disk is persisted;
		// the access set is resolved fresh at proto-build time so no credential
		// ever lands in the command record. The netboot and target-device
		// preconditions were checked before the dispatch machinery, above.
		targetDevice, _ := params["target_device"].(string)
		payload["target_device"] = targetDevice
		if rootPartition, ok := params["root_partition"].(float64); ok {
			payload["root_partition"] = int(rootPartition)
		} else {
			payload["root_partition"] = 0
		}

	case node_command.SlugCaptureImage:
		slug = node_command.SlugCaptureImage
		if s.imageService == nil {
			return nil, false, fmt.Errorf("image capture is not available on this server")
		}
		title, _ := params["title"].(string)
		if strings.TrimSpace(title) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("capture_image command requires a title"))
		}
		generalize := false
		if v, ok := params["generalize"].(bool); ok {
			generalize = v
		}
		// The registration refuses a malformed target_device too, but only
		// after the record below exists.
		if err := node_command.ValidateTargetDevicePayload(params); err != nil {
			return nil, false, app_errors.OperatorRefusal(err)
		}
		// Create the pending image record now so its id is stable across command
		// rebuilds/redelivery; the signed upload URL is minted fresh at
		// proto-build time (like install_os's download URL).
		img, err := s.imageService.CreateCaptureImage(ctx, accountID, strings.TrimSpace(title), generalize, foundNode.ID, foundNode.Name)
		if err != nil {
			if errors.Is(err, app_errors.ErrImageLimitReached) {
				return nil, false, &CaptureImageLimitError{Err: err}
			}
			return nil, false, fmt.Errorf("failed to create capture image record: %w", err)
		}
		captureImageID = &img.ID
		payload["image_id"] = img.ID.String()
		payload["generalize"] = generalize
		if td, ok := params["target_device"].(string); ok && strings.TrimSpace(td) != "" {
			payload["target_device"] = strings.TrimSpace(td)
		}

	case node_command.SlugInstallPackages:
		slug = node_command.SlugInstallPackages
		pkgs := parseListParam(params["packages"])
		if len(pkgs) == 0 {
			return nil, false, app_errors.OperatorRefusal(errors.New("install_packages command requires at least one package"))
		}
		payload["packages"] = pkgs
		if v, ok := params["update"].(bool); ok {
			payload["update"] = v
		} else {
			payload["update"] = true
		}

	case node_command.SlugManageService:
		slug = node_command.SlugManageService
		name, _ := params["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("manage_service command requires a unit name"))
		}
		rawAction, _ := params["action"].(string)
		action, aerr := normalizeManageServiceAction(rawAction)
		if aerr != nil {
			return nil, false, aerr
		}
		payload["name"] = strings.TrimSpace(name)
		payload["action"] = action
		if uc, ok := params["unit_content"].(string); ok && uc != "" {
			payload["unit_content"] = uc
		}

	case node_command.SlugDownloadFile:
		slug = node_command.SlugDownloadFile
		url, _ := params["url"].(string)
		dest, _ := params["dest_path"].(string)
		if strings.TrimSpace(url) == "" || strings.TrimSpace(dest) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("download_file command requires url and dest_path"))
		}
		payload["url"] = strings.TrimSpace(url)
		payload["dest_path"] = strings.TrimSpace(dest)
		for _, k := range []string{"mode", "owner"} {
			if v, ok := params[k].(string); ok && strings.TrimSpace(v) != "" {
				payload[k] = strings.TrimSpace(v)
			}
		}
		if v, ok := params["checksum"].(string); ok && strings.TrimSpace(v) != "" {
			checksum, cerr := normalizeDownloadChecksum(v)
			if cerr != nil {
				return nil, false, cerr
			}
			payload["checksum"] = checksum
		}
		if v, ok := params["extract"].(bool); ok {
			payload["extract"] = v
		}
		headers, herr := normalizeDownloadHeaders(params["headers"])
		if herr != nil {
			return nil, false, herr
		}
		if headers != "" {
			payload["headers"] = headers
		}

	case node_command.SlugWriteFile:
		slug = node_command.SlugWriteFile
		path, _ := params["path"].(string)
		if strings.TrimSpace(path) == "" {
			return nil, false, app_errors.OperatorRefusal(errors.New("write_file command requires a path"))
		}
		content, ok := params["content"].(string)
		if !ok {
			return nil, false, app_errors.OperatorRefusal(errors.New("write_file command requires content"))
		}
		payload["path"] = strings.TrimSpace(path)
		payload["content"] = content
		for _, k := range []string{"mode", "owner"} {
			if v, ok := params[k].(string); ok && strings.TrimSpace(v) != "" {
				payload[k] = strings.TrimSpace(v)
			}
		}
		if v, ok := params["backup"].(bool); ok {
			payload["backup"] = v
		}

	case node_command.SlugApplyWatchdogConfig:
		slug = node_command.SlugApplyWatchdogConfig
		buildApplyWatchdogPayload(params, payload)

	case node_command.SlugApplyDeviceProfile:
		// Only ApplyDeviceProfile calls this (the slug is deliberately
		// unmapped for generic dispatch — authz fails it closed before it
		// reaches here), so params are the domain render, copied verbatim.
		slug = node_command.SlugApplyDeviceProfile
		path, _ := params["path"].(string)
		marker, _ := params["marker"].(string)
		contentHash, _ := params["content_hash"].(string)
		if path == "" || marker == "" || contentHash == "" {
			return nil, false, fmt.Errorf("apply_device_profile requires path, marker and content_hash")
		}
		payload["path"] = path
		payload["marker"] = marker
		payload["content"], _ = params["content"].(string)
		payload["content_hash"] = contentHash
		if kinds, ok := params["setup_kinds"].([]interface{}); ok {
			payload["setup_kinds"] = kinds
		}
		if gpio, ok := params["button_gpio"].(float64); ok {
			payload["button_gpio"] = gpio
		}
		// The identify-LED spec rides along for the agent's local button ack
		// (this switch copies only the keys it knows — a param added at the
		// dispatch site alone is silently dropped here).
		if v, ok := params["identify_backend"].(string); ok && v != "" {
			payload["identify_backend"] = v
		}
		if v, ok := params["identify_gpio"].(float64); ok {
			payload["identify_gpio"] = v
		}
		if v, ok := params["identify_color"].(string); ok && v != "" {
			payload["identify_color"] = v
		}
		if v, ok := params["identify_duration_sec"].(float64); ok && v > 0 {
			payload["identify_duration_sec"] = v
		}

	case node_command.SlugApplyLEDConfig:
		// Only SetLED calls this (the slug is unmapped for generic dispatch —
		// authz fails it closed), so params are the validated config plus the
		// board wiring the device profile resolved.
		slug = node_command.SlugApplyLEDConfig
		if err := buildApplyLEDPayload(params, payload); err != nil {
			return nil, false, err
		}

	case node_command.SlugApplyUtilityConfig:
		// Only utility.Service.ApplyToNode calls this (the slug is unmapped for
		// generic dispatch — authz fails it closed), so params are the resolved
		// set, copied verbatim.
		slug = node_command.SlugApplyUtilityConfig
		unionHash, _ := params["union_hash"].(string)
		utilities, ok := params["utilities"].([]interface{})
		if unionHash == "" || !ok || len(utilities) == 0 {
			return nil, false, fmt.Errorf("apply_utility_config requires union_hash and a non-empty utilities set")
		}
		payload["union_hash"] = unionHash
		payload["utilities"] = utilities

	case node_command.SlugIdentify:
		slug = node_command.SlugIdentify
		if d, ok := params["duration_sec"].(float64); ok {
			payload["duration_sec"] = d
		}
		if v, ok := params["stop"].(bool); ok && v {
			payload["stop"] = v
		}
		for _, k := range []string{"backend", "color"} {
			if v, ok := params[k].(string); ok && v != "" {
				payload[k] = v
			}
		}
		if g, ok := params["gpio"].(float64); ok {
			payload["gpio"] = g
		}

	default:
		return nil, false, fmt.Errorf("unknown command: %s", command)
	}

	// Record which params carry secret material, AFTER the per-slug switch: each
	// arm copies only the keys it knows, so anything smuggled in through params
	// would be dropped here — the redaction bookkeeping has to be applied to the
	// payload the switch actually built. Kept to params that survived, so a stale
	// name from a superseded template cannot redact an unrelated field.
	if len(secretDerivedParams) > 0 {
		var present []string
		for _, k := range secretDerivedParams {
			if _, ok := payload[k]; ok {
				present = append(present, k)
			}
		}
		if len(present) > 0 {
			payload[results.SecretDerivedParamsKey] = present
		}
	}
	// The graph block this dispatch executes, for the engine's lost-dispatch
	// reconciliation. Gated on the execution-node link so a generic
	// operator dispatch can never smuggle the key in through params — the
	// switch above already dropped anything it did not copy.
	if workflowBlockID != "" && workflowExecutionNodeID != nil {
		payload[results.WorkflowBlockIDKey] = workflowBlockID
	}

	// 4b. Bound any bulk field before registration. An oversized gRPC frame does
	// not fail the command — it drops the node's stream while the receive loop
	// lives on, so the node reads ONLINE forever and every later command silently
	// fails. The tracked-file Apply path holds this same line at MaxContentBytes;
	// this is the generic /command ingress reaching the same writer.
	for _, field := range []string{"content", "script", "unit_content"} {
		if v, ok := payload[field].(string); ok && len(v) > node_file.MaxContentBytes {
			return nil, false, &CommandPayloadTooLargeError{Field: field, Bytes: len(v), Limit: node_file.MaxContentBytes}
		}
	}

	// 5. Register command directly in database with optional workflow execution node ID
	nodeCmd, err := s.nodeCommandService.RegisterNodeCommandWithWorkflowNode(ctx, nodeID, slug, payload, authContext, workflowExecutionNodeID)
	if err != nil {
		if captureImageID != nil {
			if ferr := s.imageService.FailAwaitingCapture(ctx, accountID, *captureImageID); ferr != nil {
				s.logger.WarnContext(ctx, "failed to settle the capture record of a refused capture",
					"node_id", nodeID, "image_id", *captureImageID, "error", ferr)
			}
		}
		return nil, false, fmt.Errorf("failed to register node command: %w", err)
	}

	// 6. Hand the command to the gRPC server — unless the node is offline and the
	// command is queueable, in which case it stays "registered" and reconnect
	// redelivery picks it up (RedeliverQueuedCommandsForNode).
	if !connected {
		s.logger.Info("queued command for offline node",
			"node_id", nodeID, "slug", resolvedSlug, "command_id", nodeCmd.ID)
		return nodeCmd, true, nil
	}

	sendOp := node_commands.NodeCommandOperation{
		Type: node_commands.OperationTypeSendCommand,
		SendCommand: &node_commands.SendCommandOperation{
			NodeCommandID: nodeCmd.ID,
			NodeID:        nodeCmd.NodeID,
			AccountID:     nodeCmd.AccountID,
		},
	}

	if err := s.nodeCommandClient.Send(sendOp); err != nil {
		return nil, false, fmt.Errorf("failed to queue command for gRPC server: %w", err)
	}

	return nodeCmd, false, nil
}

// SendBatchCommand sends a command to multiple nodes
// This is the core implementation that processes commands for multiple nodes
func (s *Service) SendBatchCommand(ctx context.Context, cmd node_command_cmd.BatchNodeCommandCommand) (*node_command_cmd.BatchCommandResult, error) {
	// The HTTP handler validates before it authorizes each target; this is the
	// same check for the callers that never pass through it (workflow engine,
	// watchdog, tracked files), not a second limit.
	if err := cmd.Validate(); err != nil {
		return nil, err
	}

	resultItems := make([]node_command_cmd.NodeCommandResultItem, 0, len(cmd.NodeIDs))

	for _, nodeID := range cmd.NodeIDs {
		nodeCmd, queued, err := s.sendCommandToNode(ctx, nodeID, cmd.Command, cmd.Params, cmd.AuthContext, cmd.WorkflowExecutionNodeID, cmd.SecretDerivedParams, cmd.WorkflowBlockID)
		if err != nil {
			item := node_command_cmd.NodeCommandResultItem{
				NodeID:  nodeID,
				Success: false,
				Error:   err.Error(),
				Err:     err,
			}
			// A node whose agent predates the command was deliberately not
			// attempted. Reported as skipped so the operator sees "update these
			// agents" instead of a wall of indistinguishable failures.
			var tooOld *AgentTooOldError
			if errors.As(err, &tooOld) {
				item.Skipped = true
				item.SkipReason = node_command_cmd.SkipReasonAgentTooOld
			}
			// Same for a node already on the requested agent: nothing to do,
			// and not a failure an operator should go looking into.
			var current *AgentAlreadyCurrentError
			if errors.As(err, &current) {
				item.Skipped = true
				item.SkipReason = node_command_cmd.SkipReasonAgentAlreadyCurrent
			}
			resultItems = append(resultItems, item)
		} else {
			resultItems = append(resultItems, node_command_cmd.NodeCommandResultItem{
				NodeID:    nodeID,
				Success:   true,
				CommandID: &nodeCmd.ID,
				Queued:    queued,
			})
		}
	}

	return node_command_cmd.NewBatchCommandResult(resultItems), nil
}

// SendUpdateFileCommand sends an update file command to a node using a file version ID
// The file version should already have status "applying" (set by ApplyFileVersion)
func (s *Service) SendUpdateFileCommand(ctx context.Context, fileID uuid.UUID, accountID uuid.UUID) error {
	// 1. Verify file service is available
	if s.fileService == nil {
		return errors.New("file service not available")
	}

	// 2. Get the file version
	file, err := s.fileService.GetByID(ctx, fileID)
	if err != nil {
		return fmt.Errorf("failed to get file: %w", err)
	}

	// 3. Verify node exists and belongs to account
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, file.NodeID)
	if err != nil {
		return err
	}

	// 4. Check node status is "online"
	if foundNode.Status != node.StatusOnline {
		return fmt.Errorf("node is not online (current status: %s)", foundNode.Status)
	}

	// A netbooted node's filesystem is the recovery initramfs, not the disk the
	// operator is looking at: the write would report success and evaporate on
	// the next reboot, leaving the platform believing the device holds content
	// it never had.
	if _, err := s.assertFileOperationAllowed(ctx, accountID, file.NodeID); err != nil {
		return err
	}

	// A version can only be applied for a file the platform actually follows,
	// and only when its permissions are known: the agent's atomic rename
	// installs a fresh inode, so writing back without a recorded mode and owner
	// turns a 0600 file into root:root 0644 — a secret published by an edit
	// that looked routine.
	if s.trackedFileRepo != nil {
		tracked, terr := s.trackedFileRepo.GetByNodeIDAndPath(ctx, file.NodeID, file.Path)
		switch {
		case errors.Is(terr, node_file.ErrNotTracked):
			return fmt.Errorf("%w: %s", node_file.ErrNotTracked, file.Path)
		case terr != nil:
			return terr
		case !tracked.CanApply():
			return fmt.Errorf("this file's permissions are unknown, so writing it back could change them; refresh it from the device first")
		}
	}

	// 5. Verify gRPC connection exists
	if s.communicationService == nil {
		return errors.New("communication service not available")
	}

	nodeIDStr := file.NodeID.String()
	if !s.communicationService.IsNodeConnected(nodeIDStr) {
		return fmt.Errorf("node %s is not connected", nodeIDStr)
	}

	// Never write the at-rest envelope onto a device. If a backend without the
	// decryption code is ever deployed over encrypted rows again, the editor
	// shows ciphertext and this is what stops the operator applying it into a
	// boot config.
	if node_file.LooksLikeStoredCiphertext(file.Content) {
		return fmt.Errorf("this version reads as stored ciphertext rather than file content; the backend cannot decrypt it, so applying it would corrupt the file on the device")
	}

	// Belt and braces for rows that predate the save-time cap, and for any
	// future caller that skips it: an oversized command frame does not fail the
	// command, it drops the node's gRPC stream. The agent's receive limit is
	// gRPC's 4 MiB default and cannot change without an agent release, so the
	// platform is the side that has to hold the line.
	if len(file.Content) > node_file.MaxContentBytes {
		return fmt.Errorf("%w: this version is %d KB; the limit is %d KB",
			node_file.ErrFileTooLarge, len(file.Content)/1024, node_file.MaxContentBytes/1024)
	}

	// 6. Verify file status is "applying"
	if file.Status != node_file.StatusApplying {
		return fmt.Errorf("file version status must be 'applying' to send command (current: %s)", file.Status)
	}

	// 6. Choose the command that can actually write this path.
	//
	// update_file is confined to the agent's two config.txt paths and needs the
	// Raspberry-Pi-only file reader, so it cannot apply a user-tracked file at
	// all. write_file writes any path — and carries mode/owner, without which
	// the agent's atomic rename would leave the file root:root 0644. Old agents
	// keep the legacy path for the config files, which is all they ever
	// supported.
	slug := node_command.SlugWriteFile
	payload := map[string]interface{}{
		"path":    file.Path,
		"content": file.Content,
		"backup":  true,
		// Carried so the command's terminal status can settle this version;
		// write_file emits no FileChangedEvent of its own.
		"version": file.Version,
	}
	writeFloor, _ := node_command.MinAgentVersion(node_command.SlugWriteFile)
	if !node_command.MeetsMinAgentVersion(foundNode.AgentVersion, writeFloor) {
		if !node_file.IsPlatformPath(file.Path) {
			return fmt.Errorf("this node's agent (%s) cannot write %s; update the agent first",
				foundNode.AgentVersion, file.Path)
		}
		slug = node_command.SlugUpdateFile
		payload = map[string]interface{}{
			"path":    file.Path,
			"content": file.Content,
			"hash":    file.Hash,
			"version": file.Version,
		}
	}
	if slug == node_command.SlugWriteFile && s.trackedFileRepo != nil {
		if tracked, terr := s.trackedFileRepo.GetByNodeIDAndPath(ctx, file.NodeID, file.Path); terr == nil {
			if tracked.Mode != "" {
				payload["mode"] = tracked.Mode
			}
			if tracked.Owner != "" {
				owner := tracked.Owner
				if tracked.Group != "" {
					owner += ":" + tracked.Group
				}
				payload["owner"] = owner
			}
		}
	}

	// 7. Minimal auth context: accountID is already validated via the node
	// ownership check above. It has to be a real commands.AuthContext — the
	// registrar type-asserts for GetAccountID(), so the anonymous struct that
	// used to live here failed every Apply with "missing account ID".
	authContext := commands.NewAuthContext(accountID, uuid.Nil)

	// 8. Register command directly in database
	nodeCmd, err := s.nodeCommandService.RegisterNodeCommand(ctx, file.NodeID, slug, payload, authContext)
	if err != nil {
		return fmt.Errorf("failed to register node command: %w", err)
	}

	// 9. Queue SendCommand operation for gRPC server
	sendOp := node_commands.NodeCommandOperation{
		Type: node_commands.OperationTypeSendCommand,
		SendCommand: &node_commands.SendCommandOperation{
			NodeCommandID: nodeCmd.ID,
			NodeID:        nodeCmd.NodeID,
			AccountID:     nodeCmd.AccountID,
		},
	}

	if err := s.nodeCommandClient.Send(sendOp); err != nil {
		return fmt.Errorf("failed to queue command for gRPC server: %w", err)
	}

	return nil
}

// notifyNodeCreated sends a node created event via websocket
func (s *Service) notifyNodeCreated(node node.Node) {
	if s.websocketClient == nil {
		return
	}
	nodeResult := results.NewNodeResult(&node)
	// OU-scoped: a node event carries hostname, IP and state, which the list
	// handlers already withhold from members outside the node's OU.
	err := s.websocketClient.Send(websockets3.NewAccountMessageForOU(node.AccountID, node.OUID, websockets3.MessageWrapper{
		Type:    "node_created",
		Payload: nodeResult,
	}))
	if err != nil && s.logger != nil {
		s.logger.Warn("failed to send node created websocket event", "node_id", node.ID, "error", err)
	}
}

// notifyNodeUpdated sends a node updated event via websocket
func (s *Service) notifyNodeUpdated(node node.Node) {
	if s.websocketClient == nil {
		return
	}
	nodeResult := results.NewNodeResult(&node)
	// OU-scoped: a node event carries hostname, IP and state, which the list
	// handlers already withhold from members outside the node's OU.
	err := s.websocketClient.Send(websockets3.NewAccountMessageForOU(node.AccountID, node.OUID, websockets3.MessageWrapper{
		Type:    "node_updated",
		Payload: nodeResult,
	}))
	if err != nil && s.logger != nil {
		s.logger.Warn("failed to send node updated websocket event", "node_id", node.ID, "error", err)
	}
}

// NotifyNodeChanged re-reads a node and pushes node_updated, for a write made
// outside this service (its unit, its policy overrides). Best-effort: a read
// failure is logged and nothing is sent.
func (s *Service) NotifyNodeChanged(ctx context.Context, accountID, nodeID uuid.UUID) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "node change not pushed: node read failed", "node_id", nodeID, "error", err)
		}
		return
	}
	s.notifyNodeUpdated(*n)
}

// notifyNodeDeleted sends a node deleted event via websocket
func (s *Service) notifyNodeDeleted(node node.Node) {
	if s.websocketClient == nil {
		return
	}
	nodeResult := results.NewNodeResult(&node)
	// OU-scoped: a node event carries hostname, IP and state, which the list
	// handlers already withhold from members outside the node's OU.
	err := s.websocketClient.Send(websockets3.NewAccountMessageForOU(node.AccountID, node.OUID, websockets3.MessageWrapper{
		Type:    "node_deleted",
		Payload: nodeResult,
	}))
	if err != nil && s.logger != nil {
		s.logger.Warn("failed to send node deleted websocket event", "node_id", node.ID, "error", err)
	}
}

// GetByIDAndAccount retrieves a node by ID and account ID (for internal use by gRPC server)
func (s *Service) GetByIDAndAccount(ctx context.Context, nodeID, accountID uuid.UUID) (*node.Node, error) {
	return s.nodeRepo.GetByIDAndAccount(ctx, nodeID, accountID)
}

// AgentVersionForNode reports the agent version a node currently runs. Used to
// re-apply the version floor when redelivering a queued command: the node may
// have returned on an older agent than it had when the command was queued.
func (s *Service) AgentVersionForNode(ctx context.Context, accountID, nodeID uuid.UUID) (string, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return "", err
	}
	return n.AgentVersion, nil
}

// UpdateAgentInfo updates agent version and last seen timestamp (for internal use by gRPC server)
func (s *Service) UpdateAgentInfo(ctx context.Context, nodeID uuid.UUID, agentVersion, agentKind string, ledSources []string, lastSeen time.Time) error {
	// Use repository method directly - it's optimized for this operation
	return s.nodeRepo.UpdateAgentInfo(ctx, nodeID, agentVersion, agentKind, ledSources, lastSeen)
}

// MarkAwaitingReturn / ClearAwaitingReturn maintain the "the platform expects
// this node back" stamp (see domain/node). Both best-effort at every caller:
// the stamp is a UI honesty feature, never worth failing a settle over.
func (s *Service) MarkAwaitingReturn(ctx context.Context, nodeID uuid.UUID, slug string) error {
	return s.nodeRepo.MarkAwaitingReturn(ctx, nodeID, slug, time.Now())
}

func (s *Service) ClearAwaitingReturn(ctx context.Context, nodeID uuid.UUID) error {
	return s.nodeRepo.ClearAwaitingReturn(ctx, nodeID)
}

// RotateAgentInstance records the instance id the connecting agent process
// reports and says how it relates to the previous session's. Called from the
// gRPC ConnectionInit arm before the stale-command sweep, which uses the
// verdict to decide whether in_progress commands are still being executed
// (same process — leave them alone) or certainly lost (new process — fail them
// now). An error degrades to Unknown at the caller, i.e. to the grace-period
// heuristic; never to Changed, which would fail live work on a read hiccup.
func (s *Service) RotateAgentInstance(ctx context.Context, nodeID uuid.UUID, instanceID string) (node.AgentInstanceContinuity, error) {
	if instanceID == "" {
		return node.AgentInstanceUnknown, nil
	}
	previous, err := s.nodeRepo.RotateAgentInstance(ctx, nodeID, instanceID)
	if err != nil {
		return node.AgentInstanceUnknown, err
	}
	return node.AgentInstanceContinuityOf(previous, instanceID), nil
}

// UpdateHeartbeat updates the heartbeat timestamp (for internal use by gRPC server)
func (s *Service) UpdateHeartbeat(ctx context.Context, nodeID uuid.UUID, timestamp time.Time) error {
	// Use repository method directly - heartbeats are too frequent for websocket events
	return s.nodeRepo.UpdateHeartbeat(ctx, nodeID, timestamp)
}

// UpdateStatus updates the node status (for internal use by gRPC server)
// This method doesn't send WebSocket events - use UpdateStatusWithAccount if you need WebSocket notifications
//
// A write that would ADMIT the row (an approved row leaving a pre-connect
// status) goes through the device cap: see writeStatus.
func (s *Service) UpdateStatus(ctx context.Context, nodeID uuid.UUID, status string) error {
	return s.writeStatus(ctx, nodeID, status, "")
}

// writeStatus is every status-only write the service makes. The repository
// refuses a plain write that would carry an approved row out of a
// pre-connect status (*node.AdmissionRequiredError), because that move is
// the ADMISSION the device cap bounds; it is retried here under the cap,
// with the same refusal and audit as the first connect. Moves inside the
// admitted set, and moves into a pre-connect status, never reach the lock.
func (s *Service) writeStatus(ctx context.Context, nodeID uuid.UUID, status, slug string) error {
	err := s.nodeRepo.UpdateStatusPinnedBy(ctx, nodeID, status, slug)
	var required *node.AdmissionRequiredError
	if !errors.As(err, &required) {
		return err
	}
	return s.underDeviceCap(ctx, nodeID, required.AccountID, audit.SystemActor(required.AccountID), "node:"+nodeID.String(),
		func(limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
			return s.nodeRepo.UpdateStatusUnderDeviceCap(ctx, nodeID, status, slug, limits)
		})
}

// UpdateStatusWithAccount updates the node status and sends WebSocket event.
// The pin cause is cleared — see UpdateStatusWithAccountPinnedBy.
func (s *Service) UpdateStatusWithAccount(ctx context.Context, nodeID, accountID uuid.UUID, status string) error {
	return s.updateStatusWithAccount(ctx, nodeID, accountID, status, "")
}

// UpdateStatusWithAccountPinnedBy is the same write, recording which command
// imposed the status. Only the command service calls it: a transient status is
// something a command does to a node, and node.Node.PinnedBySlug is what lets a
// reader tell a flash that keeps the device up from a genuine reboot.
func (s *Service) UpdateStatusWithAccountPinnedBy(ctx context.Context, nodeID, accountID uuid.UUID, status, slug string) error {
	return s.updateStatusWithAccount(ctx, nodeID, accountID, status, slug)
}

func (s *Service) updateStatusWithAccount(ctx context.Context, nodeID, accountID uuid.UUID, status, slug string) error {
	// Update status in repository
	if err := s.writeStatus(ctx, nodeID, status, slug); err != nil {
		return err
	}

	// Fetch updated node to send WebSocket event
	node, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err == nil && node != nil {
		s.notifyNodeUpdated(*node)
	}

	return nil
}

// GetStaleOnlineNodes finds nodes marked as online but haven't been seen recently (system-level, no account context)
func (s *Service) GetStaleOnlineNodes(ctx context.Context, threshold time.Time) ([]node.Node, error) {
	return s.nodeRepo.GetStaleOnlineNodes(ctx, threshold)
}

// GetStaleTransientNodes finds nodes stuck in 'restarting' or 'maintenance' with
// no recent heartbeat (system-level, no account context)
func (s *Service) GetStaleTransientNodes(ctx context.Context, threshold time.Time) ([]node.Node, error) {
	return s.nodeRepo.GetStaleTransientNodes(ctx, threshold)
}

// ApproveNode approves a pending node (for master token authentication)
// Returns ErrNodeNotFound if node doesn't exist or doesn't belong to account
// Returns ErrNodeAlreadyApproved if node is already approved
func (s *Service) ApproveNode(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) (*node.Node, error) {
	// Get node and verify ownership
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}

	// Check if node is already approved
	if foundNode.IsApproved() {
		return nil, app_errors.ErrNodeAlreadyApproved
	}

	// Approving a pending device that has connected is its ADMISSION, so the
	// write runs under the device cap; refused, the row stays pending and
	// can be approved once a slot is free.
	foundNode.Approve()
	admitUnderCap := func() error {
		return s.underDeviceCap(ctx, nodeID, accountID, audit.UserActor(accountID, actorUserID), "",
			func(limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
				return s.nodeRepo.ApproveUnderDeviceCap(ctx, nodeID, limits)
			})
	}
	if foundNode.IsAdmitted() {
		err = admitUnderCap()
	} else {
		// The plain approval lands only while the row is still pre-connect;
		// a device that connected since the read makes this its admission.
		err = s.nodeRepo.UpdateApprovalStatus(ctx, nodeID, string(node.ApprovalStatusApproved))
		var required *node.AdmissionRequiredError
		if errors.As(err, &required) {
			err = admitUnderCap()
		}
	}
	if err != nil {
		return nil, err
	}

	// Get updated node
	updatedNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}

	// The admission is recorded under the operator who made it; a refused
	// one is recorded by the cap check above and never reaches here.
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionNodeApprove, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("name=%q", updatedNode.Name))

	// Notify about node update
	s.notifyNodeUpdated(*updatedNode)

	s.logger.InfoContext(ctx, "node approved", "node_id", nodeID, "account_id", accountID)
	s.convergeOnApproval(ctx, accountID, updatedNode)
	return updatedNode, nil
}

// RejectNode rejects a pending node (for master token authentication)
// Returns ErrNodeNotFound if node doesn't exist or doesn't belong to account
// Returns ErrNodeNotPendingApproval if node is not pending approval
func (s *Service) RejectNode(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) (*node.Node, error) {
	// Get node and verify ownership
	foundNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}

	// Check if node is pending approval
	if !foundNode.IsPendingApproval() {
		return nil, app_errors.ErrNodeNotPendingApproval
	}

	// Update approval status
	err = s.nodeRepo.UpdateApprovalStatus(ctx, nodeID, string(node.ApprovalStatusRejected))
	if err != nil {
		return nil, err
	}

	// Get updated node
	updatedNode, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}

	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionNodeReject, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("name=%q", updatedNode.Name))

	// Notify about node update
	s.notifyNodeUpdated(*updatedNode)

	s.logger.InfoContext(ctx, "node rejected", "node_id", nodeID, "account_id", accountID)
	return updatedNode, nil
}

// GetByHardwareSerial retrieves a node by its hardware serial (from fingerprint)
func (s *Service) GetByHardwareSerial(ctx context.Context, accountID uuid.UUID, hardwareSerial string) (*node.Node, error) {
	return s.nodeRepo.GetByHardwareSerial(ctx, accountID, hardwareSerial)
}

func (s *Service) GetByHardwareSerialGlobal(ctx context.Context, hardwareSerial string) (*node.Node, error) {
	return s.nodeRepo.GetByHardwareSerialGlobal(ctx, hardwareSerial)
}

// ConnectWithMasterToken handles connection from an agent using master token authentication
// Returns the existing or newly created node
func (s *Service) ConnectWithMasterToken(ctx context.Context, masterTokenID, accountID uuid.UUID, fingerprint node.HardwareFingerprint, defaults services.NodeDefaults) (*node.Node, error) {
	hardwareSerial := fingerprint.HardwareSerial

	// Cross-account serial uniqueness — one check up front covers all three
	// binds below (MAC rebind, placeholder reconcile, new-node create). Safe at
	// the top because the guard passes any own-account match, so existing-node
	// reuse and reconnects are unaffected.
	if err := s.guardSerialBinding(ctx, accountID, hardwareSerial); err != nil {
		return nil, err
	}

	// Try to find existing node by hardware serial (from fingerprint)
	existingNode, err := s.nodeRepo.GetByHardwareSerial(ctx, accountID, hardwareSerial)
	if err == nil && existingNode != nil {
		// Node already exists, update fingerprint if needed and return
		s.logger.InfoContext(ctx, "master token connection: existing node found",
			"node_id", existingNode.ID,
			"hardware_serial", hardwareSerial,
			"approval_status", existingNode.ApprovalStatus)

		// Check if node is rejected
		if existingNode.ApprovalStatus == node.ApprovalStatusRejected {
			return nil, app_errors.ErrNodeRejected
		}

		// An ADMITTED node with a BOUND fingerprint is identified by that whole
		// fingerprint, never by the serial alone. The serial
		// is printed on the case and the master token sits on every device
		// flashed from the same bundle, so "serial + master token" is not a
		// proof of being that device; before this, it rewrote the MAC, returned
		// the row, and the gRPC layer handed over the node's permanent token
		// while the real device's next node-token Connect failed its exact
		// fingerprint match forever.
		//
		// Two refusals, both answered with the uniform ErrNodeNotFound so the
		// wire sees the same "connection failed" as any other refusal:
		//
		//   - the stored serial AND MAC must equal the submitted ones exactly —
		//     the rule node-token Connect already applies. Exact serial
		//     equality is also what defeats the lookup's suffix fallback
		//     landing a short "serial" on an admitted node.
		//   - the node must not hold a live stream: the device that IS the
		//     node is connected right now, so this caller is not it. Admitting
		//     it would displace the live stream and redeliver the node's
		//     queued commands to the newcomer.
		//
		// The predicate excludes what must keep rebinding: a WebUSB placeholder
		// (approved, short serial, no MAC, AwaitingFirstConnect) and a restored
		// tombstone (full serial, no MAC, AwaitingFirstConnect). A pending node
		// keeps today's behaviour — it is handed no identity — and the
		// reimage-from-master-SD of an OFFLINE node passes both checks, since
		// the eth0 MAC comes from the SoC and survives a reimage.
		//
		// A GENUINE board swap under an existing node therefore stays an
		// operator action: Delete, then Restore — the restore sets
		// AwaitingFirstConnect, which is exactly what lets the next connect
		// rebind. Nothing here writes the fingerprint on a refusal.
		bound := existingNode.Fingerprint != nil &&
			existingNode.Fingerprint.MacAddress != "" &&
			!existingNode.AwaitingFirstConnect
		admitted := existingNode.IsApproved() && !existingNode.AwaitingFirstConnect
		if admitted && bound {
			if existingNode.Fingerprint.HardwareSerial != fingerprint.HardwareSerial ||
				existingNode.Fingerprint.MacAddress != fingerprint.MacAddress {
				s.refuseMasterTokenIdentityClaim(ctx, accountID, existingNode, fingerprint, "fingerprint_mismatch")
				return nil, app_errors.ErrNodeNotFound
			}
			if s.communicationService != nil && s.communicationService.IsNodeConnected(existingNode.ID.String()) {
				s.refuseMasterTokenIdentityClaim(ctx, accountID, existingNode, fingerprint, "node_holds_live_stream")
				return nil, app_errors.ErrNodeNotFound
			}
		}

		// An approved row still in a pre-connect status, found by its full
		// serial — a placeholder stamped with the serial the device now
		// reports. This handshake is its first connect, and the stream it
		// opens would set it online and so admit it: reconcile it here, under
		// the device cap, exactly as the short-serial reconcile below does.
		if existingNode.IsApproved() && !existingNode.IsAdmitted() {
			return s.reconcilePlaceholder(ctx, accountID, existingNode, fingerprint)
		}

		// Update fingerprint if changed (MAC address might change).
		//
		// A row awaiting its first connect (a restored tombstone) is BOUND by
		// this write, so it stops awaiting in the same write, as
		// reconcilePlaceholder and node-token Connect do. Left set, the row
		// stays outside the bound-fingerprint and live-stream refusals above,
		// and every later master-token handshake presenting the serial would
		// rebind it. Only a fingerprint with a MAC binds; without one the row
		// keeps waiting for a device that can be identified.
		bindsFirstConnect := existingNode.AwaitingFirstConnect && fingerprint.MacAddress != ""
		if existingNode.Fingerprint == nil ||
			existingNode.Fingerprint.MacAddress != fingerprint.MacAddress ||
			bindsFirstConnect {
			existingNode.Fingerprint = &fingerprint
			if bindsFirstConnect {
				existingNode.MacAddress = fingerprint.MacAddress
				existingNode.AwaitingFirstConnect = false
			}
			if err := s.nodeRepo.Update(ctx, existingNode); err != nil {
				s.logger.WarnContext(ctx, "failed to update fingerprint for existing node",
					"node_id", existingNode.ID, "error", err)
			}
		}

		return existingNode, nil
	}

	// Reconcile with a WebUSB placeholder: if this device was flashed over USB
	// (a pre-approved node stamped with the short serial, awaiting first connect)
	// adopt INTO it rather than spawning a duplicate. The placeholder stays
	// approved (WebUSB implies physical access) and binds the full serial now.
	if placeholder, perr := s.nodeRepo.GetAwaitingFirstConnectBySerialSuffix(ctx, accountID, hardwareSerial); perr == nil && placeholder != nil {
		reconciled, err := s.reconcilePlaceholder(ctx, accountID, placeholder, fingerprint)
		if err == nil || isDeviceCapRefusal(err) {
			// A refusal at the cap, or because the cap could not be
			// checked, ends the handshake: falling through would create a
			// second row for the same device.
			return reconciled, err
		}
		s.logger.WarnContext(ctx, "failed to reconcile webusb placeholder", "node_id", placeholder.ID, "error", err)
	}

	// Node doesn't exist - create new one with pending_approval status
	newNode := node.NewNodeFromMasterToken(
		accountID,
		masterTokenID,
		fingerprint,
	)

	// Auto-generate a name from the TAIL of the hardware serial: serial
	// prefixes are shared within a manufacturing batch, the tail is what
	// actually distinguishes devices.
	newNode.Name = fmt.Sprintf("Node-%s", hardwareSerial[len(hardwareSerial)-min(8, len(hardwareSerial)):])

	// Apply default credentials from master token
	newNode.DefaultUsername = defaults.DefaultUsername
	newNode.DefaultPassword = defaults.DefaultPassword
	if defaults.DefaultPasswordUnreadable {
		// Adoption must not be blocked by this — the device is already here and
		// refusing it would strand hardware. The node lands with no provisioning
		// password, and provision_connection is where that becomes loud.
		s.logger.ErrorContext(ctx, "master token's provisioning password cannot be decrypted; the adopted node gets none",
			"node_id", newNode.ID, "master_token_id", masterTokenID)
	}
	newNode.SSHKeyID = defaults.SSHKeyID
	// Fresh adoptions land in the token's target OU (existing nodes keep
	// their assignment — re-adoption never moves a node).
	newNode.OUID = defaults.OUID

	// The fleet cap applies to adoption too, and this is where it has to be:
	// a master token lets any device holding it join, so the count cap is the
	// only thing bounding how many a leaked token can add. Checked HERE rather
	// than at the top of the function, because everything above this point is
	// an EXISTING node reconnecting — refusing those would take a fleet offline
	// the moment it crossed the cap, which is the opposite of what a quota is
	// for. Only the branch that actually creates a row is gated.
	if err := s.assertNodeQuota(ctx, accountID); err != nil {
		s.logger.WarnContext(ctx, "refusing master-token adoption: account is at its node cap",
			"account_id", accountID, "hardware_serial", hardwareSerial)
		return nil, err
	}

	// The device cap sits at the same branch, for the same reason as the
	// per-account cap above: only a NEW row is gated, never a reconnect. The
	// refusal keeps its type through the wrap, so the gRPC layer can answer it
	// with a status the device can tell apart from "connection failed".
	// A device refused at the cap asks again on its own, so its refusal is
	// recorded once per MASTER TOKEN per interval (underDeviceCap), never as
	// an ERROR per handshake. Keyed on the token, not on the serial: the
	// serial is whatever the caller presents, so a holder of the token could
	// rotate it and write one audit row per invented serial into a trail
	// with no retention. There is no node id to key on yet — this branch is
	// the one that would create the row.
	if err := s.admit(ctx, newNode, audit.SystemActor(accountID), "master_token:"+masterTokenID.String()); err != nil {
		if !isDeviceCapRefusal(err) {
			s.logger.ErrorContext(ctx, "failed to create node from master token connection",
				"hardware_serial", hardwareSerial, "error", err)
		}
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	s.logger.InfoContext(ctx, "master token connection: new pending node created",
		"node_id", newNode.ID,
		"hardware_serial", hardwareSerial,
		"master_token_id", masterTokenID)

	// Send websocket notification
	s.notifyNodeCreated(*newNode)

	return newNode, nil
}

// reconcilePlaceholder binds a master-token device into the placeholder made
// for it (WebUSB or SSH adoption) instead of spawning a duplicate. The
// placeholder stays approved — a flash implies physical access — and binding
// it is its first connect: for a placeholder that is not yet admitted that
// is the ADMISSION, under the device cap. Refused, the row is untouched.
func (s *Service) reconcilePlaceholder(ctx context.Context, accountID uuid.UUID, placeholder *node.Node, fingerprint node.HardwareFingerprint) (*node.Node, error) {
	wasAdmitted := placeholder.IsAdmitted()
	placeholder.Fingerprint = &fingerprint
	placeholder.MacAddress = fingerprint.MacAddress
	placeholder.AwaitingFirstConnect = false
	placeholder.Status = node.StatusOnline
	placeholder.UpdatedAt = time.Now()
	var err error
	if !wasAdmitted && placeholder.IsAdmitted() {
		err = s.admitTransition(ctx, placeholder, audit.SystemActor(accountID))
	} else {
		err = s.nodeRepo.Update(ctx, placeholder)
	}
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "master token connection: reconciled into placeholder",
		"node_id", placeholder.ID, "hardware_serial", fingerprint.HardwareSerial)
	s.notifyNodeUpdated(*placeholder)
	return placeholder, nil
}

// refuseMasterTokenIdentityClaim records a master-token handshake that named
// an admitted node it could not prove to be. The log line is enough for the
// person reading logs; the anomaly is what reaches the operator who is not.
// Best-effort, like every anomaly: the refusal stands whether or not it was
// recorded. Nothing here touches the node row.
func (s *Service) refuseMasterTokenIdentityClaim(ctx context.Context, accountID uuid.UUID, existing *node.Node, submitted node.HardwareFingerprint, reason string) {
	s.logger.WarnContext(ctx, "refused master-token connection claiming an admitted node's identity",
		"node_id", existing.ID, "account_id", accountID, "reason", reason,
		"hardware_serial", submitted.HardwareSerial, "stored_serial", existing.Fingerprint.HardwareSerial,
		"mac_address", submitted.MacAddress, "stored_mac_address", existing.Fingerprint.MacAddress)
	s.recordAnomaly(ctx, instance_anomaly.New(
		instance_anomaly.KindMasterTokenIdentityMismatch,
		instance_anomaly.MasterTokenIdentityMismatchKey(accountID, existing.ID),
		instance_anomaly.SeverityWarning,
		"A device presented this account's master token and the hardware serial of an admitted node, but was not that node — its fingerprint differed, or the node was connected at the time. The connection was refused and the node was left unchanged. If a board was genuinely replaced under this node, delete the node and restore it so the new hardware can bind on its next connect.",
		map[string]interface{}{
			"node_id":                existing.ID.String(),
			"account_id":             accountID.String(),
			"reason":                 reason,
			"hardware_serial":        submitted.HardwareSerial,
			"stored_hardware_serial": existing.Fingerprint.HardwareSerial,
			"mac_address":            submitted.MacAddress,
			"stored_mac_address":     existing.Fingerprint.MacAddress,
		},
		&accountID,
	))
}

// requireEEPROMCapability refuses an update_boot_order that carries no
// per-serial download capability.
//
// The capability is minted only after eeprom_provision has baked and SIGNED an
// image for this device's serial, so its presence is the proof that the bake
// happened — which makes this the enforcement of "the per-serial bake is
// mandatory". Without it the agent cannot fetch that image and falls back to a
// SHARED one, overwriting the device's embedded HTTP_PATH and adoption token: a
// green run that costs the node its ability to netboot at all.
//
// The slug stays dispatchable from the generic command API and from a playbook
// block; both are refused here, with the route that does it properly.
func requireEEPROMCapability(nodeID uuid.UUID, pkgExp, pkgSig string) error {
	if strings.TrimSpace(pkgExp) != "" && strings.TrimSpace(pkgSig) != "" {
		return nil
	}
	return fmt.Errorf(
		"update_boot_order cannot be dispatched directly: without a per-device EEPROM image the device loses "+
			"its ability to boot from the network. Use POST /api/v1/nodes/%s/provision-eeprom with the desired "+
			"boot_order, which prepares and signs that image first.", nodeID)
}
