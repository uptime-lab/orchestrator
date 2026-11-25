package node

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/node_commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// tombstoneFor derives the deleted-node record from the node about to go.
//
// The adoption method is inferred from EVIDENCE the device reported rather than
// from anything the platform believes about it: a non-empty EEPROM-baked
// HTTP_HOST means the identity is in the EEPROM, which is what makes a restore
// bring the device back with no physical access. A node with a serial but no
// baked host keeps its identity in a file on its own disk; a node with no serial
// at all never told us anything.
//
// Two more facts travel with it, because the restore copy is only as honest as
// what the record knows:
//
//   - EEPROMBakeConfirmed. HTTP_HOST says the EEPROM points at a platform, not
//     that it carries THIS node's token: a device baked for an earlier node id
//     and re-adopted by file reports the same host. Only a flash the platform
//     performed for this row (BootloaderFlashedAt) proves the bake.
//   - BootOrderNetbootsFirst. A netboot-first device presents its EEPROM
//     identity on every boot; a disk-first one boots whatever its installed OS
//     holds and asks the platform for a boot image only when that disk fails.
func tombstoneFor(n *node.Node, actorUserID uuid.UUID) *node_tombstone.Tombstone {
	serial := ""
	if n.Fingerprint != nil {
		serial = n.Fingerprint.HardwareSerial
	}
	method := node_tombstone.AdoptionUnknown
	switch {
	case n.BootHTTPHost() != "":
		method = node_tombstone.AdoptionEEPROM
	case serial != "":
		method = node_tombstone.AdoptionFile
	}
	var reachesNetboot *bool
	if order := n.ReportedBootOrder(); len(order) > 0 {
		ok, _ := node.BootOrderCanReturnToNetboot(order)
		reachesNetboot = &ok
	}
	admitted := n.IsAdmitted()
	return &node_tombstone.Tombstone{
		ID:                     n.ID,
		AccountID:              n.AccountID,
		HardwareSerial:         serial,
		UniqueToken:            n.UniqueToken,
		Name:                   n.Name,
		Hostname:               n.Hostname,
		OUID:                   n.OUID,
		AdoptionMethod:         method,
		EEPROMBakeConfirmed:    n.BootloaderFlashedAt != nil,
		BootOrderNetbootsFirst: reachesNetboot,
		BootToOS:               n.BootToOS,
		// What the device cap needs from the restore: whether this row was a
		// device the install managed. See Tombstone.WasAdmitted.
		WasAdmitted:    &admitted,
		ApprovalStatus: n.ApprovalStatus,
		Status:         n.Status,
		DeletedAt:      time.Now(),
		DeletedBy:      actorUserID,
	}
}

// RecordIdentityReleased stamps the tombstone when the device reports that it
// erased its adoption file — the terminal success of the release command
// dispatched by a forced delete, which arrives after the node row is gone.
//
// The stamp records what the DEVICE said, and the audit row says so: the
// platform cannot verify the erase, only repeat the report. First report wins;
// a replay changes nothing and is not an error worth surfacing.
func (s *Service) RecordIdentityReleased(ctx context.Context, accountID, nodeID uuid.UUID) error {
	if s.tombstoneRepo == nil {
		return nil
	}
	now := time.Now()
	err := s.tombstoneRepo.MarkIdentityReleased(ctx, accountID, nodeID, now)
	if errors.Is(err, app_errors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	tombstone, terr := s.tombstoneRepo.GetByID(ctx, accountID, nodeID)
	detail := "reported_by=device"
	if terr == nil {
		detail = fmt.Sprintf("hostname=%q serial=%q deleted_by=%s reported_by=device",
			tombstone.Hostname, tombstone.HardwareSerial, tombstone.DeletedBy)
	}
	s.audit.RecordDetail(ctx, audit.SystemActor(accountID),
		audit_event.ActionNodeIdentityReleased, audit_event.TargetNode, nodeID.String(), detail)
	return nil
}

// ListDeleted returns the account's deleted-node records, each marked with
// whether its serial has since been bound by another account, or by another
// live node of this one — the facts that decide whether the restore this record
// offers can work at all.
func (s *Service) ListDeleted(ctx context.Context, accountID uuid.UUID) ([]node_tombstone.Tombstone, error) {
	if s.tombstoneRepo == nil {
		return nil, nil
	}
	tombstones, err := s.tombstoneRepo.ListByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	s.markSerialsBoundElsewhere(ctx, accountID, tombstones)
	return tombstones, nil
}

// markSerialsBoundElsewhere resolves, in ONE query, which of these records name
// a serial another account now carries, and — in a second query run only when
// that one says this account carries some of them — which of this account's
// live nodes does.
//
// It FAILS OPEN, deliberately: on a lookup error every flag stays false and the
// list is returned as it always was. Emptying or reddening this panel because of
// a database hiccup would break the only route back from a delete for a reason
// that has nothing to do with any device — and the restore re-asks the question
// for itself and refuses on its own, so nothing here is load-bearing for
// correctness. It is an affordance, not a guard.
func (s *Service) markSerialsBoundElsewhere(ctx context.Context, accountID uuid.UUID, tombstones []node_tombstone.Tombstone) {
	if s.nodeRepo == nil {
		return
	}
	// Only serials the guard would ever act on. Asking about the rest is both
	// wasted work and a wrong answer: a short WebUSB stamp or an all-zeros VM
	// serial is legitimately shared, and flagging it would hide the recovery
	// affordance for a healthy device.
	seen := make(map[string]struct{}, len(tombstones))
	serials := make([]string, 0, len(tombstones))
	for i := range tombstones {
		normalized := node.NormalizeSerial(tombstones[i].HardwareSerial)
		if !node.IsUniqueFullSerial(normalized) {
			continue
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		serials = append(serials, normalized)
	}
	if len(serials) == 0 {
		return
	}
	scopes, err := s.nodeRepo.SerialBindingScopes(ctx, accountID, serials)
	if err != nil {
		s.logger.WarnContext(ctx, "could not resolve whether deleted nodes' serials are bound elsewhere; reporting none",
			"account_id", accountID, "error", err)
		return
	}
	inAccount := make([]string, 0, len(serials))
	for i := range tombstones {
		normalized := node.NormalizeSerial(tombstones[i].HardwareSerial)
		scope := scopes[normalized]
		tombstones[i].SerialBoundElsewhere = node.SerialConflictsAcrossAccounts(
			normalized, scope.InAccount, scope.InAnotherAccount)
		if !node.IsUniqueFullSerial(normalized) {
			continue
		}
		if scope.InAccount {
			inAccount = append(inAccount, normalized)
		}
		// The low-word stamp of a browser flash is invisible to the exact
		// projection above, so it is always asked about.
		if keys := inAccountHolderKeys(normalized); len(keys) > 1 {
			inAccount = append(inAccount, keys[1])
		}
	}
	if len(inAccount) == 0 {
		return
	}
	// Only when the projection says this account holds one of the serials: the
	// second query names the live node, which the panel shows as the reason.
	holders, err := s.nodeRepo.SerialHoldersInAccount(ctx, accountID, inAccount)
	if err != nil {
		s.logger.WarnContext(ctx, "could not resolve which of this account's nodes hold deleted nodes' serials; reporting none",
			"account_id", accountID, "error", err)
		return
	}
	for i := range tombstones {
		t := &tombstones[i]
		// Only a record the restore would otherwise accept: a restored record's
		// serial is carried by the restored node itself, and a record without a
		// usable credential is not offered at all.
		if !t.Restorable() || !t.CredentialRestorable() {
			continue
		}
		for _, key := range inAccountHolderKeys(node.NormalizeSerial(t.HardwareSerial)) {
			if holder, ok := holders[key]; ok && holder.NodeID != t.ID {
				t.SerialBoundInAccount = &node_tombstone.SerialHolderNode{ID: holder.NodeID, Name: holder.Name}
				break
			}
		}
	}
}

// inAccountHolderKeys are the stamps under which a live node of the SAME
// account carries the board a full serial names: the serial itself, and its
// low word, which is what a board flashed again from the browser is stamped
// with (the browser reads only the low eight hex). Matching the low word is
// right within one account and only there: the account's own serial lookup
// (GetByHardwareSerial) already resolves a short stamp and a full serial to
// each other by this suffix, so two such live rows are one board to network
// boot whichever device they came from; across accounts the low-word guard
// answers instead. A repeated-nibble low word is excluded, as everywhere,
// because unrelated boards really report one.
func inAccountHolderKeys(normalizedFullSerial string) []string {
	keys := []string{normalizedFullSerial}
	if node.LowWordIsGuarded(normalizedFullSerial) {
		keys = append(keys, node.LowWord(normalizedFullSerial))
	}
	return keys
}

// RestoreDeleted re-creates a deleted node under its ORIGINAL id and token, so
// the device still carrying that identity is accepted again.
//
// Everything else about the node is gone and stays gone — its command history,
// its tracked files, its secret attachments, its workflow runs. That is a
// property of the DELETE, which removes the node-scoped rows (and detaches
// node-scoped access entries, so they too apply to nothing after a restore),
// not of anything this function does: the id it hands back is the original one,
// so any row still keyed on it would be delivered again. This restores an
// identity, not a node's contents: the device re-reports its own hardware,
// hostname and boot state on the next connect, which is where all of that came
// from in the first place.
func (s *Service) RestoreDeleted(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) (*node.Node, error) {
	if s.tombstoneRepo == nil {
		return nil, app_errors.ErrNotFound
	}
	tombstone, err := s.tombstoneRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	if !tombstone.Restorable() {
		return nil, fmt.Errorf("%w (on %s)", app_errors.ErrNodeAlreadyRestored,
			tombstone.RestoredAt.UTC().Format(time.RFC3339))
	}
	// A restore whose token cannot be decrypted would recreate the node with a
	// BLANK credential: the device's own copy then never matches, the mismatch
	// reads as dead hardware, and the truth (a key problem) is invisible.
	// Refuse with the cause instead; a rotation that restores the right key
	// makes the same restore succeed.
	if tombstone.UniqueTokenUnreadable {
		return nil, &NodeCredentialUnreadableError{NodeID: nodeID, KeyProblem: true}
	}
	// A record that decrypts to NO token was written from a node whose own
	// token was already unreadable. An empty token is never written into
	// nodes.unique_token: no device can present it, and it is not unique, so
	// a second such restore would collide with the first.
	if !tombstone.CredentialRestorable() {
		return nil, &NodeCredentialUnreadableError{NodeID: nodeID}
	}
	// A live node with that id means someone already restored it, or the id came
	// back some other way. Refuse rather than overwrite: the row that exists is
	// the one the device is talking to.
	if existing, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err == nil && existing != nil {
		return nil, app_errors.ErrNodeAlreadyExists
	}

	// The admission state is RESTORED, not chosen: a row that was not
	// admitted at deletion comes back exactly as it was, so its device's
	// first connect (or the operator's approval) is the admission, under the
	// device cap. ApprovalStatus is always set explicitly — left empty, the
	// column default ('approved') applies and turns a rejected or pending
	// device into an admitted one.
	status, approval := tombstone.RestoreState()
	restored := &node.Node{
		ID:             tombstone.ID,
		AccountID:      tombstone.AccountID,
		Name:           tombstone.Name,
		Hostname:       tombstone.Hostname,
		UniqueToken:    tombstone.UniqueToken,
		Status:         status,
		ApprovalStatus: approval,
		// The boot override is RESTORED, not chosen — see the field's comment on
		// the tombstone for why either default would be wrong.
		BootToOS: tombstone.BootToOS,
	}
	// Give the row back its hardware serial, which is the ONLY thing HTTP-boot
	// can resolve a booting device by (`fingerprint->>'hardware_serial'`).
	//
	// Without it a restore returned the row and left a netboot-dependent device
	// unable to come back: the ROM asks for its per-serial bootstrap, the
	// platform still finds no node, and where the boot path is fail-CLOSED it
	// 404s and the device falls through to a disk that may not boot. No agent
	// starts, so nothing ever rebinds the fingerprint, and the loop closes on
	// a device recoverable only by physical access. That is exactly the
	// outcome the tombstone exists to remove.
	//
	// It looks like it works because a device still RUNNING rebinds its own
	// fingerprint over gRPC on the next connect, which is the path a test on
	// live hardware takes.
	if serial := node.NormalizeSerial(tombstone.HardwareSerial); serial != "" {
		// The same uniqueness guard an adoption runs: if the serial has been
		// bound by another account in the meantime, that binding wins.
		//
		// This REFUSES rather than restoring a serial-less row. Degrading was the
		// defect: the operator is answered 200, the panel shows the node back, and
		// the device is unreachable forever — HTTP-boot resolves only by serial,
		// and Connect refuses it on first bind. Nothing reports it but a log
		// line.
		//
		// A tombstone's serial is invisible to ListByExactSerial, so it is free
		// for anyone to bind while the node is deleted; this is therefore a
		// routine outcome of an account move, not an exotic one.
		//
		// The same holds inside the account: the board re-adopted as a NEW node
		// carries this serial, and restoring would leave two live rows claiming
		// it, either of which the boot lookup by serial may pick. Asked first,
		// because guardSerialBinding lets an account rebind its own serial.
		if node.IsUniqueFullSerial(serial) {
			keys := inAccountHolderKeys(serial)
			holders, err := s.nodeRepo.SerialHoldersInAccount(ctx, accountID, keys)
			if err != nil {
				return nil, fmt.Errorf("checking whether the hardware serial is still free: %w", err)
			}
			for _, key := range keys {
				if holder, ok := holders[key]; ok {
					return nil, &SerialBoundInAccountError{NodeID: nodeID, Serial: serial,
						HolderID: holder.NodeID, HolderName: holder.Name}
				}
			}
		}
		switch err := s.guardSerialBinding(ctx, accountID, serial); {
		case errors.Is(err, app_errors.ErrHardwareSerialConflict):
			return nil, &SerialBoundElsewhereError{NodeID: nodeID, Serial: serial}
		case err != nil:
			// A lookup that FAILED is not a conflict, and must never be reported as
			// one: telling an operator another account holds their serial when the
			// database merely hiccuped sends them to argue with a tenant who has
			// nothing. Refuse with the real cause and let them retry.
			return nil, fmt.Errorf("checking whether the hardware serial is still free: %w", err)
		}
		restored.Fingerprint = &node.HardwareFingerprint{HardwareSerial: serial}
		// The fingerprint carries no MAC, and `Connect` compares one. Marking
		// the node as awaiting its first connect is what makes it rebind the
		// full fingerprint instead of refusing on a mismatch against an empty
		// value — which would lock the device out over gRPC as well as over
		// HTTP-boot.
		restored.AwaitingFirstConnect = true
	}
	// Which insert is decided by the predicate the cap counts, on the row
	// about to be written, so the two cannot disagree. An admitted row is an
	// admission like any other: the delete freed its slot, and the slot may
	// have been taken since, so it is inserted under the cap (count and
	// insert under one lock) and refused with the typed device-cap error when
	// the install is full, or the account-limit error when the account is. The tombstone stays restorable, so the same restore
	// succeeds once a slot is free. A non-admitted row changes no count and
	// needs no lock.
	if restored.IsAdmitted() {
		if err := s.admit(ctx, restored, audit.UserActor(accountID, actorUserID), ""); err != nil {
			var capErr *entitlement.DeviceCapError
			if errors.As(err, &capErr) {
				return nil, capErr
			}
			var limitErr *entitlement.AccountDeviceLimitError
			if errors.As(err, &limitErr) {
				return nil, limitErr
			}
			return nil, fmt.Errorf("restoring the node row: %w", err)
		}
	} else if err := s.nodeRepo.Create(ctx, restored); err != nil {
		return nil, fmt.Errorf("restoring the node row: %w", err)
	}
	// The unit goes back after the insert, through the guarded assignment:
	// nodes.ou_id has no foreign key to refuse a unit deleted meanwhile.
	unitDetail := s.restoreUnit(ctx, accountID, nodeID, tombstone.OUID)
	// Answer with the row as stored: the insert stamps the timestamps and the
	// column defaults, which the value built above does not carry.
	if stored, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err == nil && stored != nil {
		restored = stored
	} else {
		s.logger.WarnContext(ctx, "restored a node but could not read it back; answering with the restored values",
			"node_id", nodeID, "error", err)
	}
	if err := s.tombstoneRepo.MarkRestored(ctx, accountID, nodeID); err != nil {
		// The node is back, which is what was asked for. A tombstone that still
		// reads as restorable is a cosmetic wrong — and the restore above refuses
		// to run twice anyway, because the live row now exists.
		s.logger.WarnContext(ctx, "restored a node but could not stamp its tombstone",
			"node_id", nodeID, "error", err)
	}

	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionNodeRestore, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("hostname=%q serial=%q adoption=%s deleted_at=%s status=%s approval=%s admitted=%t ou=%s",
			tombstone.Hostname, tombstone.HardwareSerial, tombstone.AdoptionMethod,
			tombstone.DeletedAt.UTC().Format(time.RFC3339), restored.Status, restored.ApprovalStatus,
			restored.IsAdmitted(), unitDetail))

	if s.websocketClient != nil {
		s.notifyNodeCreated(*restored)
	}
	return restored, nil
}

// restoreUnit puts a restored node back into the unit its record names, and
// answers what happened for the audit detail.
//
// The unit may have been deleted while the node was — the unit's delete guard
// counts live nodes, not deleted-node records — and a node whose ou_id points
// at nothing breaks every path that resolves its unit (network boot among
// them). So the assignment goes through the same share-locked check an
// ordinary move takes, and a unit that is gone is dropped: the node comes back
// unassigned and the operator can file it again. Any other failure also leaves
// it unassigned; the restore itself already succeeded and is not undone.
func (s *Service) restoreUnit(ctx context.Context, accountID, nodeID uuid.UUID, ouID *uuid.UUID) string {
	if ouID == nil {
		return "none"
	}
	if s.unitAssigner == nil {
		s.logger.WarnContext(ctx, "restored a node without its unit: unit assignment is not wired",
			"node_id", nodeID, "ou_id", *ouID)
		return "dropped:" + ouID.String()
	}
	switch err := s.unitAssigner.AssignNodeOU(ctx, accountID, nodeID, ouID); {
	case err == nil:
		return ouID.String()
	case errors.Is(err, app_errors.ErrOrganizationalUnitNotFound):
		s.logger.WarnContext(ctx, "restored a node unassigned: its unit no longer exists",
			"node_id", nodeID, "ou_id", *ouID)
		return "dropped:" + ouID.String()
	default:
		s.logger.WarnContext(ctx, "restored a node unassigned: its unit could not be assigned",
			"node_id", nodeID, "ou_id", *ouID, "error", err)
		return "dropped:" + ouID.String()
	}
}

// ForgetDeleted drops a deleted-node record: the operator's explicit "that
// device is never coming back".
//
// Worth doing rather than leaving the row: while it exists, anyone who can
// restore a node can bring that device back onto the account. Once it is gone the
// device is unrecoverable without physical access, which is the point.
func (s *Service) ForgetDeleted(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) error {
	if s.tombstoneRepo == nil {
		return app_errors.ErrNotFound
	}
	tombstone, err := s.tombstoneRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	if err := s.tombstoneRepo.Delete(ctx, accountID, nodeID); err != nil {
		return err
	}
	s.clearForgottenNodeSecrets(ctx, accountID, nodeID)
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionNodeForget, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("hostname=%q serial=%q adoption=%s",
			tombstone.Hostname, tombstone.HardwareSerial, tombstone.AdoptionMethod))
	return nil
}

// clearForgottenNodeSecrets repeats, for a forgotten node, the delete's
// best-effort removal of its secret attachments and attribute-backing secrets
// (attr:<nodeID>:<key>). Once the record is forgotten the id can never return,
// so anything still keyed on it is a leftover that counts against the secret
// quota with no node to show it under — and this is the last moment anything
// knows the id. A record can be forgotten after its node was RESTORED, so a
// live node with this id (or a lookup that cannot tell) keeps everything.
func (s *Service) clearForgottenNodeSecrets(ctx context.Context, accountID, nodeID uuid.UUID) {
	if s.attachmentRepo == nil && s.secretRepo == nil {
		return
	}
	if _, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); !errors.Is(err, app_errors.ErrNodeNotFound) {
		if err != nil {
			s.logger.WarnContext(ctx, "could not tell whether a forgotten node is live; its secrets are left in place",
				"node_id", nodeID, "error", err)
		}
		return
	}
	if s.attachmentRepo != nil {
		if err := s.attachmentRepo.DeleteByNodeID(ctx, accountID, nodeID); err != nil {
			s.logger.WarnContext(ctx, "failed to delete secret attachments for forgotten node",
				"node_id", nodeID, "error", err)
		}
	}
	if s.secretRepo != nil {
		if deleted, err := s.secretRepo.DeleteNodeAttributes(ctx, accountID, nodeID); err != nil {
			s.logger.WarnContext(ctx, "failed to delete attribute secrets for forgotten node",
				"node_id", nodeID, "error", err)
		} else if deleted > 0 {
			s.logger.InfoContext(ctx, "deleted attribute secrets of forgotten node",
				"node_id", nodeID, "deleted", deleted)
		}
	}
}

// FindDeletedBySerial answers "is this unknown serial a node we deleted?" for the
// HTTP-boot handler. Returns nil when it is not, so a caller can treat the
// absence as the ordinary unknown-serial case.
func (s *Service) FindDeletedBySerial(ctx context.Context, serial string) *node_tombstone.Tombstone {
	if s.tombstoneRepo == nil {
		return nil
	}
	tombstone, err := s.tombstoneRepo.FindByHardwareSerial(ctx, serial)
	if err != nil {
		return nil
	}
	return tombstone
}

// deletedRefusalStampInterval bounds how often one deleted node's record is
// stamped: a stranded agent redials about once a second, and the stamp is a
// "recently" fact, not a heartbeat.
const deletedRefusalStampInterval = time.Minute

// NoteDeletedNodeRefused records, on the deleted node's tombstone, that a
// device just presented that node's id and token and was refused — so the
// operator can see the device is still out there and act (restore it, then
// delete it while it is connected, which is the path that releases it).
//
// It is called from the handshake's refusal branch and must change NOTHING
// about that refusal: no different error, no stream, no row in nodes, no
// anomaly. It writes only when the presented token matches the stored one
// (constant-time) and, when the record carries a serial, the presented serial
// matches too — a peer that merely names a deleted id has proved nothing and
// must not be able to make a record say "the device is trying". Best-effort:
// a failure here is logged and the refusal proceeds unchanged.
func (s *Service) NoteDeletedNodeRefused(ctx context.Context, accountID, nodeID uuid.UUID, presentedToken, presentedSerial string) {
	if s.tombstoneRepo == nil || presentedToken == "" {
		return
	}
	tombstone, err := s.tombstoneRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return
	}
	if !tombstone.Restorable() || tombstone.UniqueTokenUnreadable {
		return
	}
	if !services.SecureTokenEqual(tombstone.UniqueToken, presentedToken) {
		return
	}
	if tombstone.HardwareSerial != "" &&
		node.NormalizeSerial(tombstone.HardwareSerial) != node.NormalizeSerial(presentedSerial) {
		return
	}
	if err := s.tombstoneRepo.MarkRefused(ctx, accountID, nodeID, time.Now(), deletedRefusalStampInterval); err != nil {
		s.logger.WarnContext(ctx, "could not note the refused connect on the deleted node's record",
			"node_id", nodeID, "error", err)
	}
}

// requestIdentityRelease dispatches release_identity so the device deletes its
// own adoption file and returns to a clean unadopted state.
//
// Registered and queued like any other command rather than pushed down the
// stream directly, so the version floor, the audit row and the command history
// all apply — including the floor being fail-closed, which is what stops the
// platform from telling an operator a device was released when an older agent
// simply ignored the message.
func (s *Service) requestIdentityRelease(ctx context.Context, n *node.Node, actorUserID uuid.UUID) error {
	if s.nodeCommandService == nil || s.nodeCommandClient == nil {
		return fmt.Errorf("command dispatch is not wired")
	}
	// The floor, applied HERE because this path does not go through
	// sendCommandToNode, which is where every other caller gets it.
	//
	// Without this the command registers, ships, and an agent below the floor
	// answers "unknown or unsupported command type". Two things
	// then go wrong at once — every forced delete leaves a FAILED command row
	// that looks like a fault, and the tombstone records
	// identity_release_requested=true for a device that plainly did not release
	// anything, which is the one claim that field exists to keep honest.
	if minVersion, required := node_command.MinAgentVersion(node_command.SlugReleaseIdentity); required {
		if !node_command.MeetsMinAgentVersion(n.AgentVersion, minVersion) {
			return fmt.Errorf("agent %q is below the %s floor for %s",
				n.AgentVersion, minVersion, node_command.SlugReleaseIdentity)
		}
	}
	accountID, nodeID := n.AccountID, n.ID
	payload := map[string]interface{}{
		"reason": "the node was deleted from the platform",
	}
	// The deleting user is the actor: the command's audit row used to read as
	// the system while deleted_by on the tombstone named a person.
	nodeCmd, err := s.nodeCommandService.RegisterNodeCommand(ctx, nodeID,
		node_command.SlugReleaseIdentity, payload, commands.NewAuthContext(accountID, actorUserID))
	if err != nil {
		return fmt.Errorf("registering the release command: %w", err)
	}
	err = s.nodeCommandClient.Send(node_commands.NodeCommandOperation{
		Type: node_commands.OperationTypeSendCommand,
		SendCommand: &node_commands.SendCommandOperation{
			NodeCommandID: nodeCmd.ID,
			NodeID:        nodeCmd.NodeID,
			AccountID:     nodeCmd.AccountID,
		},
	})
	if err != nil {
		return fmt.Errorf("queueing the release command: %w", err)
	}
	return nil
}
