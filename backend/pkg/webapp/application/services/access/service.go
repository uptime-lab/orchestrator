package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/google/uuid"
)

// AccessApplier dispatches an apply_access_config to a node so an access change
// reaches the device. Implemented by the node service; wired via SetApplier to
// avoid a circular dependency.
type AccessApplier interface {
	EnqueueApplyAccessConfig(ctx context.Context, accountID, nodeID uuid.UUID) error
	// ReapplyAccessConfig re-dispatches on an explicit operator request,
	// surfacing errors (agent too old, node not approved) instead of swallowing
	// them. queued=true means the node is offline/in recovery and the command
	// waits for a managed-OS boot.
	ReapplyAccessConfig(ctx context.Context, accountID, nodeID uuid.UUID) (bool, error)
	// ApplyAccessConfigOffline writes the resolved access config into the
	// target disk of a NETBOOTED node (recovery-mode apply).
	// targetDevice empty = auto-pick, refused when ambiguous; rootPartition
	// 0 = agent-side detection.
	ApplyAccessConfigOffline(ctx context.Context, accountID, nodeID uuid.UUID, targetDevice string, rootPartition int) error
}

// Service owns the access-entry lifecycle: CRUD, per-key attach/detach, and
// resolution of a node's effective access set from its OU hierarchy.
type Service struct {
	entryRepo  repository.AccessEntryRepository
	sshKeyRepo repository.SSHKeyRepository
	ouRepo     repository.OrganizationalUnitRepository
	nodeRepo   repository.NodeRepository
	applier    AccessApplier
	audit      *audit.Recorder
	logger     *slog.Logger
}

// SetApplier wires the node-command dispatcher used to propagate access changes
// to devices. Optional: without it, mutations are stored but not pushed (the
// pre-existing behaviour), which is why it is a setter rather than a constructor
// argument.
func (s *Service) SetApplier(a AccessApplier) { s.applier = a }

func NewService(
	entryRepo repository.AccessEntryRepository,
	sshKeyRepo repository.SSHKeyRepository,
	ouRepo repository.OrganizationalUnitRepository,
	nodeRepo repository.NodeRepository,
	auditRecorder *audit.Recorder,
	logger *slog.Logger,
) *Service {
	if entryRepo == nil {
		panic("access.Service requires an access entry repository")
	}
	if sshKeyRepo == nil {
		panic("access.Service requires an ssh key repository")
	}
	if ouRepo == nil {
		panic("access.Service requires an organizational unit repository")
	}
	if nodeRepo == nil {
		panic("access.Service requires a node repository")
	}
	if auditRecorder == nil {
		panic("access.Service requires an audit recorder")
	}
	return &Service{
		entryRepo:  entryRepo,
		sshKeyRepo: sshKeyRepo,
		ouRepo:     ouRepo,
		nodeRepo:   nodeRepo,
		audit:      auditRecorder,
		logger:     logger.With("service", "access"),
	}
}

// CreateUserEntryRequest creates a named-user access entry.
type CreateUserEntryRequest struct {
	AccountID uuid.UUID
	// ActorUserID is the operator making the change, for the audit trail. An
	// access entry is what grants a shell and sudo on every node in its scope, so
	// a change here with no recorded author is the gap this field closes.
	ActorUserID    uuid.UUID
	OUIDs          []uuid.UUID // a user can live in several units
	NodeID         *uuid.UUID
	AccountWide    bool // only when OUIDs and NodeID are empty: fleet-wide vs unassigned
	Username       string
	Sudo           bool
	Password       *string
	AdoptUnmanaged bool
	KeyIDs         []uuid.UUID // optional initial ssh keys
	// Principals are certificate principals accepted to log in as this user
	// (effective only where a CA is trusted).
	Principals []string
}

// CreateCAEntryRequest creates a trust-external-CA access entry. A CA is a pure
// trust anchor — certificate principals live on USER entries — so none here.
type CreateCAEntryRequest struct {
	AccountID   uuid.UUID
	ActorUserID uuid.UUID
	OUIDs       []uuid.UUID
	NodeID      *uuid.UUID
	AccountWide bool
	CAKeyID     uuid.UUID
}

// UpdateEntryRequest mutates the editable fields of an entry. A nil field is
// left unchanged; ClearPassword removes the password (distinct from "leave").
// A non-nil Scope re-targets the entry (assign to units, make account-wide, or
// park it as unassigned); the change is pushed to both the old and new nodes.
type UpdateEntryRequest struct {
	AccountID      uuid.UUID
	ActorUserID    uuid.UUID
	EntryID        uuid.UUID
	Sudo           *bool
	Password       *string
	ClearPassword  bool
	Principals     *[]string
	AdoptUnmanaged *bool
	Scope          *ScopeChange
}

// ScopeChange re-targets an entry. OUIDs XOR NodeID; both empty with
// AccountWide=false means unassigned (applies nowhere), both empty with
// AccountWide=true means the fleet-wide default.
type ScopeChange struct {
	OUIDs       []uuid.UUID
	NodeID      *uuid.UUID
	AccountWide bool
}

// assertTargetOwned verifies the entry's target belongs to the caller's
// account. Without it an entry could name another tenant's OU or node id: reads
// are account-scoped so it would never resolve for them, but storing a
// cross-tenant reference is an integrity problem and makes the row impossible to
// reason about.
func (s *Service) assertTargetOwned(ctx context.Context, accountID uuid.UUID, ouIDs []uuid.UUID, nodeID *uuid.UUID) error {
	// One query per unit, so deduplicate here rather than relying on each caller
	// to have done it: a request body repeating one owned unit id would
	// otherwise buy one database round trip per repetition.
	for _, ouID := range access_entry.DedupOUIDs(ouIDs) {
		if _, err := s.ouRepo.GetByID(ctx, accountID, ouID); err != nil {
			return err
		}
	}
	if nodeID != nil {
		if _, err := s.nodeRepo.GetByID(ctx, accountID, *nodeID); err != nil {
			return err
		}
	}
	return nil
}

// assertUsernameFree rejects a user entry whose username would collide with
// another entry covering any of the same units (two entries would fight over
// the same OS user there). DB-level partial indexes cover the node and
// fleet-wide scopes; the OU overlap lives in a join table, so it is checked
// here. exceptID skips the entry being re-scoped.
func (s *Service) assertUsernameFree(ctx context.Context, accountID uuid.UUID, username string, ouIDs []uuid.UUID, exceptID uuid.UUID) error {
	if len(ouIDs) == 0 {
		return nil
	}
	others, err := s.entryRepo.ListByOUs(ctx, accountID, ouIDs)
	if err != nil {
		return err
	}
	for i := range others {
		if others[i].ID != exceptID && others[i].Type == access_entry.TypeUser && others[i].Username == username {
			return app_errors.ErrAccessEntryUsernameTaken
		}
	}
	return nil
}

// CreateUserEntry validates the entry, checks key-type conformance, and persists it.
func (s *Service) CreateUserEntry(ctx context.Context, req CreateUserEntryRequest) (*access_entry.AccessEntry, error) {
	if err := s.assertTargetOwned(ctx, req.AccountID, req.OUIDs, req.NodeID); err != nil {
		return nil, err
	}
	e, err := access_entry.NewUserAccessEntry(req.AccountID, req.Username, req.Sudo, req.Password, req.OUIDs, req.NodeID)
	if err != nil {
		return nil, err
	}
	if err := s.assertUsernameFree(ctx, req.AccountID, e.Username, e.OUIDs, e.ID); err != nil {
		return nil, err
	}
	e.AccountWide = len(e.OUIDs) == 0 && req.NodeID == nil && req.AccountWide
	e.AdoptUnmanaged = req.AdoptUnmanaged
	e.KeyIDs = req.KeyIDs
	if e.AdoptsWithoutCredential() {
		return nil, access_entry.ErrAdoptNeedsCredential
	}
	e.Principals = req.Principals
	if err := e.ValidatePrincipals(); err != nil {
		return nil, err
	}
	if err := s.checkKeyConformance(ctx, e); err != nil {
		return nil, err
	}
	if err := s.entryRepo.Create(ctx, e); err != nil {
		return nil, err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(req.AccountID, req.ActorUserID),
		audit_event.ActionAccessEntryCreateUser, audit_event.TargetAccessEntry, e.ID.String(),
		userEntryDetail(e))
	s.propagate(ctx, req.AccountID, e)
	return e, nil
}

// CreateCAEntry validates and persists a CA-trust entry.
func (s *Service) CreateCAEntry(ctx context.Context, req CreateCAEntryRequest) (*access_entry.AccessEntry, error) {
	if err := s.assertTargetOwned(ctx, req.AccountID, req.OUIDs, req.NodeID); err != nil {
		return nil, err
	}
	e, err := access_entry.NewCAAccessEntry(req.AccountID, req.CAKeyID, req.OUIDs, req.NodeID)
	if err != nil {
		return nil, err
	}
	e.AccountWide = len(e.OUIDs) == 0 && req.NodeID == nil && req.AccountWide
	if err := s.checkKeyConformance(ctx, e); err != nil {
		return nil, err
	}
	if err := s.entryRepo.Create(ctx, e); err != nil {
		return nil, err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(req.AccountID, req.ActorUserID),
		audit_event.ActionAccessEntryCreateCA, audit_event.TargetAccessEntry, e.ID.String(),
		"trusted ca key "+req.CAKeyID.String()+"; "+scopeDetail(e))
	s.propagate(ctx, req.AccountID, e)
	return e, nil
}

// UpdateEntry applies the requested field changes and persists.
func (s *Service) UpdateEntry(ctx context.Context, req UpdateEntryRequest) (*access_entry.AccessEntry, error) {
	e, err := s.entryRepo.GetByID(ctx, req.AccountID, req.EntryID)
	if err != nil {
		return nil, err
	}
	if req.Sudo != nil {
		e.Sudo = *req.Sudo
	}
	if req.ClearPassword {
		e.Password = nil
	} else if req.Password != nil {
		e.Password = req.Password
	}
	if req.Principals != nil {
		e.Principals = *req.Principals
		if err := e.ValidatePrincipals(); err != nil {
			return nil, err
		}
	}
	if req.AdoptUnmanaged != nil {
		e.AdoptUnmanaged = *req.AdoptUnmanaged
	}
	// Asked only of a change that takes the account over or takes its
	// password away: detaching the last key stays the deliberate revoke it is
	// for a managed account, and an unrelated edit of such an entry is not
	// refused for it.
	if (req.AdoptUnmanaged != nil && *req.AdoptUnmanaged || req.ClearPassword) && e.AdoptsWithoutCredential() {
		return nil, access_entry.ErrAdoptNeedsCredential
	}

	// Re-scope, if requested. Capture the old scope first so the change can be
	// pushed to the nodes it USED to reach (the user is removed there) as well as
	// the nodes it now reaches. Ownership of the new target is asserted.
	var oldScope *access_entry.AccessEntry
	if req.Scope != nil {
		ouIDs := access_entry.DedupOUIDs(req.Scope.OUIDs)
		if len(ouIDs) > 0 && req.Scope.NodeID != nil {
			return nil, access_entry.ErrInvalidTarget
		}
		if err := s.assertTargetOwned(ctx, req.AccountID, ouIDs, req.Scope.NodeID); err != nil {
			return nil, err
		}
		if e.Type == access_entry.TypeUser {
			if err := s.assertUsernameFree(ctx, req.AccountID, e.Username, ouIDs, e.ID); err != nil {
				return nil, err
			}
		}
		oldScope = &access_entry.AccessEntry{OUIDs: e.OUIDs, NodeID: e.NodeID, AccountWide: e.AccountWide}
		e.OUIDs = ouIDs
		e.NodeID = req.Scope.NodeID
		e.AccountWide = len(ouIDs) == 0 && req.Scope.NodeID == nil && req.Scope.AccountWide
	}

	if err := e.Validate(); err != nil {
		return nil, err
	}
	if err := s.entryRepo.Update(ctx, e); err != nil {
		return nil, err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(req.AccountID, req.ActorUserID),
		audit_event.ActionAccessEntryUpdate, audit_event.TargetAccessEntry, e.ID.String(),
		updateDetail(req, e))
	if oldScope != nil {
		// Push to the nodes the entry no longer reaches so the OS user is
		// reconciled (removed/locked) there, in addition to the new scope below.
		s.propagate(ctx, req.AccountID, oldScope)
	}
	s.propagate(ctx, req.AccountID, e)
	return e, nil
}

func (s *Service) GetEntry(ctx context.Context, accountID, entryID uuid.UUID) (*access_entry.AccessEntry, error) {
	return s.entryRepo.GetByID(ctx, accountID, entryID)
}

func (s *Service) DeleteEntry(ctx context.Context, accountID, actorUserID, entryID uuid.UUID) error {
	// Capture the scope before deleting so the change can be pushed to the nodes
	// it affected.
	e, err := s.entryRepo.GetByID(ctx, accountID, entryID)
	if err != nil {
		return err
	}
	if err := s.entryRepo.Delete(ctx, accountID, entryID); err != nil {
		return err
	}
	// Recorded from the pre-delete copy: after the row is gone the id alone says
	// nothing, and "which OS user stopped existing on the fleet" is the question.
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionAccessEntryDelete, audit_event.TargetAccessEntry, entryID.String(),
		userEntryDetail(e))
	s.propagate(ctx, accountID, e)
	return nil
}

// propagate pushes an access change to the nodes it affects, so a create/update/
// revoke actually reaches the device instead of only changing the database. It
// is best-effort and non-fatal: apply_access_config is queued for offline nodes
// and skipped for too-old agents, and an access edit succeeds regardless.
func (s *Service) propagate(ctx context.Context, accountID uuid.UUID, e *access_entry.AccessEntry) {
	if s.applier == nil || e == nil {
		return
	}
	// The node set is resolved on the request (one query, and the count belongs
	// in the log line the operator will read); the per-node dispatch is not.
	s.dispatchDetached(ctx, "access entry change", accountID,
		s.affectedNodeIDs(ctx, accountID, e.OUIDs, e.NodeID, e.AccountWide))
}

// fleetDispatchTimeout bounds a detached propagation. Generous, because it is a
// sequential walk of an account's nodes and nobody is waiting on it, but finite:
// a goroutine per access edit that could outlive the process is not a thing to
// leave running.
const fleetDispatchTimeout = 10 * time.Minute

// dispatchDetached enqueues apply_access_config for each node OFF the request.
//
// Run inline, at 300 nodes it is ~1200 sequential queries against a
// 60-connection pool — past any proxy read timeout. The operator's experience
// of revoking an SSH key would be a 504 on a REVOCATION that had partially
// happened, with nothing recording where it stopped. Making it
// asynchronous is safe precisely because of what this command is:
// apply_access_config is queueable (it waits for an offline node) and superseding
// (a newer apply replaces an unsent older one), so late and out-of-order delivery
// is the designed behaviour, not a compromise.
//
// The context is DETACHED with context.WithoutCancel: the request's context is
// cancelled the moment the response is written, so inheriting it would cancel
// the propagation at almost exactly the point the old code returned — the same
// bug with a faster error page.
func (s *Service) dispatchDetached(ctx context.Context, reason string, accountID uuid.UUID, nodeIDs []uuid.UUID) {
	if s.applier == nil || len(nodeIDs) == 0 {
		return
	}
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
			if err := s.applier.EnqueueApplyAccessConfig(detached, accountID, nodeID); err != nil {
				failed++
				s.logger.Warn("failed to enqueue access apply", "node_id", nodeID, "error", err)
				continue
			}
			dispatched++
		}
		// The counts are the record the old inline loop never left. A revocation
		// that reached 297 of 300 nodes is a materially different situation from
		// one that reached all of them, and it used to be indistinguishable.
		if failed > 0 {
			s.logger.Warn("access propagation finished with failures",
				"reason", reason, "account_id", accountID, "nodes", len(nodeIDs),
				"dispatched", dispatched, "failed", failed)
			return
		}
		s.logger.Info("access propagation finished",
			"reason", reason, "account_id", accountID, "nodes", dispatched)
	}()
}

// PropagateKeyChange pushes an access apply to every node in the account after
// an SSH key was removed from the inventory (it may have been referenced by
// entries at any scope). Called by the ssh-key service.
func (s *Service) PropagateKeyChange(ctx context.Context, accountID uuid.UUID) {
	s.propagateAccountWide(ctx, accountID)
}

// propagateAccountWide pushes to every node in the account — used when the set
// of affected nodes cannot be narrowed (e.g. an SSH key removed from the
// inventory could be referenced by entries at any scope).
func (s *Service) propagateAccountWide(ctx context.Context, accountID uuid.UUID) {
	if s.applier == nil {
		return
	}
	nodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to list nodes for access apply", "error", err)
		return
	}
	nodeIDs := make([]uuid.UUID, 0, len(nodes))
	for i := range nodes {
		nodeIDs = append(nodeIDs, nodes[i].ID)
	}
	s.dispatchDetached(ctx, "ssh key removed from the inventory", accountID, nodeIDs)
}

// affectedNodeIDs resolves which nodes an entry's scope reaches: a node-scoped
// entry hits that node; a unit-scoped entry hits nodes in each of its OUs and
// their child OUs (which inherit it); an account-default (accountWide) entry
// hits every node; an unassigned entry (no target, not account-wide) hits none.
func (s *Service) affectedNodeIDs(ctx context.Context, accountID uuid.UUID, ouIDs []uuid.UUID, nodeID *uuid.UUID, accountWide bool) []uuid.UUID {
	if nodeID != nil {
		return []uuid.UUID{*nodeID}
	}
	if len(ouIDs) == 0 && !accountWide {
		// Unassigned entry: applies to no node, so nothing to push.
		return nil
	}
	nodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		s.logger.WarnContext(ctx, "failed to list nodes for access apply", "error", err)
		return nil
	}
	if len(ouIDs) == 0 {
		out := make([]uuid.UUID, len(nodes))
		for i := range nodes {
			out[i] = nodes[i].ID
		}
		return out
	}
	// Each OU itself plus its children (a child OU inherits the parent's entries).
	scope := make(map[uuid.UUID]bool, len(ouIDs))
	for _, ouID := range access_entry.DedupOUIDs(ouIDs) {
		scope[ouID] = true
	}
	if ous, err := s.ouRepo.GetAllByAccountID(ctx, accountID); err == nil {
		for i := range ous {
			if ous[i].ParentID != nil && scope[*ous[i].ParentID] {
				scope[ous[i].ID] = true
			}
		}
	}
	var out []uuid.UUID
	for i := range nodes {
		if nodes[i].OUID != nil && scope[*nodes[i].OUID] {
			out = append(out, nodes[i].ID)
		}
	}
	return out
}

func (s *Service) ListByOU(ctx context.Context, accountID, ouID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return s.entryRepo.ListByOU(ctx, accountID, ouID)
}

func (s *Service) ListByNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return s.entryRepo.ListByNode(ctx, accountID, nodeID)
}

func (s *Service) ListAccountLevel(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return s.entryRepo.ListAccountLevel(ctx, accountID)
}

// ListUnassigned returns entries with no target that are not account-wide — the
// holding bucket where a freshly created entry lands until it is assigned to a
// unit. They apply to no node.
func (s *Service) ListUnassigned(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return s.entryRepo.ListUnassigned(ctx, accountID)
}

// OUCounts returns per-OU access-entry counts for the credentials filter rail,
// plus the unassigned bucket, the account-default (fleet-wide) bucket and the
// account total. Counts are split by entry type (user vs ca).
func (s *Service) OUCounts(ctx context.Context, accountID uuid.UUID) (counts map[uuid.UUID]repository.AccessEntryCounts, unassigned, accountWide repository.AccessEntryCounts, total int64, err error) {
	return s.entryRepo.CountByOU(ctx, accountID)
}

// ListAllEntries returns every entry of the account regardless of scope — the
// CA trust overview shows, per CA, where it is trusted and with which
// principals, which needs the whole account in one read.
func (s *Service) ListAllEntries(ctx context.Context, accountID uuid.UUID) ([]access_entry.AccessEntry, error) {
	return s.entryRepo.ListAll(ctx, accountID)
}

// AttachKey attaches a single key to an entry after checking type conformance.
// Per-key (not whole-list) so a stale client cannot clobber a set it never saw.
func (s *Service) AttachKey(ctx context.Context, accountID, actorUserID, entryID, keyID uuid.UUID) error {
	e, err := s.entryRepo.GetByID(ctx, accountID, entryID)
	if err != nil {
		return err
	}
	// Validate the resulting key set (existing + new) against the entry type.
	probe := *e
	probe.KeyIDs = append(append([]uuid.UUID{}, e.KeyIDs...), keyID)
	if err := s.checkKeyConformance(ctx, &probe); err != nil {
		return err
	}
	if err := s.entryRepo.AttachKey(ctx, accountID, entryID, keyID); err != nil {
		return err
	}
	// The key id goes in the detail, not the target: the thing changed is the
	// entry, and an entry with several keys makes "a key was attached" useless.
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionAccessEntryKeyAttach, audit_event.TargetAccessEntry, entryID.String(),
		"key "+keyID.String()+" -> "+entryLabel(e))
	s.propagate(ctx, accountID, e)
	return nil
}

// DetachKey removes a single key from an entry. Detaching the last key of a
// user entry is allowed: it locks the user (derived state), it does not delete.
func (s *Service) DetachKey(ctx context.Context, accountID, actorUserID, entryID, keyID uuid.UUID) error {
	e, err := s.entryRepo.GetByID(ctx, accountID, entryID)
	if err != nil {
		return err
	}
	if err := s.entryRepo.DetachKey(ctx, accountID, entryID, keyID); err != nil {
		return err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionAccessEntryKeyDetach, audit_event.TargetAccessEntry, entryID.String(),
		"key "+keyID.String()+" from "+entryLabel(e))
	s.propagate(ctx, accountID, e)
	return nil
}

// ReapplyNode re-pushes the node's effective access config to the device on an
// explicit operator request — the manual companion to the automatic re-apply
// after recovery. Node ownership is asserted; applier errors are surfaced.
func (s *Service) ReapplyNode(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID) (bool, error) {
	if s.applier == nil {
		return false, errors.New("access apply is not wired on this install")
	}
	if _, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err != nil {
		return false, err
	}
	// Recorded even though it stores no new intent: it changes what the DEVICE
	// believes, so "nobody edited anything and the node's logins changed" has an
	// author. The dispatch itself is audited separately as a node command; this
	// row is the human decision that caused it.
	s.audit.Record(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionAccessReapplyNode, audit_event.TargetNode, nodeID.String())
	return s.applier.ReapplyAccessConfig(ctx, accountID, nodeID)
}

// ApplyOfflineToNode writes the node's effective access config onto its
// installed disk while the node is netbooted into recovery — the delivery path
// for a node whose live-OS agent never runs. Ownership is
// asserted; the netboot precondition and the target-disk resolution live in
// the node service, the reconciler itself on the agent.
func (s *Service) ApplyOfflineToNode(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID, targetDevice string, rootPartition int) error {
	if s.applier == nil {
		return errors.New("access apply is not wired on this install")
	}
	if _, err := s.nodeRepo.GetByID(ctx, accountID, nodeID); err != nil {
		return err
	}
	// Same reasoning as ReapplyNode's row: this changes what the DEVICE's disk
	// says about who may log in, so the human decision gets an author. The
	// detail names the disk because "which disk" is the one caller-supplied
	// input this command has.
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionAccessApplyOffline, audit_event.TargetNode, nodeID.String(),
		fmt.Sprintf("target_device=%s root_partition=%d", targetDevice, rootPartition))
	return s.applier.ApplyAccessConfigOffline(ctx, accountID, nodeID, targetDevice, rootPartition)
}

// ResolveEffectiveAccess builds the node's effective access set: node entries,
// then its OU, then the root OU, then the account level — merged with the union
// / nearest-username-override semantics. Passwords come back decrypted for
// internal command-building; callers redact before the API.
func (s *Service) ResolveEffectiveAccess(ctx context.Context, accountID, nodeID uuid.UUID) ([]services.ResolvedAccessEntry, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}

	levels := make([]accessLevel, 0, 4)

	nodeEntries, err := s.entryRepo.ListByNode(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	levels = append(levels, accessLevel{name: "node", entries: nodeEntries})

	if n.OUID != nil {
		ou, err := s.ouRepo.GetByID(ctx, accountID, *n.OUID)
		switch {
		case err == nil:
			ouEntries, err := s.entryRepo.ListByOU(ctx, accountID, ou.ID)
			if err != nil {
				return nil, err
			}
			levels = append(levels, accessLevel{name: "ou", ouID: &ou.ID, entries: ouEntries})

			if ou.ParentID != nil {
				rootEntries, err := s.entryRepo.ListByOU(ctx, accountID, *ou.ParentID)
				if err != nil {
					return nil, err
				}
				levels = append(levels, accessLevel{name: "root", ouID: ou.ParentID, entries: rootEntries})
			}
		case errors.Is(err, app_errors.ErrOrganizationalUnitNotFound):
			// A dangling ou_id degrades to "unassigned", loudly — mirroring
			// loadOUChain, which every other resolver already uses. Failing here
			// disabled ALL access management for the node: apply_access_config
			// could never be built, first-connect apply and the drift self-heal
			// warned-and-returned forever, and GET .../effective-access 500'd — so
			// an SSH-key revocation reported success and never landed.
			// The node still resolves against its node-level and account-level
			// entries, which is exactly what a genuinely unassigned node gets.
			s.logger.WarnContext(ctx, "node references an organizational unit that no longer exists; resolving access as unassigned",
				"account_id", accountID, "node_id", nodeID, "ou_id", *n.OUID)
		default:
			return nil, err
		}
	}

	accountEntries, err := s.entryRepo.ListAccountLevel(ctx, accountID)
	if err != nil {
		return nil, err
	}
	levels = append(levels, accessLevel{name: "account", entries: accountEntries})

	return mergeEffectiveAccess(levels), nil
}

// checkKeyConformance loads the referenced keys and verifies each one's type
// matches the entry (user→ssh, ca→exactly one ca key).
func (s *Service) checkKeyConformance(ctx context.Context, e *access_entry.AccessEntry) error {
	if len(e.KeyIDs) == 0 {
		// A ca entry with no key is rejected by ValidateKeyConformance below;
		// a user entry with no keys is a valid (locked) state.
		return e.ValidateKeyConformance(map[uuid.UUID]ssh_key.KeyType{})
	}
	keys, err := s.sshKeyRepo.GetByIDs(ctx, e.AccountID, e.KeyIDs)
	if err != nil {
		return err
	}
	types := make(map[uuid.UUID]ssh_key.KeyType, len(keys))
	for i := range keys {
		types[keys[i].ID] = keys[i].KeyType
	}
	return e.ValidateKeyConformance(types)
}

// --- audit detail helpers -------------------------------------------------
//
// These build the short note stored in AuditEvent.Detail. Everything they read
// is an identifier or a flag; NONE of them touch e.Password, which is the one
// field on an access entry that is encrypted at rest. An audit table is not a
// place a rotation would ever think to re-wrap.

// entryLabel names an entry the way an operator refers to it: the OS username
// for a user entry, the type for a CA (which has no name of its own).
func entryLabel(e *access_entry.AccessEntry) string {
	if e == nil {
		return "unknown entry"
	}
	if e.Type == access_entry.TypeUser && e.Username != "" {
		return "user " + e.Username
	}
	return string(e.Type) + " entry"
}

// scopeDetail describes where an entry applies. "unassigned" is a real state and
// worth recording: an entry parked with no target grants nothing, and the
// difference between that and account-wide is the difference between a no-op and
// a fleet-wide login.
func scopeDetail(e *access_entry.AccessEntry) string {
	switch {
	case e == nil:
		return "unknown scope"
	case e.NodeID != nil:
		return "scope node " + e.NodeID.String()
	case len(e.OUIDs) > 0:
		ids := make([]string, 0, len(e.OUIDs))
		for _, id := range e.OUIDs {
			ids = append(ids, id.String())
		}
		return "scope ous " + strings.Join(ids, ",")
	case e.AccountWide:
		return "scope account-wide"
	default:
		return "scope unassigned"
	}
}

// userEntryDetail summarises a user entry: who it is, whether it can sudo,
// whether it has a password at all (never the password), how many keys and
// principals it carries, and where it applies.
func userEntryDetail(e *access_entry.AccessEntry) string {
	if e == nil {
		return ""
	}
	parts := []string{entryLabel(e)}
	if e.Type == access_entry.TypeUser {
		if e.Sudo {
			parts = append(parts, "sudo")
		}
		if e.Password != nil {
			// Presence only. Whether a login prompts for a password changes
			// whether sudo is NOPASSWD, which is worth recording; the value is not.
			parts = append(parts, "password set")
		}
		parts = append(parts, fmt.Sprintf("keys=%d", len(e.KeyIDs)))
		if len(e.Principals) > 0 {
			parts = append(parts, fmt.Sprintf("principals=%d", len(e.Principals)))
		}
	}
	parts = append(parts, scopeDetail(e))
	return strings.Join(parts, "; ")
}

// updateDetail records WHICH fields an update touched, from the request rather
// than the result: "sudo changed" and "sudo re-sent unchanged" look identical in
// the stored entry, and only the first is a privilege change.
func updateDetail(req UpdateEntryRequest, e *access_entry.AccessEntry) string {
	var changed []string
	if req.Sudo != nil {
		changed = append(changed, fmt.Sprintf("sudo=%t", *req.Sudo))
	}
	if req.ClearPassword {
		changed = append(changed, "password cleared")
	} else if req.Password != nil {
		changed = append(changed, "password set")
	}
	if req.Principals != nil {
		changed = append(changed, fmt.Sprintf("principals=%d", len(*req.Principals)))
	}
	if req.AdoptUnmanaged != nil {
		changed = append(changed, fmt.Sprintf("adopt_unmanaged=%t", *req.AdoptUnmanaged))
	}
	if req.Scope != nil {
		changed = append(changed, "re-scoped to "+scopeDetail(e))
	}
	if len(changed) == 0 {
		// A PUT that changed nothing is still an attempt worth seeing in the trail.
		return entryLabel(e) + "; no field changed"
	}
	return entryLabel(e) + "; " + strings.Join(changed, ", ")
}
