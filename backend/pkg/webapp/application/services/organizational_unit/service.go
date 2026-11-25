package organizational_unit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_policy_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/organizational_unit_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/organizational_unit_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

// authzInvalidator drops cached authorization for a member so revoking an
// OU-scoped binding takes effect immediately rather than within the cache TTL.
type authzInvalidator interface {
	Invalidate(accountID, userID uuid.UUID)
}

// nodeScopeUpdater repoints a CONNECTED node's organizational unit on the live
// stream registry. The gRPC layer captures the unit once at registration so the
// per-sample metrics push needs no database read; without this call a node
// moved between units kept feeding the old unit's viewers until it reconnected.
type nodeScopeUpdater interface {
	UpdateNodeOU(nodeID string, ouID *uuid.UUID)
}

// Service implements organizational-unit use cases plus node membership/policy.
type Service struct {
	ouRepo            repository.OrganizationalUnitRepository
	ouMutationRepo    repository.OUMutationRepository
	nodeRepo          repository.NodeRepository
	sshKeyRepo        repository.SSHKeyRepository
	secretRepo        repository.SecretRepository
	attachmentRepo    repository.SecretAttachmentRepository
	accountPolicyRepo repository.AccountPolicyRepository
	imageRepo         repository.ImageRepository
	bindingRepo       repository.RoleBindingRepository
	inviteRepo        repository.AccountInviteRepository
	masterTokenRepo   repository.MasterTokenRepository
	templateRepo      repository.WorkflowTemplateRepository
	accessEntryRepo   repository.AccessEntryRepository
	accessApplier     AccessApplier
	nodeNotifier nodeChangeNotifier
	invalidator       authzInvalidator
	scopeUpdater      nodeScopeUpdater
	deviceProfiles    *device_profile.Catalog
	audit             *audit.Recorder
	logger            *slog.Logger
}

// AccessApplier dispatches an apply_access_config to one node, so a change that
// alters a node's effective access config reaches the device instead of only the
// database. Implemented by the node service and wired via SetAccessApplier,
// because the node service already depends on this one.
type AccessApplier interface {
	EnqueueApplyAccessConfig(ctx context.Context, accountID, nodeID uuid.UUID) error
}

// SetAccessApplier wires the dispatcher used to push access changes to devices.
// Optional: without it the mutations are stored and not pushed, which is the
// pre-existing behaviour.
func (s *Service) SetAccessApplier(a AccessApplier) { s.accessApplier = a }

// nodeChangeNotifier tells open browsers that a node's record changed. The
// unit, a policy override or their reset are columns of the node, and a list
// that is not told keeps showing the old values until a reload.
type nodeChangeNotifier interface {
	NotifyNodeChanged(ctx context.Context, accountID, nodeID uuid.UUID)
}

// SetNodeChangeNotifier wires the node_updated push for the node writes made
// here (AssignNode, SetNodePolicy, ResetNodePolicyOverrides).
func (s *Service) SetNodeChangeNotifier(n nodeChangeNotifier) { s.nodeNotifier = n }

func (s *Service) notifyNodeChanged(ctx context.Context, accountID, nodeID uuid.UUID) {
	if s.nodeNotifier != nil {
		s.nodeNotifier.NotifyNodeChanged(ctx, accountID, nodeID)
	}
}

// SetAccessEntryRepo wires the access-entry repository for the OU-delete
// cascade (optional; when unset, access entries scoped to a deleted OU are left
// as dangling references, no worse than the pre-existing behaviour).
func (s *Service) SetAccessEntryRepo(repo repository.AccessEntryRepository) {
	s.accessEntryRepo = repo
}

// SetAuthzInvalidator wires the authz cache invalidator (optional; when unset
// an OU-scoped binding revocation takes effect within the cache TTL instead of
// immediately).
func (s *Service) SetAuthzInvalidator(inv authzInvalidator) {
	s.invalidator = inv
}

// SetNodeScopeUpdater wires the live-stream scope refresh (optional; when unset
// a connected node's pushed telemetry keeps its old unit's scope until the node
// reconnects).
func (s *Service) SetNodeScopeUpdater(u nodeScopeUpdater) {
	s.scopeUpdater = u
}

// SetDeviceProfileCatalog wires the loaded device-profile registry. Optional
// by wiring convention, but fail-closed in behaviour: without it any request
// that sets or resolves a device profile is refused rather than stored
// unvalidated.
func (s *Service) SetDeviceProfileCatalog(c *device_profile.Catalog) {
	s.deviceProfiles = c
}

// invalidate drops a member's cached authorization if an invalidator is set.
func (s *Service) invalidate(accountID, userID uuid.UUID) {
	if s.invalidator != nil {
		s.invalidator.Invalidate(accountID, userID)
	}
}

// NewService wires the OU service. All repositories are required.
func NewService(ouRepo repository.OrganizationalUnitRepository, ouMutationRepo repository.OUMutationRepository, nodeRepo repository.NodeRepository, sshKeyRepo repository.SSHKeyRepository, secretRepo repository.SecretRepository, attachmentRepo repository.SecretAttachmentRepository, accountPolicyRepo repository.AccountPolicyRepository, imageRepo repository.ImageRepository, bindingRepo repository.RoleBindingRepository, inviteRepo repository.AccountInviteRepository, masterTokenRepo repository.MasterTokenRepository, templateRepo repository.WorkflowTemplateRepository, auditRecorder *audit.Recorder, logger *slog.Logger) *Service {
	if ouRepo == nil {
		panic("organizational_unit.Service requires an OU repository")
	}
	if ouMutationRepo == nil {
		// Required, not optional: Delete's serialization guarantee lives in it,
		// and an optional dependency that silently falls back to the racy path
		// is worse than none.
		panic("organizational_unit.Service requires an OU mutation repository")
	}
	if nodeRepo == nil {
		panic("organizational_unit.Service requires a node repository")
	}
	if sshKeyRepo == nil {
		panic("organizational_unit.Service requires an SSH key repository")
	}
	if secretRepo == nil {
		panic("organizational_unit.Service requires a secret repository")
	}
	if attachmentRepo == nil {
		panic("organizational_unit.Service requires a secret attachment repository")
	}
	if accountPolicyRepo == nil {
		panic("organizational_unit.Service requires an account policy repository")
	}
	if imageRepo == nil {
		panic("organizational_unit.Service requires an image repository")
	}
	if bindingRepo == nil {
		panic("organizational_unit.Service requires a role binding repository")
	}
	if inviteRepo == nil {
		panic("organizational_unit.Service requires an account invite repository")
	}
	if masterTokenRepo == nil {
		panic("organizational_unit.Service requires a master token repository")
	}
	if templateRepo == nil {
		panic("organizational_unit.Service requires a workflow template repository")
	}
	if auditRecorder == nil {
		// Required, not optional: an OU update can dispatch apply_access_config
		// to the fleet, and a fleet-wide push with no audit row is the gap this
		// parameter closes.
		panic("organizational_unit.Service requires an audit recorder")
	}
	return &Service{
		ouRepo:            ouRepo,
		ouMutationRepo:    ouMutationRepo,
		nodeRepo:          nodeRepo,
		sshKeyRepo:        sshKeyRepo,
		secretRepo:        secretRepo,
		attachmentRepo:    attachmentRepo,
		accountPolicyRepo: accountPolicyRepo,
		imageRepo:         imageRepo,
		bindingRepo:       bindingRepo,
		inviteRepo:        inviteRepo,
		masterTokenRepo:   masterTokenRepo,
		templateRepo:      templateRepo,
		audit:             auditRecorder,
		logger:            logger.With("service", "organizational_unit"),
	}
}

// validateProvisioningRefs refuses a provisioning reference that does not
// resolve INSIDE the caller's account. Without it a foreign or fabricated
// UUID is stored verbatim; the resolve path degrades it to "no key" with a
// warning, so the symptom is provisioning that silently grants nothing —
// refusing at write time is where the operator can still see the mistake.
// The image repo may be absent in narrow test fixtures; production wiring
// always provides it (NewService panics otherwise).
func (s *Service) validateProvisioningRefs(ctx context.Context, accountID uuid.UUID, sshKeyID, osImageID *uuid.UUID) error {
	if sshKeyID != nil && s.sshKeyRepo != nil {
		if _, err := s.sshKeyRepo.GetByID(ctx, accountID, *sshKeyID); err != nil {
			err = fmt.Errorf("invalid SSH key ID: %w", err)
			if errors.Is(err, app_errors.ErrSSHKeyNotFound) {
				return app_errors.InvalidRequest("invalid_ssh_key_id", err)
			}
			return err
		}
	}
	if osImageID != nil && s.imageRepo != nil {
		if _, err := s.imageRepo.GetByID(ctx, accountID, *osImageID); err != nil {
			err = fmt.Errorf("invalid OS image ID: %w", err)
			if errors.Is(err, app_errors.ErrImageNotFound) {
				return app_errors.InvalidRequest("invalid_os_image_id", err)
			}
			return err
		}
	}
	return nil
}

// nextSiblingPosition is the position a unit created without one takes: one
// past the highest among the units under the same parent (top-level units are
// siblings of each other), 0 for the first. Without it every such unit stored
// 0 and their order was undefined. Two creates racing may still share a
// position; ordering then falls back to the list's tie-break, as before.
func (s *Service) nextSiblingPosition(ctx context.Context, accountID uuid.UUID, parentID *uuid.UUID) (int, error) {
	all, err := s.ouRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return 0, err
	}
	next := 0
	for i := range all {
		sibling := &all[i]
		sameParent := (parentID == nil && sibling.ParentID == nil) ||
			(parentID != nil && sibling.ParentID != nil && *sibling.ParentID == *parentID)
		if sameParent && sibling.Position >= next {
			next = sibling.Position + 1
		}
	}
	return next, nil
}

// Create creates a unit, enforcing the two-level hierarchy.
func (s *Service) Create(ctx context.Context, cmd organizational_unit_create.OrganizationalUnitCreateCommand) (*organizational_unit.OrganizationalUnit, error) {
	if cmd.ParentID != nil {
		parent, err := s.ouRepo.GetByID(ctx, cmd.AuthContext.AccountID, *cmd.ParentID)
		if err != nil {
			return nil, err
		}
		if !parent.IsRoot() {
			return nil, organizational_unit.ErrNestingTooDeep
		}
	}

	if err := refuseInvalidPolicyFlags(&cmd.NetworkFirst, &cmd.BootToOS, &cmd.SSHPasswordAuth); err != nil {
		return nil, err
	}
	ou, err := organizational_unit.NewOrganizationalUnit(cmd.AuthContext.AccountID, cmd.Name, cmd.ParentID)
	if err != nil {
		return nil, err
	}
	ou.NetworkFirst = cmd.NetworkFirst.ToBool()
	ou.BootToOS = cmd.BootToOS.ToBool()
	ou.SSHPasswordAuth = cmd.SSHPasswordAuth.ToBool()
	ou.DefaultUsername = strToPtr(cmd.DefaultUsername)
	ou.DefaultPassword = strToPtr(cmd.DefaultPassword)
	sshID, err := uuidStrToPtr("ssh_key_id", cmd.SSHKeyID)
	if err != nil {
		return nil, err
	}
	ou.SSHKeyID = sshID
	osID, err := uuidStrToPtr("os_image_id", cmd.OSImageID)
	if err != nil {
		return nil, err
	}
	ou.OSImageID = osID
	if cmd.Position != nil {
		ou.Position = *cmd.Position
	} else {
		next, err := s.nextSiblingPosition(ctx, cmd.AuthContext.AccountID, cmd.ParentID)
		if err != nil {
			return nil, err
		}
		ou.Position = next
	}
	if err := s.applyPolicyBlocks(ou, cmd.DeviceProfile, cmd.LEDConfig, cmd.WatchdogConfig); err != nil {
		return nil, err
	}
	if err := s.validateProvisioningRefs(ctx, cmd.AuthContext.AccountID, ou.SSHKeyID, ou.OSImageID); err != nil {
		return nil, err
	}

	if err := s.ouRepo.Create(ctx, ou); err != nil {
		s.logger.ErrorContext(ctx, "failed to create organizational unit", "error", err)
		return nil, err
	}
	s.logger.InfoContext(ctx, "organizational unit created", "ou_id", ou.ID, "name", ou.Name)
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionOUCreate, audit_event.TargetOU, ou.ID.String(), ouAuditDetail(ou))
	return ou, nil
}

// ouAuditDetail names the unit and its parent — names and ids only, never a
// policy value (audit_events is on no rewrap list).
func ouAuditDetail(ou *organizational_unit.OrganizationalUnit) string {
	parent := "root"
	if ou.ParentID != nil {
		parent = ou.ParentID.String()
	}
	return fmt.Sprintf("name=%q parent=%s", ou.Name, parent)
}

// Update mutates name and/or policies; nil fields are left unchanged.
func (s *Service) Update(ctx context.Context, cmd organizational_unit_update.OrganizationalUnitUpdateCommand) (*organizational_unit.OrganizationalUnit, error) {
	if err := refuseInvalidPolicyFlags(cmd.NetworkFirst, cmd.BootToOS, cmd.SSHPasswordAuth); err != nil {
		return nil, err
	}
	ou, err := s.ouRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}
	// Captured before ANY field is applied, so the pushes at the end of this
	// method are driven by what actually changed. `parentWas` in particular has
	// to be read before `applyReparent`, which mutates `ou.ParentID` in place —
	// reading it further down captures the new value and the comparison is
	// always equal.
	passwordAuthWas := ou.SSHPasswordAuth
	parentWas := ou.ParentID
	if cmd.Name != nil {
		ou.Name = *cmd.Name
	}
	if cmd.ParentID != nil {
		if err := s.applyReparent(ctx, ou, *cmd.ParentID); err != nil {
			return nil, err
		}
	}
	if cmd.Position != nil {
		ou.Position = *cmd.Position
	}
	if cmd.NetworkFirst != nil {
		ou.NetworkFirst = cmd.NetworkFirst.ToBool()
	}
	if cmd.BootToOS != nil {
		ou.BootToOS = cmd.BootToOS.ToBool()
	}
	// Remembered before the write so the push below is driven by an actual
	// change, not by the field having been present in the request. A full-document
	// PUT re-sends every field it holds, so "was it supplied" would push the whole
	// fleet on a rename.
	if cmd.SSHPasswordAuth != nil {
		ou.SSHPasswordAuth = cmd.SSHPasswordAuth.ToBool()
	}
	if cmd.DefaultUsername != nil {
		ou.DefaultUsername = strToPtr(*cmd.DefaultUsername)
	}
	if cmd.DefaultPassword != nil {
		ou.DefaultPassword = strToPtr(*cmd.DefaultPassword)
		// Explicitly supplied, so the repository's preserve-unreadable guard
		// must stand down or an explicit clear no-ops.
		ou.DefaultPasswordUnreadable = false
	}
	if cmd.SSHKeyID != nil {
		id, err := uuidStrToPtr("ssh_key_id", *cmd.SSHKeyID)
		if err != nil {
			return nil, err
		}
		ou.SSHKeyID = id
	}
	if cmd.OSImageID != nil {
		id, err := uuidStrToPtr("os_image_id", *cmd.OSImageID)
		if err != nil {
			return nil, err
		}
		ou.OSImageID = id
	}
	if err := s.applyPolicyBlocks(ou, cmd.DeviceProfile, cmd.LEDConfig, cmd.WatchdogConfig); err != nil {
		return nil, err
	}
	// Only checks refs the request supplied — a stored ref that has since gone
	// stale degrades at resolve time and must not wedge an unrelated rename.
	{
		var checkSSH, checkImg *uuid.UUID
		if cmd.SSHKeyID != nil {
			checkSSH = ou.SSHKeyID
		}
		if cmd.OSImageID != nil {
			checkImg = ou.OSImageID
		}
		if err := s.validateProvisioningRefs(ctx, cmd.AuthContext.AccountID, checkSSH, checkImg); err != nil {
			return nil, err
		}
	}
	if err := s.ouRepo.Update(ctx, ou); err != nil {
		s.logger.ErrorContext(ctx, "failed to update organizational unit", "error", err, "ou_id", cmd.ID)
		return nil, err
	}
	// ssh_password_auth is what apply_access_config writes into sshd's
	// `PasswordAuthentication`, so changing it here and writing only the row left
	// password logins open on every node the unit governs, while the UI reported
	// them closed. Nothing heals it on a timer: the applied-generation self-heal
	// runs from UpdateSystemInfo, which fires on connect — so on a node that
	// stays up the change never lands.
	pushReason, pushedNodes := "", 0
	if !samePolicyFlag(passwordAuthWas, ou.SSHPasswordAuth) {
		affected := s.nodesResolvingPasswordAuthAt(ctx, cmd.AuthContext.AccountID, &ou.ID)
		s.pushAccessToFleet(ctx, cmd.AuthContext.AccountID, affected,
			"ssh_password_auth changed on an organizational unit")
		pushReason, pushedNodes = "ssh_password_auth", len(affected)
	} else if !sameParent(parentWas, ou.ParentID) {
		// Moving a unit changes which ROOT its nodes inherit from, which is the
		// same event as moving a node between units — and `AssignNode` pushes
		// for exactly that. Reparenting is reachable by dragging a unit in the
		// rail, and `applyReparent` requires the unit to be a LEAF but never
		// requires it to be EMPTY, so a unit full of nodes moves in one gesture.
		//
		// The affected set is every node in this unit, not the password-auth
		// resolver's set: access ENTRIES resolve as a UNION across node -> OU ->
		// root -> account, so the old root's users keep their keys, sudo and
		// password on the device even when this unit answers the flag itself.
		// The unit is a leaf, so "its nodes" is the whole set.
		//
		// `else if`: when both changed, the flag branch has already pushed, and
		// its set is a superset for a leaf unit.
		affected := s.nodesInUnit(ctx, cmd.AuthContext.AccountID, ou.ID)
		s.pushAccessToFleet(ctx, cmd.AuthContext.AccountID, affected,
			"organizational unit moved to a different parent")
		pushReason, pushedNodes = "reparent", len(affected)
	}
	detail := fmt.Sprintf("%s fields=[%s]", ouAuditDetail(ou), strings.Join(suppliedUpdateFields(cmd), " "))
	if pushReason != "" {
		detail += fmt.Sprintf(" push=%s nodes=%d", pushReason, pushedNodes)
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionOUUpdate, audit_event.TargetOU, ou.ID.String(), detail)
	return ou, nil
}

// applyPolicyBlocks sets the unit's device profile, LED and watchdog
// declarations from a create or an update, with the same validation and the
// same typed refusals on both; a nil block is left as it is.
func (s *Service) applyPolicyBlocks(ou *organizational_unit.OrganizationalUnit, deviceProfile *device_profile.Equipage, ledConfig *node.LEDConfig, watchdogConfig *node.WatchdogConfig) error {
	if deviceProfile != nil {
		eq, err := s.checkedEquipage(deviceProfile)
		if err != nil {
			return err
		}
		ou.DeviceProfile = eq
	}
	if ledConfig != nil {
		// Domain bounds only — the board gates (stealth circuit, digital chain
		// length) are per-DEVICE facts a mixed-board unit cannot answer; the
		// node's own LED apply is where they hold.
		if ledConfig.IsZero() {
			ou.LEDConfig = nil
		} else {
			if err := ledConfig.Validate(); err != nil {
				return app_errors.InvalidRequest("invalid_led_config", err)
			}
			cfg := *ledConfig
			ou.LEDConfig = &cfg
		}
	}
	if watchdogConfig != nil {
		// Domain bounds only, like the LED arm; the device's own pre-arm
		// verification still runs at every per-node apply. A disabled config
		// clears the declaration (the command's contract), and the save
		// dispatches nothing — nothing arms a device without a human present.
		if !watchdogConfig.Enabled {
			ou.WatchdogConfig = nil
		} else {
			if err := watchdogConfig.Validate(); err != nil {
				return app_errors.InvalidRequest("invalid_watchdog_config", err)
			}
			cfg := *watchdogConfig
			ou.WatchdogConfig = &cfg
		}
	}
	return nil
}

// suppliedUpdateFields lists the field NAMES the update carried — what the
// caller touched, never any value. A full-document PUT lists most of them on
// every save; the push= clause is what marks the consequential edits.
func suppliedUpdateFields(cmd organizational_unit_update.OrganizationalUnitUpdateCommand) []string {
	fields := make([]string, 0, 11)
	add := func(name string, supplied bool) {
		if supplied {
			fields = append(fields, name)
		}
	}
	add("name", cmd.Name != nil)
	add("parent", cmd.ParentID != nil)
	add("position", cmd.Position != nil)
	add("network_first", cmd.NetworkFirst != nil)
	add("boot_to_os", cmd.BootToOS != nil)
	add("ssh_password_auth", cmd.SSHPasswordAuth != nil)
	add("default_username", cmd.DefaultUsername != nil)
	add("default_password", cmd.DefaultPassword != nil)
	add("ssh_key", cmd.SSHKeyID != nil)
	add("os_image", cmd.OSImageID != nil)
	add("device_profile", cmd.DeviceProfile != nil)
	add("led_config", cmd.LEDConfig != nil)
	add("watchdog_config", cmd.WatchdogConfig != nil)
	return fields
}

// suppliedPolicyFields is suppliedUpdateFields for the account policy command.
func suppliedPolicyFields(cmd account_policy_update.AccountPolicyUpdateCommand) []string {
	fields := make([]string, 0, 8)
	add := func(name string, supplied bool) {
		if supplied {
			fields = append(fields, name)
		}
	}
	add("network_first", cmd.NetworkFirst != nil)
	add("boot_to_os", cmd.BootToOS != nil)
	add("ssh_password_auth", cmd.SSHPasswordAuth != nil)
	add("default_username", cmd.DefaultUsername != nil)
	add("default_password", cmd.DefaultPassword != nil)
	add("ssh_key", cmd.SSHKeyID != nil)
	add("os_image", cmd.OSImageID != nil)
	add("device_profile", cmd.DeviceProfile != nil)
	add("led_config", cmd.LEDConfig != nil)
	add("watchdog_config", cmd.WatchdogConfig != nil)
	return fields
}

// sameParent compares two optional parent references. Distinct pointers to the
// same id are the same parent; nil is "a root unit", and nil-to-set is a real
// move.
func sameParent(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// nodesInUnit returns the account's nodes directly assigned to one unit.
func (s *Service) nodesInUnit(ctx context.Context, accountID, ouID uuid.UUID) []uuid.UUID {
	nodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to list nodes for an access push", "error", err, "account_id", accountID)
		return nil
	}
	var out []uuid.UUID
	for i := range nodes {
		if nodes[i].OUID != nil && *nodes[i].OUID == ouID {
			out = append(out, nodes[i].ID)
		}
	}
	return out
}

// samePolicyFlag compares two tri-state flags, where nil means "inherit". Worth
// a named function because the interesting cases are the ones a `==` on pointers
// gets wrong in both directions: two distinct pointers to the same value are the
// same policy, and nil-to-false is a real change (from inheriting to an explicit
// "off").
func samePolicyFlag(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// applyReparent moves a unit to a new parent, preserving the two-level limit.
// parentID: "" makes the unit a root; a UUID nests it under that root unit.
func (s *Service) applyReparent(ctx context.Context, ou *organizational_unit.OrganizationalUnit, parentID string) error {
	if parentID == "" {
		ou.ParentID = nil
		return nil
	}
	pid, err := uuid.Parse(parentID)
	if err != nil {
		return err
	}
	if pid == ou.ID {
		return organizational_unit.ErrCircularParent
	}
	parent, err := s.ouRepo.GetByID(ctx, ou.AccountID, pid)
	if err != nil {
		return err
	}
	// The new parent must itself be a root unit (only two levels allowed).
	if !parent.IsRoot() {
		return organizational_unit.ErrParentIsNotRoot
	}
	// The unit being moved must be a leaf; nesting a unit that has children
	// would create a third level.
	childCount, err := s.ouRepo.CountChildren(ctx, ou.AccountID, ou.ID)
	if err != nil {
		return err
	}
	if childCount > 0 {
		return organizational_unit.ErrNestingTooDeep
	}
	ou.ParentID = &pid
	return nil
}

// Delete removes a unit, refusing if it still has nested units, member nodes or
// playbooks scoped to it, then cascades the OU-scoped references that would
// otherwise dangle (role bindings, pending invites, secret attachments and
// adoption tokens).
func (s *Service) Delete(ctx context.Context, cmd organizational_unit_delete.OrganizationalUnitDeleteCommand) error {
	accountID := cmd.AuthContext.AccountID

	// Read the name before the row is gone — best-effort, for the audit detail
	// only; the delete itself never depends on it.
	deletedName := ""
	if ou, err := s.ouRepo.GetByID(ctx, accountID, cmd.ID); err == nil {
		deletedName = ou.Name
	}

	// Collect the members holding an OU-scoped binding before deleting them, so
	// their cached authorization can be dropped immediately afterwards.
	affected := s.membersScopedTo(ctx, accountID, cmd.ID)

	// The emptiness guards used to live here, three reads followed by a delete,
	// which is the classic check-then-act race: a node assigned into the unit
	// between the check and the delete kept an `ou_id` pointing at a row that no
	// longer exists. They now run inside the deleting transaction with the
	// unit's row locked, and the assignment path takes a share lock on the same
	// row, so the two orders are serialized rather than merely usually-ordered.
	//
	// The OU row still goes first relative to the CASCADE: a partial cascade
	// failure leaves dangling references, whereas cleaning up first and failing
	// to delete would strip access from a unit that still exists.
	if err := s.ouMutationRepo.DeleteIfUnreferenced(ctx, accountID, cmd.ID); err != nil {
		s.logger.ErrorContext(ctx, "failed to delete organizational unit", "error", err, "ou_id", cmd.ID)
		return err
	}

	s.cascadeDelete(ctx, accountID, cmd.ID)

	for _, userID := range affected {
		s.invalidate(accountID, userID)
	}
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, cmd.AuthContext.UserID),
		audit_event.ActionOUDelete, audit_event.TargetOU, cmd.ID.String(),
		fmt.Sprintf("name=%q", deletedName))
	return nil
}

// membersScopedTo lists the users holding a binding scoped to the unit. A
// lookup failure is not fatal — it only costs cache-TTL-delayed revocation.
func (s *Service) membersScopedTo(ctx context.Context, accountID, ouID uuid.UUID) []uuid.UUID {
	bindings, err := s.bindingRepo.ListByAccountID(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to list bindings before OU delete", "error", err, "ou_id", ouID)
		return nil
	}
	seen := make(map[uuid.UUID]bool)
	users := make([]uuid.UUID, 0)
	for i := range bindings {
		if bindings[i].OUID != nil && *bindings[i].OUID == ouID && !seen[bindings[i].UserID] {
			seen[bindings[i].UserID] = true
			users = append(users, bindings[i].UserID)
		}
	}
	return users
}

// cascadeDelete drops every OU-scoped reference left behind by a deleted unit.
// The unit itself is already gone, so a failure here is logged rather than
// returned: the caller's operation did succeed, and each leftover is no worse
// than the pre-existing dangling-reference state.
func (s *Service) cascadeDelete(ctx context.Context, accountID, ouID uuid.UUID) {
	cleanups := []struct {
		what string
		run  func() error
	}{
		{"role bindings", func() error { return s.bindingRepo.DeleteByOUID(ctx, accountID, ouID) }},
		{"pending invites", func() error { return s.inviteRepo.DeleteByOUID(ctx, accountID, ouID) }},
		{"secret attachments", func() error { return s.attachmentRepo.DeleteByOUID(ctx, accountID, ouID) }},
		{"master tokens", func() error { return s.masterTokenRepo.DeleteByOUID(ctx, accountID, ouID) }},
		{"access entries", func() error {
			if s.accessEntryRepo == nil {
				return nil
			}
			return s.accessEntryRepo.DeleteByOUID(ctx, accountID, ouID)
		}},
		// Defensive: the delete guard counts this table and refuses a unit any
		// playbook is scoped to, so this clears only a reference that was
		// already dangling. It matters because validateOUs then refuses every
		// save of that playbook, with no gesture able to drop the ghost unit.
		{"playbook scopes", func() error {
			return s.templateRepo.DeleteScopeByOUID(ctx, accountID, ouID)
		}},
	}
	for _, c := range cleanups {
		if err := c.run(); err != nil {
			s.logger.ErrorContext(ctx, "OU delete cascade left dangling references",
				"error", err, "ou_id", ouID, "account_id", accountID, "kind", c.what)
		}
	}
}

// GetByID fetches one unit.
func (s *Service) GetByID(ctx context.Context, query organizational_unit_get.OrganizationalUnitGetQuery) (*organizational_unit.OrganizationalUnit, error) {
	return s.ouRepo.GetByID(ctx, query.AuthContext.AccountID, query.ID)
}

// GetAllByAccountID lists units for the account.
func (s *Service) GetAllByAccountID(ctx context.Context, query organizational_unit_list.OrganizationalUnitListQuery) ([]organizational_unit.OrganizationalUnit, error) {
	return s.ouRepo.GetAllByAccountID(ctx, query.AuthContext.AccountID)
}

// AssignNode sets (or clears, with nil ouID) a node's organizational unit.
//
// "Does the unit exist?" and "write the node" are one transaction holding a
// share lock on the unit, so a concurrent delete cannot slip between them — see
// OUMutationRepository. It also writes the single column rather than saving the
// whole row back.
//
// The move is recorded under actor: it swaps every unit-level access entry the
// device carries, so the trail must say who moved it and from where. A platform
// caller passes audit.SystemActor.
func (s *Service) AssignNode(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, ouID *uuid.UUID) error {
	// The previous unit, read for the audit detail only; the write below is
	// what decides whether the node exists.
	from := "none"
	if prev, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err == nil && prev != nil && prev.OUID != nil {
		from = prev.OUID.String()
	}
	if err := s.ouMutationRepo.AssignNodeOU(ctx, accountID, nodeID, ouID); err != nil {
		return err
	}
	to := "none"
	if ouID != nil {
		to = ouID.String()
	}
	s.audit.RecordDetail(ctx, actor, audit_event.ActionNodeAssignOU, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("from=%s to=%s", from, to))
	// The live stream registry holds its own copy of the unit (captured at
	// connect for the per-sample metrics push); repoint it, or a connected
	// node keeps feeding the OLD unit's viewers until it reconnects. Best
	// effort by shape — the updater has no failure mode and a node without a
	// stream is a no-op.
	if s.scopeUpdater != nil {
		s.scopeUpdater.UpdateNodeOU(nodeID.String(), ouID)
	}
	// A node's effective access set is a union across node → OU → root →
	// account, so moving it between units swaps every OU-level user it has.
	// Writing the column alone left the OLD unit's users logged in on the
	// device — with keys, sudo and, where the entry has one, a password — until
	// something else happened to push. Nothing does on a timer: the
	// applied-generation self-heal runs from UpdateSystemInfo, which fires on
	// connect. A node that stays up keeps the access of a unit it is no longer in.
	s.pushAccess(ctx, accountID, nodeID, "node moved between organizational units")
	s.notifyNodeChanged(ctx, accountID, nodeID)
	return nil
}

// pushAccess enqueues an apply_access_config for one node after a change that
// alters its effective access config.
//
// Best-effort and non-fatal, for the same reasons the access service's own
// propagation is: the command is queueable (it waits for an offline node) and
// superseding (a newer apply retires an unsent older one), so late and
// out-of-order delivery is the designed behaviour — and refusing the operator's
// edit because its follow-up dispatch failed would be worse than the delay.
func (s *Service) pushAccess(ctx context.Context, accountID, nodeID uuid.UUID, reason string) {
	if s.accessApplier == nil {
		return
	}
	if err := s.accessApplier.EnqueueApplyAccessConfig(ctx, accountID, nodeID); err != nil {
		s.logger.WarnContext(ctx, "failed to push an access change to the device",
			"reason", reason, "node_id", nodeID, "account_id", accountID, "error", err)
	}
}

// fleetDispatchTimeout bounds a detached propagation. Generous, because it is a
// sequential walk of an account's nodes and nobody is waiting on it, but finite:
// a goroutine per policy edit that could outlive the process is not a thing to
// leave running. Mirrors the access service's own constant.
const fleetDispatchTimeout = 10 * time.Minute

// pushAccessToFleet enqueues apply_access_config for many nodes OFF the request.
//
// An OU-level or account-level flag can reach every node in the account, and at
// 300 nodes an inline walk is hundreds of sequential queries against a
// 60-connection pool — past any proxy read timeout, so the operator's experience
// of closing password auth would be a 504 on a change that had partially
// happened. Detaching is safe because of what this command is:
// apply_access_config is queueable (it waits for an offline node) and superseding
// (a newer apply retires an unsent older one), so late and out-of-order delivery
// is the designed behaviour.
//
// The context is detached with context.WithoutCancel: the request's context dies
// the moment the response is written, and inheriting it would cancel the
// propagation at almost exactly the point an inline version would have stopped.
func (s *Service) pushAccessToFleet(ctx context.Context, accountID uuid.UUID, nodeIDs []uuid.UUID, reason string) {
	if s.accessApplier == nil || len(nodeIDs) == 0 {
		return
	}
	s.logger.InfoContext(ctx, "pushing an access change to the fleet",
		"reason", reason, "account_id", accountID, "node_count", len(nodeIDs))
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), fleetDispatchTimeout)
	go func() {
		defer cancel()
		dispatched, failed := 0, 0
		for _, nodeID := range nodeIDs {
			if detached.Err() != nil {
				s.logger.Error("access propagation abandoned before finishing",
					"reason", reason, "account_id", accountID, "dispatched", dispatched,
					"failed", failed, "remaining", len(nodeIDs)-dispatched-failed, "error", detached.Err())
				return
			}
			if err := s.accessApplier.EnqueueApplyAccessConfig(detached, accountID, nodeID); err != nil {
				failed++
				s.logger.Warn("failed to push an access change to the device",
					"reason", reason, "node_id", nodeID, "account_id", accountID, "error", err)
				continue
			}
			dispatched++
		}
		s.logger.Info("access change pushed to the fleet",
			"reason", reason, "account_id", accountID, "dispatched", dispatched, "failed", failed)
	}()
}

// nodesResolvingPasswordAuthAt returns the nodes whose ssh_password_auth
// resolution REACHES the given level — an organizational unit, or the account
// when ouID is nil.
//
// This is deliberately NOT "the nodes whose ou_id is X", which is what makes it
// more than a one-line call to the node-level push. The flag is tri-state and
// inherited node -> OU -> root OU -> account: a node with its own value ignores
// every level above it, and a child unit's nodes follow the ROOT when the child
// itself is on inherit. So an edit to a root unit reaches nodes that are not in
// it, and misses nodes that are.
//
// "Reaches", not "is decided at", and the difference is a real trap: if
// the answer depended on the edited level currently HAVING a value, then setting
// a unit back to inherit would push nothing — at exactly the moment its nodes
// stop following it and start following the account. Whether a level answers is
// what changed; whether the chain arrives there is the stable property.
//
// Nodes whose chain stops below the edited level are still excluded rather than
// pushed "just in case": an apply is not free — it puts the node into
// maintenance and rewrites its authorized_keys.d — and at fleet scale the
// difference between the two sets is most of the fleet.
func (s *Service) nodesResolvingPasswordAuthAt(ctx context.Context, accountID uuid.UUID, ouID *uuid.UUID) []uuid.UUID {
	nodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to list nodes for an access push", "error", err, "account_id", accountID)
		return nil
	}
	ous, err := s.ouRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to list organizational units for an access push", "error", err, "account_id", accountID)
		return nil
	}
	byID := make(map[uuid.UUID]*organizational_unit.OrganizationalUnit, len(ous))
	for i := range ous {
		byID[ous[i].ID] = &ous[i]
	}

	var out []uuid.UUID
	for i := range nodes {
		n := &nodes[i]
		// The node's own value wins over everything above it.
		if n.SSHPasswordAuth != nil {
			continue
		}
		// The node's own unit, and that unit's root. A dangling ou_id resolves
		// as unassigned, exactly as loadOUChain does.
		var own, root *organizational_unit.OrganizationalUnit
		if n.OUID != nil {
			if own = byID[*n.OUID]; own != nil && own.ParentID != nil {
				root = byID[*own.ParentID]
			}
		}

		if ouID == nil {
			// The account is the last level: reached only when nothing in the
			// node's chain answers. An unassigned node always reaches it.
			ownSilent := own == nil || own.SSHPasswordAuth == nil
			rootSilent := root == nil || root.SSHPasswordAuth == nil
			if ownSilent && rootSilent {
				out = append(out, n.ID)
			}
			continue
		}

		switch {
		case own != nil && own.ID == *ouID:
			// Its own unit: the chain arrives here as soon as the node itself
			// is silent, which was checked above.
			out = append(out, n.ID)
		case root != nil && root.ID == *ouID && own.SSHPasswordAuth == nil:
			// A root reached THROUGH a child that has no opinion of its own.
			out = append(out, n.ID)
		}
	}
	return out
}

// SetNodePolicy overrides a node's policy flags; nil flags are left unchanged.
// The change is recorded under actor (audit.SystemActor for a platform caller):
// it can open password logins on the device or change how it boots.
func (s *Service) SetNodePolicy(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, networkFirst, bootToOS, sshPasswordAuth *organizational_unit.PolicyFlag) error {
	if err := refuseInvalidPolicyFlags(networkFirst, bootToOS, sshPasswordAuth); err != nil {
		return err
	}
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	// Only the flags this call was given, by column. Saving the whole row back
	// would carry every machine-owned column read a moment ago along with it —
	// the node's status, its connection state, the hardware facts the agent
	// reports — and silently revert whatever the gRPC layer wrote in between.
	columns := map[string]interface{}{}
	if networkFirst != nil {
		n.NetworkFirst = networkFirst.ToBool()
		columns["network_first"] = n.NetworkFirst
	}
	if bootToOS != nil {
		n.BootToOS = bootToOS.ToBool()
		columns["boot_to_os"] = n.BootToOS
		// An explicit choice supersedes an in-flight one-shot, and must clear its
		// bookkeeping or it is silently undone. `reboot-to-recovery` saves the
		// node's boot target, forces netboot, and restores the saved value the
		// next time the node reports it came up over HTTP. Setting the target by
		// hand in that window used to leave the saved value untouched, so the
		// restore would later overwrite the operator's change with a value from
		// before they made it — no error, no log, just a setting that reverts.
		//
		// The symptom is indirect: a node pinned to netboot permanently, and
		// later install cycles failing with "came back netbooted" or "device has
		// mounted partitions", neither of which names the cause — a one-shot
		// restore that inherited a stale saved value.
		n.RecoveryRevertPending = false
		n.RecoveryPrevBootToOS = ""
		columns["recovery_revert_pending"] = false
		columns["recovery_prev_boot_to_os"] = ""
	}
	if sshPasswordAuth != nil {
		n.SSHPasswordAuth = sshPasswordAuth.ToBool()
		columns["ssh_password_auth"] = n.SSHPasswordAuth
	}
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, columns); err != nil {
		return err
	}
	if len(columns) > 0 {
		s.audit.RecordDetail(ctx, actor, audit_event.ActionNodePolicyUpdate, audit_event.TargetNode, nodeID.String(),
			nodePolicyAuditDetail(n.Name, networkFirst, bootToOS, sshPasswordAuth))
	}
	// ssh_password_auth is applied to the DEVICE by apply_access_config — it is
	// what writes `PasswordAuthentication` into sshd's config — so turning it off
	// and writing only the column left password logins open on a node the UI
	// already showed as closed. The other two flags in this call are boot policy,
	// which the platform reads at boot time and does not push.
	if sshPasswordAuth != nil {
		s.pushAccess(ctx, accountID, nodeID, "ssh_password_auth changed on the node")
	}
	if len(columns) > 0 {
		s.notifyNodeChanged(ctx, accountID, nodeID)
	}
	return nil
}

// nodePolicyAuditDetail names the flags a SetNodePolicy call set and their new
// values — flag names and inherit/on/off only, nothing else.
func nodePolicyAuditDetail(name string, networkFirst, bootToOS, sshPasswordAuth *organizational_unit.PolicyFlag) string {
	detail := fmt.Sprintf("name=%q", name)
	for _, f := range []struct {
		key  string
		flag *organizational_unit.PolicyFlag
	}{{"network_first", networkFirst}, {"boot_to_os", bootToOS}, {"ssh_password_auth", sshPasswordAuth}} {
		if f.flag == nil {
			continue
		}
		value := organizational_unit.PolicyInherit
		if b := f.flag.ToBool(); b != nil {
			value = organizational_unit.PolicyOff
			if *b {
				value = organizational_unit.PolicyOn
			}
		}
		detail += fmt.Sprintf(" %s=%s", f.key, value)
	}
	return detail
}

// ResetNodePolicyOverrides clears EVERY per-node policy override in one write,
// returning the node to full inheritance from its unit chain (the Organization
// page's per-node "reset to inherited"). Cleared: the three boot/access flags,
// the provisioning defaults (username, password, key, image), the hardware
// equipage and the LED behaviour. Boot-target bookkeeping is cleared with the
// flag for the same reason SetNodePolicy clears it: a stale one-shot restore
// would silently overwrite the reset. When the node HAD an ssh_password_auth
// override, apply_access_config is pushed — the same posture as SetNodePolicy,
// because that flag reaches sshd only through the push.
func (s *Service) ResetNodePolicyOverrides(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	hadPasswordAuth := n.SSHPasswordAuth != nil

	columns := map[string]interface{}{
		"network_first":            nil,
		"boot_to_os":               nil,
		"ssh_password_auth":        nil,
		"default_username":         "",
		"default_password":         "",
		"default_password_enc":     0,
		"ssh_key_id":               nil,
		"os_image_id":              nil,
		"device_profile":           nil,
		"led_config":               nil,
		"recovery_revert_pending":  false,
		"recovery_prev_boot_to_os": "",
	}
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, columns); err != nil {
		return err
	}

	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionNodePolicyReset, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("name=%q push=%t", n.Name, hadPasswordAuth))

	if hadPasswordAuth {
		s.pushAccess(ctx, accountID, nodeID, "node policy overrides reset to inherited")
	}
	s.notifyNodeChanged(ctx, accountID, nodeID)
	return nil
}

// ResolveEffectivePolicy computes the effective policy for a node by walking
// node -> its OU -> the OU's root -> system default.
func (s *Service) ResolveEffectivePolicy(ctx context.Context, accountID, nodeID uuid.UUID) (organizational_unit.EffectivePolicy, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return organizational_unit.EffectivePolicy{}, err
	}

	ou, root, err := s.loadOUChain(ctx, accountID, n.OUID)
	if err != nil {
		return organizational_unit.EffectivePolicy{}, err
	}
	account, err := s.accountPolicyRepo.Get(ctx, accountID)
	if err != nil {
		return organizational_unit.EffectivePolicy{}, err
	}
	return organizational_unit.ResolveEffectivePolicy(organizational_unit.NodePolicyInputs{
		NetworkFirst:    n.NetworkFirst,
		BootToOS:        n.BootToOS,
		SSHPasswordAuth: n.SSHPasswordAuth,
		DefaultUsername: n.DefaultUsername,
		DefaultPassword: n.DefaultPassword,
		SSHKeyID:        n.SSHKeyID,
		OSImageID:       n.OSImageID,
	}, ou, root, account), nil
}

// checkedEquipage normalizes and validates a device-profile update value:
// a zero equipage clears (nil), anything else must validate against the
// loaded registry. Fail-closed on a missing catalog — storing an unvalidated
// equipage would let a broken config.txt render later, far from its cause.
func (s *Service) checkedEquipage(eq *device_profile.Equipage) (*device_profile.Equipage, error) {
	if eq == nil || eq.IsZero() {
		return nil, nil
	}
	if s.deviceProfiles == nil {
		return nil, fmt.Errorf("device profile registry is not available")
	}
	if err := eq.Validate(s.deviceProfiles); err != nil {
		return nil, app_errors.InvalidRequest("invalid_device_profile", err)
	}
	copied := *eq
	return &copied, nil
}

// ResolveEffectiveDeviceProfile walks node -> OU -> root -> account and
// returns the first declared equipage with its source level; ok=false when no
// level declares one. Deliberately NOT part of ResolveEffectiveProvisioning:
// that struct is install-oriented (username/password/key/image) and stamps
// usage tracking — the hardware declaration is a different concern with a
// different consumer.
func (s *Service) ResolveEffectiveDeviceProfile(ctx context.Context, accountID, nodeID uuid.UUID) (device_profile.Equipage, organizational_unit.PolicySource, bool, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return device_profile.Equipage{}, organizational_unit.SourceDefault, false, err
	}
	ou, root, err := s.loadOUChain(ctx, accountID, n.OUID)
	if err != nil {
		return device_profile.Equipage{}, organizational_unit.SourceDefault, false, err
	}
	account, err := s.accountPolicyRepo.Get(ctx, accountID)
	if err != nil {
		return device_profile.Equipage{}, organizational_unit.SourceDefault, false, err
	}
	eq, source, ok := organizational_unit.ResolveDeviceProfile(n.DeviceProfile, ou, root, account)
	return eq, source, ok, nil
}

// ResolveEffectiveLEDConfig walks node -> OU -> root -> account and returns
// the first declared LED configuration with its source; ok=false when no level
// declares one. Same one-block inheritance as the device profile.
func (s *Service) ResolveEffectiveLEDConfig(ctx context.Context, accountID, nodeID uuid.UUID) (node.LEDConfig, organizational_unit.PolicySource, bool, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return node.LEDConfig{}, organizational_unit.SourceDefault, false, err
	}
	ou, root, err := s.loadOUChain(ctx, accountID, n.OUID)
	if err != nil {
		return node.LEDConfig{}, organizational_unit.SourceDefault, false, err
	}
	account, err := s.accountPolicyRepo.Get(ctx, accountID)
	if err != nil {
		return node.LEDConfig{}, organizational_unit.SourceDefault, false, err
	}
	cfg, source, ok := organizational_unit.ResolveLEDConfig(n.LEDConfig, ou, root, account)
	return cfg, source, ok, nil
}

// ResolveEffectiveWatchdogConfig walks node -> OU -> root -> account and
// returns the first declared watchdog configuration with its source. It
// answers what is DESIRED at each level; nothing here dispatches, and nothing
// arms a device without a human present — the per-node apply is the only path
// to hardware.
func (s *Service) ResolveEffectiveWatchdogConfig(ctx context.Context, accountID, nodeID uuid.UUID) (node.WatchdogConfig, organizational_unit.PolicySource, bool, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return node.WatchdogConfig{}, organizational_unit.SourceDefault, false, err
	}
	ou, root, err := s.loadOUChain(ctx, accountID, n.OUID)
	if err != nil {
		return node.WatchdogConfig{}, organizational_unit.SourceDefault, false, err
	}
	account, err := s.accountPolicyRepo.Get(ctx, accountID)
	if err != nil {
		return node.WatchdogConfig{}, organizational_unit.SourceDefault, false, err
	}
	cfg, source, ok := organizational_unit.ResolveWatchdogConfig(n.WatchdogConfig, ou, root, account)
	return cfg, source, ok, nil
}

// GetAccountPolicy returns the account-wide policy fallback; a never-set
// policy comes back as an empty (all-unset) record.
func (s *Service) GetAccountPolicy(ctx context.Context, accountID uuid.UUID) (*organizational_unit.AccountPolicy, error) {
	policy, err := s.accountPolicyRepo.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return &organizational_unit.AccountPolicy{AccountID: accountID}, nil
	}
	return policy, nil
}

// SetAccountPolicy applies an update to the account-wide policy fallback and
// returns the resulting record. Nil command fields leave the stored value
// unchanged; "" / the inherit flag clears a field back to unset.
func (s *Service) SetAccountPolicy(ctx context.Context, cmd account_policy_update.AccountPolicyUpdateCommand) (*organizational_unit.AccountPolicy, error) {
	if err := refuseInvalidPolicyFlags(cmd.NetworkFirst, cmd.BootToOS, cmd.SSHPasswordAuth); err != nil {
		return nil, err
	}
	policy, err := s.GetAccountPolicy(ctx, cmd.AuthContext.AccountID)
	if err != nil {
		return nil, err
	}
	if cmd.NetworkFirst != nil {
		policy.NetworkFirst = cmd.NetworkFirst.ToBool()
	}
	if cmd.BootToOS != nil {
		policy.BootToOS = cmd.BootToOS.ToBool()
	}
	passwordAuthWas := policy.SSHPasswordAuth
	if cmd.SSHPasswordAuth != nil {
		policy.SSHPasswordAuth = cmd.SSHPasswordAuth.ToBool()
	}
	if cmd.DefaultUsername != nil {
		policy.DefaultUsername = strToPtr(*cmd.DefaultUsername)
	}
	if cmd.DefaultPassword != nil {
		policy.DefaultPassword = strToPtr(*cmd.DefaultPassword)
		// Explicitly supplied, so the repository's preserve-unreadable guard
		// must stand down or an explicit clear no-ops.
		policy.DefaultPasswordUnreadable = false
	}
	if cmd.SSHKeyID != nil {
		id, err := uuidStrToPtr("ssh_key_id", *cmd.SSHKeyID)
		if err != nil {
			return nil, err
		}
		policy.SSHKeyID = id
	}
	if cmd.OSImageID != nil {
		id, err := uuidStrToPtr("os_image_id", *cmd.OSImageID)
		if err != nil {
			return nil, err
		}
		policy.OSImageID = id
	}
	if cmd.DeviceProfile != nil {
		eq, err := s.checkedEquipage(cmd.DeviceProfile)
		if err != nil {
			return nil, err
		}
		policy.DeviceProfile = eq
	}
	if cmd.LEDConfig != nil {
		if cmd.LEDConfig.IsZero() {
			policy.LEDConfig = nil
		} else {
			if err := cmd.LEDConfig.Validate(); err != nil {
				return nil, app_errors.InvalidRequest("invalid_led_config", err)
			}
			cfg := *cmd.LEDConfig
			policy.LEDConfig = &cfg
		}
	}
	if cmd.WatchdogConfig != nil {
		// Same contract as the OU arm: disabled clears, the save dispatches
		// nothing, and the device's pre-arm verification holds at apply.
		if !cmd.WatchdogConfig.Enabled {
			policy.WatchdogConfig = nil
		} else {
			if err := cmd.WatchdogConfig.Validate(); err != nil {
				return nil, app_errors.InvalidRequest("invalid_watchdog_config", err)
			}
			cfg := *cmd.WatchdogConfig
			policy.WatchdogConfig = &cfg
		}
	}
	// Same rule as the OU levels: a supplied ref must resolve inside the account.
	{
		var checkSSH, checkImg *uuid.UUID
		if cmd.SSHKeyID != nil {
			checkSSH = policy.SSHKeyID
		}
		if cmd.OSImageID != nil {
			checkImg = policy.OSImageID
		}
		if err := s.validateProvisioningRefs(ctx, cmd.AuthContext.AccountID, checkSSH, checkImg); err != nil {
			return nil, err
		}
	}
	if err := s.accountPolicyRepo.Upsert(ctx, policy); err != nil {
		s.logger.ErrorContext(ctx, "failed to upsert account policy", "error", err, "account_id", cmd.AuthContext.AccountID)
		return nil, err
	}
	// The account default is the last level, so this reaches every node that has
	// not been given an answer by its own flag or by its OU chain — including
	// unassigned nodes, which no OU-level edit can ever reach. Same reasoning as
	// the OU case above.
	pushDetail := ""
	if !samePolicyFlag(passwordAuthWas, policy.SSHPasswordAuth) {
		affected := s.nodesResolvingPasswordAuthAt(ctx, cmd.AuthContext.AccountID, nil)
		s.pushAccessToFleet(ctx, cmd.AuthContext.AccountID, affected,
			"ssh_password_auth changed on the account policy")
		pushDetail = fmt.Sprintf(" push=ssh_password_auth nodes=%d", len(affected))
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionAccountPolicyUpdate, audit_event.TargetAccount, cmd.AuthContext.AccountID.String(),
		fmt.Sprintf("fields=[%s]%s", strings.Join(suppliedPolicyFields(cmd), " "), pushDetail))
	return policy, nil
}

// ResolveEffectiveProvisioning resolves the ready-to-apply provisioning inputs
// for a node (node override -> OU -> root OU), including the real password and
// the SSH key content. Used by the gRPC layer to fill in command fields the
// caller left blank.
func (s *Service) ResolveEffectiveProvisioning(ctx context.Context, accountID, nodeID uuid.UUID) (services.EffectiveProvisioning, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return services.EffectiveProvisioning{}, err
	}
	ou, root, err := s.loadOUChain(ctx, accountID, n.OUID)
	if err != nil {
		return services.EffectiveProvisioning{}, err
	}
	account, err := s.accountPolicyRepo.Get(ctx, accountID)
	if err != nil {
		return services.EffectiveProvisioning{}, err
	}

	var ouUser, rootUser, accUser, ouPass, rootPass, accPass *string
	var ouSSH, rootSSH, accSSH, ouImg, rootImg, accImg *uuid.UUID
	if ou != nil {
		ouUser, ouPass, ouSSH, ouImg = ou.DefaultUsername, ou.DefaultPassword, ou.SSHKeyID, ou.OSImageID
	}
	if root != nil {
		rootUser, rootPass, rootSSH, rootImg = root.DefaultUsername, root.DefaultPassword, root.SSHKeyID, root.OSImageID
	}
	if account != nil {
		accUser, accPass, accSSH, accImg = account.DefaultUsername, account.DefaultPassword, account.SSHKeyID, account.OSImageID
	}

	pwAuth, pwAuthManaged := resolvePasswordAuthPolicy(n, ou, root, account)

	password, passwordUnreadable := pickPassword(
		passwordLevel{n.DefaultPassword, n.DefaultPasswordUnreadable},
		passwordLevelPtr(ouPass, ou != nil && ou.DefaultPasswordUnreadable),
		passwordLevelPtr(rootPass, root != nil && root.DefaultPasswordUnreadable),
		passwordLevelPtr(accPass, account != nil && account.DefaultPasswordUnreadable),
	)

	// The same walk the API-facing effective policy uses, so the level a
	// refusal names is the level the UI shows.
	resolvedUsername := organizational_unit.ResolveString(n.DefaultUsername, ouUser, rootUser, accUser)
	out := services.EffectiveProvisioning{
		Username:               resolvedUsername.Value,
		UsernameSource:         resolvedUsername.Source,
		OUName:                 ouName(ou),
		RootOUName:             ouName(root),
		Password:               password,
		PasswordUnreadable:     passwordUnreadable,
		OSImageID:              pickUUID(n.OSImageID, ouImg, rootImg, accImg),
		SSHPasswordAuth:        pwAuth,
		SSHPasswordAuthManaged: pwAuthManaged,
	}
	if sshKeyID := pickUUID(n.SSHKeyID, ouSSH, rootSSH, accSSH); sshKeyID != nil {
		key, err := s.sshKeyRepo.GetByID(ctx, accountID, *sshKeyID)
		switch {
		case err == nil:
			out.SSHKey = key.KeyContent
			out.SSHKeyType = string(key.KeyType)
		case errors.Is(err, app_errors.ErrSSHKeyNotFound):
			// A reference to a key that no longer exists degrades to "no key",
			// loudly. Returning the error instead took down far more than the
			// key: this function fills install_os and provision_connection
			// fields, resolves playbook facts and builds the access config, so
			// one stale id made every one of them fail for that node until
			// someone edited the database. Deleting a key now clears these
			// columns (ssh_key.Service.Delete), so this is the guard for rows
			// that predate that and for anything that writes an id directly.
			// Safe direction: a config that ends up granting nothing is still
			// refused downstream by WouldLockOutAllAccess.
			s.logger.WarnContext(ctx, "provisioning references an SSH key that no longer exists; continuing without a key",
				"account_id", accountID, "node_id", nodeID, "ssh_key_id", *sshKeyID)
		default:
			return services.EffectiveProvisioning{}, err
		}
	}

	// Resolve the effective secret set: gather attachments for the node, its OU
	// and the root OU, load the (decrypted) secrets, then merge node > OU > root.
	envVars, secrets, err := s.resolveSecrets(ctx, accountID, &nodeID, ou, root)
	if err != nil {
		return services.EffectiveProvisioning{}, err
	}
	out.EnvVars = envVars
	out.Secrets = secrets

	// Best-effort usage tracking: this path materializes secrets into agent
	// commands / workflow contexts. Throttled to once per hour in SQL.
	if len(secrets) > 0 {
		usedIDs := make([]uuid.UUID, 0, len(secrets))
		for i := range secrets {
			usedIDs = append(usedIDs, secrets[i].ID)
		}
		if err := s.secretRepo.TouchLastUsed(ctx, accountID, usedIDs); err != nil {
			s.logger.WarnContext(ctx, "failed to stamp secret last_used_at", "error", err)
		}
	}
	return out, nil
}

// resolvePasswordAuthPolicy folds the tri-state SSH password-login flag down
// the node -> OU -> root -> account chain. System default is true: password
// auth stays permitted unless a level explicitly turns it off. `managed`
// reports that some level set it at all, which is what tells the agent to write
// the sshd directive rather than leave the distro default alone.
//
// It exists as one function because two callers must agree EXACTLY: these two
// booleans are the only inputs to the access-config hash besides the resolved
// entry set, so a second implementation that drifted by one level would make
// the platform's desired hash differ from the command builder's and re-apply
// access to every converged node, forever, on every reconnect.
func resolvePasswordAuthPolicy(
	n *node.Node,
	ou, root *organizational_unit.OrganizationalUnit,
	account *organizational_unit.AccountPolicy,
) (auth bool, managed bool) {
	var ouPwAuth, rootPwAuth, accPwAuth *bool
	if ou != nil {
		ouPwAuth = ou.SSHPasswordAuth
	}
	if root != nil {
		rootPwAuth = root.SSHPasswordAuth
	}
	if account != nil {
		accPwAuth = account.SSHPasswordAuth
	}
	return pickBool(n.SSHPasswordAuth, ouPwAuth, rootPwAuth, accPwAuth, true),
		anySet(n.SSHPasswordAuth, ouPwAuth, rootPwAuth, accPwAuth)
}

// ResolvePasswordAuthPolicy answers only "may this node accept password
// logins, and did anyone say so" — the two booleans the access-config hash
// needs, and nothing else.
//
// ResolveEffectiveProvisioning answers the same question, but to get there it
// also loads the SSH key, resolves and DECRYPTS the node's entire secret set
// and issues a TouchLastUsed UPDATE, because it is built for the command
// builder, which needs all of that. The self-heal path runs on every
// managed-OS report of every converged node and needs none of it: the
// apply_access_config command carries no secrets at all. That made a routine
// heartbeat-shaped event the most expensive read in the system, and stamped
// last_used_at on secrets nothing was using.
func (s *Service) ResolvePasswordAuthPolicy(ctx context.Context, accountID, nodeID uuid.UUID) (auth bool, managed bool, err error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return false, false, err
	}
	ou, root, err := s.loadOUChain(ctx, accountID, n.OUID)
	if err != nil {
		return false, false, err
	}
	account, err := s.accountPolicyRepo.Get(ctx, accountID)
	if err != nil {
		return false, false, err
	}
	auth, managed = resolvePasswordAuthPolicy(n, ou, root, account)
	return auth, managed, nil
}

// ResolveEffectiveSecrets returns the node's effective secret set with source
// provenance. Unlike ResolveEffectiveProvisioning it has no usage-tracking
// side effects — it backs the read-only effective-secrets endpoint.
func (s *Service) ResolveEffectiveSecrets(ctx context.Context, accountID, nodeID uuid.UUID) ([]services.ResolvedSecret, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	ou, root, err := s.loadOUChain(ctx, accountID, n.OUID)
	if err != nil {
		return nil, err
	}
	_, secrets, err := s.resolveSecrets(ctx, accountID, &nodeID, ou, root)
	return secrets, err
}

// ResolveEffectiveSecretsForOU returns the effective secret set visible at an
// OU: its own attachments plus those inherited from its root unit.
func (s *Service) ResolveEffectiveSecretsForOU(ctx context.Context, accountID, ouID uuid.UUID) ([]services.ResolvedSecret, error) {
	ou, err := s.ouRepo.GetByID(ctx, accountID, ouID)
	if err != nil {
		return nil, err
	}
	var root *organizational_unit.OrganizationalUnit
	if ou.ParentID != nil {
		root, err = s.ouRepo.GetByID(ctx, accountID, *ou.ParentID)
		if err != nil {
			return nil, err
		}
	}
	_, secrets, err := s.resolveSecrets(ctx, accountID, nil, ou, root)
	return secrets, err
}

// secretRef is one subject reference contributing to a resolution level.
type secretRef struct {
	secretID uuid.UUID
	via      string // direct | tag
	tag      string
}

// secretLevel is one precedence level (root, ou or node) with its refs.
type secretLevel struct {
	name string // root | ou | node
	ouID *uuid.UUID
	refs []secretRef
}

// resolveSecrets gathers the secrets attached (directly or via tags) to the
// node, its OU and the root OU and merges them with node > OU > root
// precedence; within a level a direct attachment beats a tag-provided one.
// Secrets are returned decrypted (via the secret repository) for internal use;
// nodeID may be nil to resolve the OU-level view only.
func (s *Service) resolveSecrets(ctx context.Context, accountID uuid.UUID, nodeID *uuid.UUID, ou, root *organizational_unit.OrganizationalUnit) (map[string]string, []services.ResolvedSecret, error) {
	// Gather raw attachments per level, root first (lowest precedence).
	var levels []secretLevel
	if root != nil {
		atts, err := s.attachmentRepo.ListByOU(ctx, accountID, root.ID)
		if err != nil {
			return nil, nil, err
		}
		rootID := root.ID
		levels = append(levels, secretLevel{name: "root", ouID: &rootID, refs: nil})
		levels[len(levels)-1].refs = attachmentsToRefs(atts)
	}
	if ou != nil {
		atts, err := s.attachmentRepo.ListByOU(ctx, accountID, ou.ID)
		if err != nil {
			return nil, nil, err
		}
		ouIDCopy := ou.ID
		level := secretLevel{name: "ou", ouID: &ouIDCopy, refs: attachmentsToRefs(atts)}
		if root == nil {
			// A root-level OU chain of one: report it as root for consistency.
			level.name = "root"
		}
		levels = append(levels, level)
	}
	if nodeID != nil {
		atts, err := s.attachmentRepo.ListByNode(ctx, accountID, *nodeID)
		if err != nil {
			return nil, nil, err
		}
		levels = append(levels, secretLevel{name: "node", refs: attachmentsToRefs(atts)})
	}

	// Expand tag refs into carrying-secret refs (one query for all tags) and
	// load every referenced secret.
	if err := s.expandTagRefs(ctx, accountID, levels); err != nil {
		return nil, nil, err
	}
	byID, err := s.loadReferencedSecrets(ctx, accountID, levels)
	if err != nil {
		return nil, nil, err
	}

	env, secrets := mergeEffectiveSecrets(levels, byID)
	return env, secrets, nil
}

// attachmentsToRefs converts raw attachments to refs; tag refs carry a
// placeholder that expandTagRefs replaces with carrying-secret refs.
func attachmentsToRefs(atts []secret.SecretAttachment) []secretRef {
	refs := make([]secretRef, 0, len(atts))
	for i := range atts {
		switch {
		case atts[i].SecretID != nil:
			refs = append(refs, secretRef{secretID: *atts[i].SecretID, via: "direct"})
		case atts[i].Tag != "":
			refs = append(refs, secretRef{via: "tag", tag: atts[i].Tag})
		}
	}
	return refs
}

// expandTagRefs replaces tag placeholders with one ref per carrying secret,
// keeping tag refs before direct refs inside each level so that a direct
// attachment of the same secret (or env key) wins within the level. Dynamic
// membership: the expansion happens at resolve time, so secrets tagged later
// are picked up automatically.
func (s *Service) expandTagRefs(ctx context.Context, accountID uuid.UUID, levels []secretLevel) error {
	tagList := make([]string, 0)
	seen := make(map[string]bool)
	for _, level := range levels {
		for _, ref := range level.refs {
			if ref.tag != "" && !seen[ref.tag] {
				seen[ref.tag] = true
				tagList = append(tagList, ref.tag)
			}
		}
	}
	if len(tagList) == 0 {
		return nil
	}

	carriers, err := s.secretRepo.GetByTags(ctx, accountID, tagList)
	if err != nil {
		return err
	}
	carriersByTag := make(map[string][]uuid.UUID)
	for i := range carriers {
		for _, tag := range carriers[i].Tags {
			if seen[tag] {
				carriersByTag[tag] = append(carriersByTag[tag], carriers[i].ID)
			}
		}
	}

	for li := range levels {
		tagged := make([]secretRef, 0, len(levels[li].refs))
		direct := make([]secretRef, 0, len(levels[li].refs))
		for _, ref := range levels[li].refs {
			if ref.tag == "" {
				direct = append(direct, ref)
				continue
			}
			for _, carrierID := range carriersByTag[ref.tag] {
				tagged = append(tagged, secretRef{secretID: carrierID, via: "tag", tag: ref.tag})
			}
		}
		levels[li].refs = append(tagged, direct...)
	}
	return nil
}

// loadReferencedSecrets loads every unique referenced secret in one query,
// decrypted, keyed by ID. Missing secrets (deleted but still referenced) are
// skipped.
func (s *Service) loadReferencedSecrets(ctx context.Context, accountID uuid.UUID, levels []secretLevel) (map[uuid.UUID]secret.Secret, error) {
	unique := make([]uuid.UUID, 0)
	seen := make(map[uuid.UUID]bool)
	for _, level := range levels {
		for _, ref := range level.refs {
			if !seen[ref.secretID] {
				seen[ref.secretID] = true
				unique = append(unique, ref.secretID)
			}
		}
	}
	secrets, err := s.secretRepo.GetByIDs(ctx, accountID, unique)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]secret.Secret, len(secrets))
	for i := range secrets {
		// The repository degrades an undecryptable value to empty + a flag so the
		// Secrets list stays readable. This is the resolution that feeds the agent
		// env, {{secrets.*}} and the effective-secrets view, so here it must fail
		// CLOSED: substituting the empty value would dispatch a blank
		// credential to a device and report success. Only a REFERENCED secret can
		// do this — an unreadable row nothing is attached to costs nothing.
		if secrets[i].ValueUnreadable {
			return nil, fmt.Errorf("secret %q (%s): %w", secrets[i].Name, secrets[i].ID, app_errors.ErrSecretValueUnreadable)
		}
		byID[secrets[i].ID] = secrets[i]
	}
	return byID, nil
}

// mergeEffectiveSecrets merges the levels (ordered lowest to highest
// precedence, refs within a level ordered group-provided before direct) into
// the env-var map (kind == env, keyed by Key; later applications win) and the
// de-duplicated (by secret ID) effective set whose Source reports the
// highest-precedence attachment. It is a pure function to keep the precedence
// logic unit-testable.
func mergeEffectiveSecrets(levels []secretLevel, byID map[uuid.UUID]secret.Secret) (map[string]string, []services.ResolvedSecret) {
	env := make(map[string]string)
	index := make(map[uuid.UUID]int)
	var out []services.ResolvedSecret

	for _, level := range levels {
		for _, ref := range level.refs {
			sec, ok := byID[ref.secretID]
			if !ok {
				continue
			}
			if sec.Kind == secret.KindEnv {
				env[sec.Key] = sec.Value
			}
			source := services.ResolvedSecretSource{
				Level: level.name,
				Via:   ref.via,
				Tag:   ref.tag,
				OUID:  level.ouID,
			}
			if i, seen := index[sec.ID]; seen {
				// Later applications have higher precedence — update the source.
				out[i].Source = source
				continue
			}
			index[sec.ID] = len(out)
			out = append(out, services.ResolvedSecret{
				ID:        sec.ID,
				Name:      sec.Name,
				Kind:      string(sec.Kind),
				Key:       sec.Key,
				Value:     sec.Value,
				Sensitive: sec.Sensitive,
				Source:    source,
			})
		}
	}
	return env, out
}

// loadOUChain returns the node's OU and its root unit; either may be nil.
func (s *Service) loadOUChain(ctx context.Context, accountID uuid.UUID, ouID *uuid.UUID) (ou, root *organizational_unit.OrganizationalUnit, err error) {
	if ouID == nil {
		return nil, nil, nil
	}
	ou, err = s.ouRepo.GetByID(ctx, accountID, *ouID)
	switch {
	case err == nil:
	case errors.Is(err, app_errors.ErrOrganizationalUnitNotFound):
		// A stale reference resolves to "no OU", loudly, rather than failing
		// every caller. Policy then falls through to the account default, which
		// is what an unassigned node gets anyway — whereas the error propagated
		// into the HTTP-boot handler, which answers it with a 404 for
		// `boot.sig`, so a network-first node with a dangling `ou_id` could
		// never boot again and read as dead hardware. The delete path no longer
		// creates such rows; this is for the ones that already exist and for
		// anything that writes an `ou_id` directly.
		s.logger.WarnContext(ctx, "node references an organizational unit that no longer exists; resolving as unassigned",
			"account_id", accountID, "ou_id", *ouID)
		return nil, nil, nil
	default:
		return nil, nil, err
	}
	if ou.ParentID != nil {
		root, err = s.ouRepo.GetByID(ctx, accountID, *ou.ParentID)
		switch {
		case err == nil:
		case errors.Is(err, app_errors.ErrOrganizationalUnitNotFound):
			// Same rule one level up: a child whose parent is gone still
			// resolves against itself and the account default.
			s.logger.WarnContext(ctx, "organizational unit references a parent that no longer exists; resolving without it",
				"account_id", accountID, "ou_id", ou.ID, "parent_id", *ou.ParentID)
			return ou, nil, nil
		default:
			return nil, nil, err
		}
	}
	return ou, root, nil
}

// ouName names a unit for an operator-facing message, and answers "" when there
// is none — an unassigned node, a top-level unit with no parent, or a unit that
// is gone (loadOUChain resolves a dangling reference as unassigned rather than
// failing).
func ouName(ou *organizational_unit.OrganizationalUnit) string {
	if ou == nil {
		return ""
	}
	return ou.Name
}

// passwordLevel is one level of the password inheritance chain: the value it
// holds, and whether that value is stored-but-undecryptable.
type passwordLevel struct {
	value      string
	unreadable bool
}

// passwordLevelPtr adapts a nullable column to a level.
func passwordLevelPtr(v *string, unreadable bool) passwordLevel {
	if v == nil {
		return passwordLevel{unreadable: unreadable}
	}
	return passwordLevel{value: *v, unreadable: unreadable}
}

// pickPassword resolves the password walking node -> ou -> root -> account.
//
// It is not pickString: a level whose stored password could not be decrypted is
// PRESENT AND BROKEN, not absent. pickString would see the blank the repository
// returned, fall through, and hand the device the parent's password — a silent
// substitution of one credential for another. Resolution stops at that level and
// reports it instead.
func pickPassword(levels ...passwordLevel) (string, bool) {
	for _, l := range levels {
		if l.unreadable {
			return "", true
		}
		if l.value != "" {
			return l.value, false
		}
	}
	return "", false
}

// pickUUID returns the first non-nil value walking node -> ou -> root.
func pickUUID(node *uuid.UUID, candidates ...*uuid.UUID) *uuid.UUID {
	if node != nil {
		return node
	}
	for _, c := range candidates {
		if c != nil {
			return c
		}
	}
	return nil
}

// pickBool resolves a tri-state flag walking node -> ou -> root -> account,
// falling back to systemDefault when no level sets it. nil means "inherit" at
// every level, matching the *bool storage convention.
func pickBool(node *bool, ou, root, account *bool, systemDefault bool) bool {
	for _, c := range []*bool{node, ou, root, account} {
		if c != nil {
			return *c
		}
	}
	return systemDefault
}

// anySet reports whether any level set a tri-state flag explicitly, as opposed
// to every level inheriting and the system default applying.
func anySet(candidates ...*bool) bool {
	for _, c := range candidates {
		if c != nil {
			return true
		}
	}
	return false
}

// strToPtr returns nil for an empty string (meaning "inherit"), else a pointer.
func strToPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// uuidStrToPtr parses a UUID string; "" yields nil (meaning "inherit"). A
// value that does not parse is the request's fault, named by field.
func uuidStrToPtr(field, s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, app_errors.InvalidRequest("invalid_"+field, fmt.Errorf("%s is not a valid UUID", field))
	}
	return &id, nil
}

// policyFlagFields names the tri-state flags in the order every policy
// command carries them: network_first, boot_to_os, ssh_password_auth.
var policyFlagFields = [...]string{"network_first", "boot_to_os", "ssh_password_auth"}

// refuseInvalidPolicyFlags refuses a supplied tri-state flag outside
// inherit|on|off. ToBool reads an unknown value as inherit, so without this a
// typo answered success and cleared the level's setting. nil means "not
// supplied" and is skipped.
func refuseInvalidPolicyFlags(networkFirst, bootToOS, sshPasswordAuth *organizational_unit.PolicyFlag) error {
	for i, flag := range []*organizational_unit.PolicyFlag{networkFirst, bootToOS, sshPasswordAuth} {
		if flag == nil {
			continue
		}
		if err := flag.Validate(policyFlagFields[i]); err != nil {
			return app_errors.InvalidRequest("invalid_policy_flag", err)
		}
	}
	return nil
}
