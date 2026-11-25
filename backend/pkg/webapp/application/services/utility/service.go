// Package utility implements the Utilities application service
// (docs/UTILITIES-PLAN.md): assignment CRUD, per-node resolution over the
// node → OU → account chain, and the wholesale apply dispatch.
//
// Phase 2 of the design is in place: reconcile.go self-heals drift, fresh
// joiners and reimaged nodes on every managed-OS report, with the 3-strike cap
// the design specified. What is NOT implemented is the change-time PUSH — an
// assignment edit converges each node as it reports, not in one fan-out; an
// operator who wants a fleet converged now presses Apply.
package utility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// CommandDispatcher is the narrow slice of node.Service the apply needs.
// Wired via SetDispatcher in main.go (the setter pattern the gRPC layer uses)
// so the utility service does not depend on the whole node service at
// construction.
type CommandDispatcher interface {
	SendBatchCommand(ctx context.Context, cmd node_command_cmd.BatchNodeCommandCommand) (*node_command_cmd.BatchCommandResult, error)
}

// ActiveApplyCounter answers how many apply_utility_config commands are still
// live for an account — the fleet-pacing bound (docs/UTILITIES-PLAN.md §3.7).
type ActiveApplyCounter interface {
	CountActiveByAccountAndSlug(ctx context.Context, accountID uuid.UUID, slug string) (int64, error)
	// CountLiveByNodeAndSlug answers whether an apply already owns ONE node's
	// convergence — queued included, because a registered command is waiting
	// for exactly that node (the reconcile loop's guard).
	CountLiveByNodeAndSlug(ctx context.Context, accountID, nodeID uuid.UUID, slug string) (int64, error)
}

// SecretsResolver resolves a node's effective secrets for {{secrets.*}} /
// {{node.attributes.*}} references in utility params — the SAME interface the
// workflow engine uses (satisfied by the organizational unit service), so the
// two features cannot drift on what a reference means.
type SecretsResolver interface {
	ResolveEffectiveProvisioning(ctx context.Context, accountID, nodeID uuid.UUID) (services.EffectiveProvisioning, error)
}

// Service is the Utilities application service.
type Service struct {
	catalog     *utility.Catalog
	assignments repository.UtilityAssignmentRepository
	states      repository.NodeUtilityStateRepository
	ouRepo      repository.OrganizationalUnitRepository
	nodeRepo    repository.NodeRepository
	audit       *audit.Recorder
	logger      *slog.Logger

	dispatcher      CommandDispatcher
	applyCounter    ActiveApplyCounter
	secretsResolver SecretsResolver

	// maxAssignments caps utility_assignments rows per account (0 = unlimited;
	// refusals 409, count fails open — the caps doctrine).
	maxAssignments int
	// maxConcurrentApplies bounds live apply commands per account, because one
	// apply pins its node to maintenance for up to 30 minutes and an unbounded
	// fan-out is a fleet-wide outage from a single click (0 = unlimited).
	maxConcurrentApplies int
}

// NewService constructs the service. Required deps panic when missing — the
// repo-wide DI rule.
func NewService(
	catalog *utility.Catalog,
	assignments repository.UtilityAssignmentRepository,
	states repository.NodeUtilityStateRepository,
	ouRepo repository.OrganizationalUnitRepository,
	nodeRepo repository.NodeRepository,
	auditRecorder *audit.Recorder,
	logger *slog.Logger,
	maxAssignments int,
	maxConcurrentApplies int,
) *Service {
	if catalog == nil || assignments == nil || states == nil || ouRepo == nil || nodeRepo == nil {
		panic("utility.Service requires catalog, assignment, state, OU and node repositories")
	}
	if auditRecorder == nil {
		panic("utility.Service requires an audit recorder")
	}
	return &Service{
		catalog:              catalog,
		assignments:          assignments,
		states:               states,
		ouRepo:               ouRepo,
		nodeRepo:             nodeRepo,
		audit:                auditRecorder,
		logger:               logger.With("service", "utility"),
		maxAssignments:       maxAssignments,
		maxConcurrentApplies: maxConcurrentApplies,
	}
}

// SetDispatcher wires the command dispatch (node.Service) after construction.
func (s *Service) SetDispatcher(d CommandDispatcher) { s.dispatcher = d }

// SetActiveApplyCounter wires the in-flight apply count source.
func (s *Service) SetActiveApplyCounter(c ActiveApplyCounter) { s.applyCounter = c }

// SetSecretsResolver wires {{secrets.*}}/{{node.attributes.*}} resolution.
func (s *Service) SetSecretsResolver(r SecretsResolver) { s.secretsResolver = r }

// Catalog returns the loaded registry.
func (s *Service) Catalog() *utility.Catalog { return s.catalog }

// --- assignment CRUD -----------------------------------------------------

// AssignmentInput is the create/update shape. Scope fields follow the
// assignment XOR; a nil Config means "no params set" (schema defaults apply at
// resolution).
type AssignmentInput struct {
	UtilityID   string
	AccountWide bool
	OUIDs       []uuid.UUID
	NodeID      *uuid.UUID
	Config      map[string]interface{}
	Enabled     *bool
}

func (s *Service) validateInput(ctx context.Context, accountID uuid.UUID, in AssignmentInput) (utility.Definition, error) {
	def, ok := s.catalog.Get(in.UtilityID)
	if !ok {
		return def, utility.ErrUnknownUtility
	}
	if in.Config != nil {
		if err := def.ValidateConfig(in.Config); err != nil {
			return def, fmt.Errorf("invalid config: %w", err)
		}
	}
	// Targets must exist and belong to the account — a dangling reference at
	// CREATE is an operator error, unlike one appearing later (tolerated by
	// resolution).
	for _, ouID := range in.OUIDs {
		if _, err := s.ouRepo.GetByID(ctx, accountID, ouID); err != nil {
			return def, fmt.Errorf("target organizational unit %s: %w", ouID, err)
		}
	}
	if in.NodeID != nil {
		if _, err := s.nodeRepo.GetByID(ctx, accountID, *in.NodeID); err != nil {
			return def, fmt.Errorf("target node: %w", err)
		}
	}
	return def, nil
}

// checkOUOverlap refuses an OU set that shares a unit with ANOTHER assignment
// of the same utility — resolution must never have ties, and with a SET scope
// that rule cannot be a DB index. This is the EARLY, friendly check; the
// authority under concurrency is the repository, which re-runs the same rule
// inside the write transaction under the account's advisory lock
// (refuseOUOverlapLocked) — two concurrent creates both passing here settle
// there, one of them with ErrDuplicateAssignment. excludeID skips the
// assignment being edited.
func (s *Service) checkOUOverlap(ctx context.Context, accountID uuid.UUID, utilityID string, ouIDs []uuid.UUID, excludeID uuid.UUID) error {
	if len(ouIDs) == 0 {
		return nil
	}
	all, err := s.assignments.ListByAccount(ctx, accountID)
	if err != nil {
		// Fail CLOSED: a tie the check could not see is a nondeterministic
		// fleet config, worse than a refused save.
		return fmt.Errorf("could not verify unit overlap: %w", err)
	}
	want := make(map[uuid.UUID]bool, len(ouIDs))
	for _, id := range ouIDs {
		want[id] = true
	}
	for _, other := range all {
		if other.ID == excludeID || other.UtilityID != utilityID {
			continue
		}
		for _, ou := range other.OUIDs {
			if want[ou] {
				return fmt.Errorf("%w: another assignment of %s already covers one of these units",
					utility.ErrDuplicateAssignment, utilityID)
			}
		}
	}
	return nil
}

// CreateAssignment creates one utility × one target × one config row.
func (s *Service) CreateAssignment(ctx context.Context, accountID, userID uuid.UUID, in AssignmentInput) (*utility.Assignment, error) {
	def, err := s.validateInput(ctx, accountID, in)
	if err != nil {
		return nil, err
	}
	if err := def.ValidateRanges(in.Config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	a := &utility.Assignment{
		ID:          uuid.New(),
		AccountID:   accountID,
		UtilityID:   def.ID,
		AccountWide: in.AccountWide,
		OUIDs:       in.OUIDs,
		NodeID:      in.NodeID,
		Config:      in.Config,
		Enabled:     true,
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if err := a.ValidateScope(); err != nil {
		return nil, err
	}
	if err := s.checkOUOverlap(ctx, accountID, a.UtilityID, a.OUIDs, uuid.Nil); err != nil {
		return nil, err
	}
	// The cap fails OPEN on a count error, like every count cap in this repo.
	if s.maxAssignments > 0 {
		if count, err := s.assignments.CountByAccount(ctx, accountID); err == nil && count >= int64(s.maxAssignments) {
			return nil, &AssignmentCapError{Max: s.maxAssignments}
		}
	}
	// Stamped here so the answer carries the times the row is stored with: the
	// columns' database defaults are not read back, and the response used to
	// say 0001-01-01. Microseconds are what the column keeps.
	now := time.Now().UTC().Truncate(time.Microsecond)
	a.CreatedAt, a.UpdatedAt = now, now
	if err := s.assignments.Create(ctx, a); err != nil {
		return nil, err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, userID),
		audit_event.ActionUtilityAssignmentCreate, audit_event.TargetUtilityAssignment, a.ID.String(),
		fmt.Sprintf("utility=%s scope=%s", a.UtilityID, a.Scope()))
	return a, nil
}

// UpdateAssignment edits config/enabled and optionally re-targets. Validation
// runs on the MERGED row (a merge invalidates checks written against the
// submission).
type AssignmentUpdate struct {
	Config  map[string]interface{} // nil = unchanged
	Enabled *bool                  // nil = unchanged
	// Retarget, when true, replaces the scope with the fields below (all-nil =
	// unassigned). When false the scope fields are ignored.
	Retarget    bool
	AccountWide bool
	OUIDs       []uuid.UUID
	NodeID      *uuid.UUID
}

func (s *Service) UpdateAssignment(ctx context.Context, accountID, userID, id uuid.UUID, up AssignmentUpdate) (*utility.Assignment, error) {
	a, err := s.assignments.GetByID(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	if up.Config != nil {
		a.Config = up.Config
	}
	if up.Enabled != nil {
		a.Enabled = *up.Enabled
	}
	if up.Retarget {
		a.AccountWide = up.AccountWide
		a.OUIDs = up.OUIDs
		a.NodeID = up.NodeID
	}
	def, err := s.validateInput(ctx, accountID, AssignmentInput{
		UtilityID: a.UtilityID,
		OUIDs:     a.OUIDs,
		NodeID:    a.NodeID,
		Config:    a.Config,
	})
	if err != nil {
		return nil, err
	}
	// Ranges are checked only when this update brings a config: an enable
	// toggle or a re-target must not be refused for a stored value that
	// predates the bound.
	if up.Config != nil {
		if err := def.ValidateRanges(up.Config); err != nil {
			return nil, fmt.Errorf("invalid config: %w", err)
		}
	}
	if err := a.ValidateScope(); err != nil {
		return nil, err
	}
	if err := s.checkOUOverlap(ctx, accountID, a.UtilityID, a.OUIDs, a.ID); err != nil {
		return nil, err
	}
	a.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	if err := s.assignments.Update(ctx, a); err != nil {
		return nil, err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, userID),
		audit_event.ActionUtilityAssignmentUpdate, audit_event.TargetUtilityAssignment, a.ID.String(),
		fmt.Sprintf("utility=%s scope=%s enabled=%t", a.UtilityID, a.Scope(), a.Enabled))
	return a, nil
}

// DeleteAssignment removes the row. Unassign-is-retain: nothing is dispatched
// and nothing changes on any device — the software stays, unmanaged
// (docs/UTILITIES-PLAN.md §2.3). Uninstall is a separate, explicitly consented
// action that does not exist in phase 1.
func (s *Service) DeleteAssignment(ctx context.Context, accountID, userID, id uuid.UUID) error {
	a, err := s.assignments.GetByID(ctx, accountID, id)
	if err != nil {
		return err
	}
	if err := s.assignments.Delete(ctx, accountID, id); err != nil {
		return err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, userID),
		audit_event.ActionUtilityAssignmentDelete, audit_event.TargetUtilityAssignment, id.String(),
		fmt.Sprintf("utility=%s scope=%s (retained on devices)", a.UtilityID, a.Scope()))
	return nil
}

// ListAssignments returns every assignment of the account.
// ListAssignmentsForScope narrows the account's assignments to what an
// OU-scoped caller may see: assignments targeting an allowed unit, and
// node-scoped assignments whose node sits in one. Account-wide and unassigned
// rows are visible to account-wide holders only — the AllowedOUs doctrine
// ("OU-less resources are shown to account-wide holders only"), which is what
// keeps opening utility.read to OU-scoped bindings from becoming account-wide
// enumeration.
func (s *Service) ListAssignmentsForScope(ctx context.Context, accountID uuid.UUID, allowAll bool, allowed map[uuid.UUID]bool) ([]utility.Assignment, error) {
	all, err := s.assignments.ListByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if allowAll {
		return all, nil
	}
	// One bulk read for the node-scoped rows, not one per assignment.
	nodeOU := map[uuid.UUID]*uuid.UUID{}
	if nodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID); err == nil {
		for i := range nodes {
			nodeOU[nodes[i].ID] = nodes[i].OUID
		}
	} else {
		// Fail CLOSED for the rows this read was for: without it a node-scoped
		// assignment cannot be attributed to a unit, and showing it would be
		// the enumeration this filter exists to prevent.
		s.logger.WarnContext(ctx, "could not resolve node units; node-scoped assignments are omitted from this listing", "error", err)
	}
	out := make([]utility.Assignment, 0, len(all))
	for _, a := range all {
		switch a.Scope() {
		case utility.ScopeOU:
			for _, ou := range a.OUIDs {
				if allowed[ou] {
					out = append(out, a)
					break
				}
			}
		case utility.ScopeNode:
			if ou := nodeOU[*a.NodeID]; ou != nil && allowed[*ou] {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

// NodeOU resolves the unit a node belongs to, for per-node authorization.
func (s *Service) NodeOU(ctx context.Context, accountID, nodeID uuid.UUID) (*uuid.UUID, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	return n.OUID, nil
}

func (s *Service) ListAssignments(ctx context.Context, accountID uuid.UUID) ([]utility.Assignment, error) {
	return s.assignments.ListByAccount(ctx, accountID)
}

// GetAssignment returns one assignment.
func (s *Service) GetAssignment(ctx context.Context, accountID, id uuid.UUID) (*utility.Assignment, error) {
	return s.assignments.GetByID(ctx, accountID, id)
}

// OUCounts returns per-OU assignment counts for the rail facet.
func (s *Service) OUCounts(ctx context.Context, accountID uuid.UUID) (map[uuid.UUID]int64, error) {
	return s.assignments.OUCounts(ctx, accountID)
}

// --- resolution ----------------------------------------------------------

// ouChainOf returns the node's OU lineage nearest-first (own unit, then its
// parent). A dangling ou_id resolves as NO chain with a warning, never an
// error: a stale reference degrades to no inheritance instead of failing
// every caller that resolves the node.
func (s *Service) ouChainOf(ctx context.Context, accountID uuid.UUID, n *node.Node) []uuid.UUID {
	if n.OUID == nil {
		return nil
	}
	own, err := s.ouRepo.GetByID(ctx, accountID, *n.OUID)
	if err != nil {
		s.logger.WarnContext(ctx, "node references a missing organizational unit; resolving utilities as unassigned",
			"node_id", n.ID, "ou_id", *n.OUID, "error", err)
		return nil
	}
	chain := []uuid.UUID{own.ID}
	if own.ParentID != nil {
		chain = append(chain, *own.ParentID)
	}
	return chain
}

// ResolveForNode returns the node's effective utility set.
func (s *Service) ResolveForNode(ctx context.Context, accountID, nodeID uuid.UUID) ([]utility.Resolved, *node.Node, error) {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	assignments, err := s.assignments.ListByAccount(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	set := utility.ResolveForNode(s.catalog, assignments, nodeID, s.ouChainOf(ctx, accountID, n))
	return set, n, nil
}

// EffectiveState is the node sidebar's answer: the resolved set with ladder
// rungs and provenance.
type EffectiveState struct {
	Resolved []utility.Resolved
	States   []utility.NodeState
}

// EffectiveForNode returns the resolved set plus the stored ladder.
//
// The HASHES are computed the way the APPLY computes them — over params with
// {{secrets.*}}/{{node.attributes.*}} resolved — or the view compares an
// unresolved desired hash against a resolved applied one and shows
// update_pending forever for any config carrying a reference. Only the hash
// is carried over: the returned Config keeps the
// UNRESOLVED references, because the view answers at utility.read and resolved
// values in it would widen what a reader sees (the payload-readback concern).
// A config whose references cannot resolve keeps its unresolved hash — the
// apply would refuse it too, so update_pending is the honest answer there.
func (s *Service) EffectiveForNode(ctx context.Context, accountID, nodeID uuid.UUID) (*EffectiveState, error) {
	set, _, err := s.ResolveForNode(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	if resolved, rerr := s.resolveParamRefs(ctx, accountID, nodeID, set); rerr == nil {
		hashByID := make(map[string]string, len(resolved))
		for _, r := range resolved {
			hashByID[r.Definition.ID] = r.ConfigHash
		}
		for i := range set {
			if h, ok := hashByID[set[i].Definition.ID]; ok {
				set[i].ConfigHash = h
			}
		}
	} else {
		s.logger.WarnContext(ctx, "effective view: param references unresolvable; hashes shown over unresolved params",
			"node_id", nodeID, "error", rerr)
	}
	states, err := s.states.ListByNode(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	return &EffectiveState{Resolved: set, States: states}, nil
}

// --- apply ---------------------------------------------------------------

// NodeApplyResult is one node's outcome of an apply request.
type NodeApplyResult struct {
	NodeID    uuid.UUID
	NodeName  string
	CommandID *uuid.UUID
	Queued    bool
	Skipped   bool
	Reason    string
}

// ApplyToNode resolves the node's set and dispatches ONE wholesale
// apply_utility_config. An empty resolved set dispatches nothing
// (unassign-is-retain) and only prunes stale desired rungs.
func (s *Service) ApplyToNode(ctx context.Context, accountID, userID, nodeID uuid.UUID) (*NodeApplyResult, error) {
	return s.applyToNode(ctx, accountID, userID, nodeID, nil)
}

// applyToNode is ApplyToNode plus an optional canary marker, which rides the
// dispatch payload (platform-side only — the proto builder forwards
// union_hash and utilities alone, so the agent never sees it) and is what
// SettleApply reads to continue a canary-first fan-out.
func (s *Service) applyToNode(ctx context.Context, accountID, userID, nodeID uuid.UUID, canary map[string]interface{}) (*NodeApplyResult, error) {
	if s.dispatcher == nil {
		return nil, fmt.Errorf("utility apply is not available: dispatcher not wired")
	}
	set, n, err := s.ResolveForNode(ctx, accountID, nodeID)
	if err != nil {
		return nil, err
	}
	res := &NodeApplyResult{NodeID: nodeID, NodeName: n.Name}
	if n.IsWatcher() {
		// The dispatch gate would refuse anyway (WatcherNodeError); answering
		// here names the reason in the per-node report instead of a 409.
		res.Skipped, res.Reason = true, "watcher node"
		return res, nil
	}
	// Per-utility floors (Definition.MinAgentVersion): a utility whose applier
	// ships in a LATER agent release than the slug's handler. Checked here —
	// against the same fail-closed comparison the slug floor uses — so the
	// skip names the utility and the remedy instead of dispatching a set the
	// agent will partially refuse. Not a new mechanism: the slug floor still
	// runs at dispatch and redelivery as for every command.
	for _, r := range set {
		if min := r.Definition.MinAgentVersion; min != "" &&
			!node_command.MeetsMinAgentVersion(n.AgentVersion, min) {
			res.Skipped, res.Reason = true, fmt.Sprintf(
				"%s needs agent %s or newer (node reports %q) — update the agent",
				r.Definition.ID, min, n.AgentVersion)
			return res, nil
		}
	}
	if len(set) == 0 {
		if err := s.states.DeleteMissing(ctx, accountID, nodeID, nil); err != nil {
			s.logger.WarnContext(ctx, "failed to prune utility state", "node_id", nodeID, "error", err)
		}
		res.Skipped, res.Reason = true, "no utilities resolve to this node"
		return res, nil
	}
	if err := utility.CheckSetConstraints(set); err != nil {
		return nil, err
	}
	// An assignment saved before its schema tightened (a param made required,
	// a default removed — e.g. a kiosk image param) can resolve to a config
	// the registry no longer accepts. Such a utility is DROPPED from this
	// dispatch and its own rung records the reason; the rest of the set still
	// converges.
	//
	// Dropping rather than refusing the whole node is the settle path's rule
	// applied one step earlier: one failing utility must not redden — or here,
	// hold back — the siblings that are fine. Refusing wholesale meant a single
	// stale config silently blocked every other utility on that node forever,
	// on every reconcile, which is the worse failure by far. Recording the
	// failure also feeds the reconcile loop's 3-strike cap, so a config nobody
	// fixes stops being retried instead of being retried invisibly.
	kept := make([]utility.Resolved, 0, len(set))
	var invalid []string
	for _, r := range set {
		if err := r.Definition.ValidateConfig(r.Config); err != nil {
			reason := fmt.Sprintf("%v — edit the assignment", err)
			invalid = append(invalid, r.Definition.ID+": "+reason)
			if recErr := s.states.RecordFailed(ctx, accountID, nodeID, r.Definition.ID, truncateReason(reason)); recErr != nil {
				s.logger.WarnContext(ctx, "failed to record an invalid utility config",
					"node_id", nodeID, "utility", r.Definition.ID, "error", recErr)
			}
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == 0 {
		// Nothing left to dispatch: the per-node report carries the reasons
		// rather than a bare "nothing resolves", which would be a lie.
		res.Skipped, res.Reason = true, strings.Join(invalid, "; ")
		return res, nil
	}
	if len(invalid) > 0 {
		s.logger.WarnContext(ctx, "dispatching without utilities whose stored config is no longer valid",
			"node_id", nodeID, "dropped", invalid)
	}
	set = kept
	// The PRE-resolution hashes, captured before resolveParamRefs replaces
	// them: this is what the OU rollup compares against without resolving
	// secrets per node (NodeState.DesiredSourceHash).
	sourceHash := make(map[string]string, len(set))
	for _, r := range set {
		sourceHash[r.Definition.ID] = r.ConfigHash
	}
	// {{secrets.*}}/{{node.attributes.*}} references resolve per node BEFORE
	// hashing, so a changed attribute honestly moves the desired hash.
	set, err = s.resolveParamRefs(ctx, accountID, nodeID, set)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", utility.ErrInvalidConfig, err)
	}
	// Dependencies before dependents in the dispatched payload — the agent
	// applies in payload order.
	set = utility.SortByDependencies(set)

	// Record the desired rungs BEFORE dispatch, and prune utilities that left
	// the set, so the ladder shows what the platform currently wants even when
	// the dispatch queues for an offline node.
	keep := make([]string, 0, len(set))
	for _, r := range set {
		keep = append(keep, r.Definition.ID)
		if err := s.states.SetDesired(ctx, accountID, nodeID, r.Definition.ID, r.ConfigHash, sourceHash[r.Definition.ID]); err != nil {
			return nil, fmt.Errorf("failed to record desired state: %w", err)
		}
	}
	if err := s.states.DeleteMissing(ctx, accountID, nodeID, keep); err != nil {
		s.logger.WarnContext(ctx, "failed to prune utility state", "node_id", nodeID, "error", err)
	}

	utilities := make([]interface{}, 0, len(set))
	secretDerived := false
	for _, r := range set {
		raw, err := json.Marshal(r.Config)
		if err != nil {
			return nil, fmt.Errorf("failed to encode %s config: %w", r.Definition.ID, err)
		}
		if r.ParamsSecretDerived {
			secretDerived = true
		}
		utilities = append(utilities, map[string]interface{}{
			"utility_id":  r.Definition.ID,
			"config_hash": r.ConfigHash,
			"params_json": string(raw),
		})
	}
	// A resolved {{secrets.*}} / {{node.attributes.*}} value rides inside
	// params_json, so it lands in a payload every node.read caller can list —
	// Tailscale's auth_key being the motivating case. Marking the payload sends
	// it through the SAME redaction the workflow engine's blocks use.
	//
	// The mark names the whole "utilities" key, not one utility, because the
	// values sit nested inside a JSON STRING that the generic redactor cannot
	// reach into — and teaching it this payload's shape would make one module
	// know about every other. The cost is that a node.read caller loses the
	// utility ids and hashes for that one command; the effective view
	// (utility.read) and the per-node ladder still carry them, and both are
	// where the UI reads them from.
	var derivedParams []string
	if secretDerived {
		derivedParams = []string{"utilities"}
	}
	params := map[string]interface{}{
		"union_hash": utility.UnionHash(set),
		"utilities":  utilities,
	}
	if canary != nil {
		params["canary"] = canary
	}
	batch, err := s.dispatcher.SendBatchCommand(ctx, node_command_cmd.BatchNodeCommandCommand{
		NodeIDs:             []uuid.UUID{nodeID},
		Command:             node_command.SlugApplyUtilityConfig,
		Params:              params,
		SecretDerivedParams: derivedParams,
		AuthContext:         commands.NewAuthContext(accountID, userID),
	})
	if err != nil {
		return nil, err
	}
	item := batch.Results[0]
	if !item.Success {
		if item.Skipped {
			res.Skipped, res.Reason = true, "agent below the utility floor — update the agent"
			return res, nil
		}
		return nil, item.FailureError()
	}
	if item.CommandID == nil {
		res.Queued = true // held for an offline or netbooted node, delivered on reconnect
		return res, nil
	}
	res.CommandID = item.CommandID
	return res, nil
}

// ApplyAssignment fans the apply out to every node the assignment reaches,
// bounded by the per-account concurrent-applies cap. Nodes past the bound are
// reported deferred, not silently dropped (the no-silent-caps rule).
func (s *Service) ApplyAssignment(ctx context.Context, accountID, userID, assignmentID uuid.UUID) ([]NodeApplyResult, error) {
	a, err := s.assignments.GetByID(ctx, accountID, assignmentID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.targetNodes(ctx, accountID, a)
	if err != nil {
		return nil, err
	}
	allowed := s.allowedUnderPacing(ctx, accountID, len(nodes))

	// Canary-first ordering (docs/UTILITIES-PLAN.md §3.7): a utility apply is
	// heavy and pins the node to maintenance, so a multi-node fan-out
	// dispatches to ONE node first and parks the rest. SettleApply reads the
	// canary marker off the settled command's payload and dispatches the
	// remainder — only on a SUCCESSFUL settle, so a config that bricks the
	// canary stops there. Deliberately not durable: if the backend restarts
	// between canary settle and continuation, or the canary fails, the parked
	// nodes stay parked until the operator re-runs the apply (idempotent —
	// converged nodes settle cheaply, and a re-run re-canaries).
	if len(nodes) > 1 {
		return s.canaryFanOut(ctx, accountID, userID, a.ID, nodes, allowed)
	}
	results, _ := s.fanOutToNodes(ctx, accountID, userID, nodes, allowed, nil)
	return results, nil
}

// allowedUnderPacing returns how many of targets may dispatch now under the
// per-account in-flight bound. Fails open, like every count cap — but says so.
func (s *Service) allowedUnderPacing(ctx context.Context, accountID uuid.UUID, targets int) int {
	allowed := targets
	if s.maxConcurrentApplies > 0 && s.applyCounter != nil {
		inflight, err := s.applyCounter.CountActiveByAccountAndSlug(ctx, accountID, node_command.SlugApplyUtilityConfig)
		if err != nil {
			s.logger.WarnContext(ctx, "in-flight apply count failed; pacing bound not applied", "error", err)
		} else {
			allowed = s.maxConcurrentApplies - int(inflight)
			if allowed < 0 {
				allowed = 0
			}
			if allowed < targets {
				s.logger.WarnContext(ctx, "utility apply pacing bound engaged",
					"account_id", accountID, "in_flight", inflight,
					"bound", s.maxConcurrentApplies, "targets", targets, "dispatching", allowed)
			}
		}
	}
	return allowed
}

// fanOutToNodes dispatches to each node under the pacing bound. The canary
// marker, when non-nil, rides every dispatched command's payload.
func (s *Service) fanOutToNodes(ctx context.Context, accountID, userID uuid.UUID, nodes []node.Node, allowed int, canary map[string]interface{}) ([]NodeApplyResult, int) {
	results := make([]NodeApplyResult, 0, len(nodes))
	dispatched := 0
	for _, n := range nodes {
		if dispatched >= allowed {
			results = append(results, NodeApplyResult{
				NodeID: n.ID, NodeName: n.Name, Skipped: true,
				Reason: "deferred: concurrent-apply bound reached — re-run once in-flight applies settle",
			})
			continue
		}
		r, err := s.applyToNode(ctx, accountID, userID, n.ID, canary)
		if err != nil {
			results = append(results, NodeApplyResult{NodeID: n.ID, NodeName: n.Name, Skipped: true, Reason: err.Error()})
			continue
		}
		if !r.Skipped {
			dispatched++
		}
		results = append(results, *r)
	}
	return results, dispatched
}

// canaryFanOut dispatches to the first node that will take the command and
// parks the rest behind it. Connected nodes are tried first: a queued canary
// on an offline node still gates correctly (it settles when the node returns)
// but holds the whole fan-out for as long as the node is away.
func (s *Service) canaryFanOut(ctx context.Context, accountID, userID, assignmentID uuid.UUID, nodes []node.Node, allowed int) ([]NodeApplyResult, error) {
	ordered := make([]node.Node, 0, len(nodes))
	var offline []node.Node
	for _, n := range nodes {
		if n.IsInConnectedState() {
			ordered = append(ordered, n)
		} else {
			offline = append(offline, n)
		}
	}
	ordered = append(ordered, offline...)

	marker := map[string]interface{}{
		"assignment_id": assignmentID.String(),
		"user_id":       userID.String(),
	}
	results := make([]NodeApplyResult, 0, len(nodes))
	canaryAt := -1
	for i, n := range ordered {
		if allowed < 1 {
			break
		}
		r, err := s.applyToNode(ctx, accountID, userID, n.ID, marker)
		if err != nil {
			results = append(results, NodeApplyResult{NodeID: n.ID, NodeName: n.Name, Skipped: true, Reason: err.Error()})
			continue
		}
		results = append(results, *r)
		if !r.Skipped {
			canaryAt = i
			break
		}
	}
	if canaryAt == -1 {
		// Nothing would take the command (every candidate skipped or failed);
		// there is no canary to park behind, so the per-node reasons stand on
		// their own.
		return results, nil
	}
	canaryName := ordered[canaryAt].Name
	for _, n := range ordered[canaryAt+1:] {
		results = append(results, NodeApplyResult{
			NodeID: n.ID, NodeName: n.Name, Skipped: true,
			Reason: fmt.Sprintf("parked: canary-first fan-out — dispatches automatically when %s settles successfully", canaryName),
		})
	}
	return results, nil
}

// targetNodes lists the nodes an assignment reaches: its node, its OU's nodes
// plus child-unit nodes (the inheritance chain REACHES those unless a nearer
// level overrides — and even then the nearer level is what the apply
// resolves), or the whole account.
func (s *Service) targetNodes(ctx context.Context, accountID uuid.UUID, a *utility.Assignment) ([]node.Node, error) {
	switch a.Scope() {
	case utility.ScopeNode:
		n, err := s.nodeRepo.GetByID(ctx, accountID, *a.NodeID)
		if err != nil {
			return nil, err
		}
		return []node.Node{*n}, nil
	case utility.ScopeOU:
		reached := make(map[uuid.UUID]bool, len(a.OUIDs))
		for _, ouID := range a.OUIDs {
			reached[ouID] = true
		}
		all, err := s.ouRepo.GetAllByAccountID(ctx, accountID)
		if err != nil {
			// Fail CLOSED: proceeding without the child expansion silently
			// drops every child-unit node from the fan-out, and a partial
			// apply that looks complete is worse than a failed one.
			return nil, fmt.Errorf("could not enumerate organizational units for the apply fan-out: %w", err)
		}
		for _, ou := range all {
			if ou.ParentID != nil && reached[*ou.ParentID] {
				reached[ou.ID] = true
			}
		}
		nodes, err := s.nodeRepo.GetAllByAccountID(ctx, accountID)
		if err != nil {
			return nil, err
		}
		out := make([]node.Node, 0)
		for _, n := range nodes {
			if n.OUID != nil && reached[*n.OUID] {
				out = append(out, n)
			}
		}
		return out, nil
	case utility.ScopeAccount:
		return s.nodeRepo.GetAllByAccountID(ctx, accountID)
	default:
		return nil, nil // unassigned reaches nothing
	}
}

// --- settle (called from the gRPC terminal-status hook) ------------------

// SettleApply records per-utility outcomes from a terminal apply. Hashes come
// from the command's OWN payload — what was dispatched — never from the
// agent's echo (the access-generation rule). Per-utility markers in the result
// map (utility.<id> = "applied" | "failed: ...") decide each rung; a terminal
// FAILURE with no marker fails every utility of the payload, because an agent
// that died mid-apply converged nothing it did not explicitly claim.
// ForgetAppliedAfterReimage drops the node's whole ladder after a successful
// install_os.
//
// The recorded hashes describe software on a disk that has just been written
// over, so every rung would otherwise keep reading converged while nothing is
// installed — the "reimaged node keeps a green ladder" case. Dropping the rows
// rather than clearing one column: DESIRED is recomputed and re-stored by the
// next apply anyway, and a row with a desired hash and no applied one says
// exactly the same thing as no row at all.
//
// It dispatches nothing: the node's next managed-OS report reaches reconcile
// (reconcile.go), which finds no ladder rows and converges the node as a first
// connect — one trigger, fired only once the new OS is known to be managed.
func (s *Service) ForgetAppliedAfterReimage(ctx context.Context, accountID, nodeID uuid.UUID) error {
	return s.states.DeleteByNode(ctx, accountID, nodeID)
}

func (s *Service) SettleApply(ctx context.Context, cmd *node_command.NodeCommand, status string, result map[string]string) {
	if cmd == nil || cmd.Slug != node_command.SlugApplyUtilityConfig {
		return
	}
	// A late terminal report must not RESURRECT a pruned ladder row: the
	// assignment may have been deleted or re-targeted while the command was in
	// flight (a queued apply waits up to its 24 h TTL), and Record* re-creates
	// the row it settles. Guard on the node's CURRENT membership. Fail-open on
	// a resolution error — losing a real settle over a transient read beats
	// nothing, and the ladder self-corrects on the next apply — except when
	// the NODE is gone: CleanupNode has dropped the whole ladder, and a settle
	// for a deleted node must record nothing at all.
	var current map[string]bool
	if resolved, _, err := s.ResolveForNode(ctx, cmd.AccountID, cmd.NodeID); err == nil {
		current = make(map[string]bool, len(resolved))
		for _, r := range resolved {
			current[r.Definition.ID] = true
		}
	} else if errors.Is(err, app_errors.ErrNodeNotFound) {
		s.logger.InfoContext(ctx, "dropping a utility settle for a deleted node", "node_id", cmd.NodeID)
		return
	} else {
		s.logger.WarnContext(ctx, "could not resolve current utilities for settle; recording unguarded", "node_id", cmd.NodeID, "error", err)
	}
	rawList, _ := cmd.Payload["utilities"].([]interface{})
	for _, raw := range rawList {
		entry, _ := raw.(map[string]interface{})
		id, _ := entry["utility_id"].(string)
		hash, _ := entry["config_hash"].(string)
		if id == "" || hash == "" {
			continue
		}
		if current != nil && !current[id] {
			s.logger.InfoContext(ctx, "skipping settle for a utility no longer assigned to this node",
				"node_id", cmd.NodeID, "utility", id)
			continue
		}
		marker := result["utility."+id]
		switch {
		case marker == "applied":
			// The marker decides the RUNG, the payload decides the HASH — and
			// the marker counts regardless of the command's overall status:
			// the wholesale command reports failed when ANY utility fails, and
			// treating that as failure for the siblings that converged is
			// exactly the coupling the per-utility ladder exists to prevent.
			if err := s.states.RecordApplied(ctx, cmd.AccountID, cmd.NodeID, id, hash); err != nil {
				s.logger.WarnContext(ctx, "failed to record utility applied", "node_id", cmd.NodeID, "utility", id, "error", err)
			}
		case marker != "":
			if err := s.states.RecordFailed(ctx, cmd.AccountID, cmd.NodeID, id, truncateReason(marker)); err != nil {
				s.logger.WarnContext(ctx, "failed to record utility failure", "node_id", cmd.NodeID, "utility", id, "error", err)
			}
		case status == string(node_command.StatusFailed):
			if err := s.states.RecordFailed(ctx, cmd.AccountID, cmd.NodeID, id, "apply failed before this utility reported"); err != nil {
				s.logger.WarnContext(ctx, "failed to record utility failure", "node_id", cmd.NodeID, "utility", id, "error", err)
			}
		}
		// StatusSuccess without a marker records nothing: an agent that
		// succeeded but did not claim this utility is a contradiction the
		// ladder must keep visible as declared/update_pending, not paper over.
	}

	if status == string(node_command.StatusSuccess) {
		s.maybeContinueCanaryFanOut(ctx, cmd)
	}
}

// maybeContinueCanaryFanOut is the second half of canary-first ordering
// (docs/UTILITIES-PLAN.md §3.7): a successfully settled apply whose payload
// carries the canary marker dispatches the assignment's REMAINING nodes. The
// targets are recomputed from the assignment as it is NOW — the scope may
// have changed while the canary ran, and yesterday's target list would
// dispatch to nodes the assignment no longer reaches. Runs detached
// (context.WithoutCancel, 10-min bound — the OU access fan-out precedent):
// the settle fires on the gRPC status path, whose context dies with the
// stream frame, and the fan-out is minutes of dispatch work.
func (s *Service) maybeContinueCanaryFanOut(ctx context.Context, cmd *node_command.NodeCommand) {
	raw, _ := cmd.Payload["canary"].(map[string]interface{})
	if raw == nil {
		return
	}
	assignmentText, _ := raw["assignment_id"].(string)
	userText, _ := raw["user_id"].(string)
	assignmentID, errA := uuid.Parse(assignmentText)
	userID, errU := uuid.Parse(userText)
	if errA != nil || errU != nil {
		s.logger.WarnContext(ctx, "canary marker unreadable; fan-out not continued",
			"node_id", cmd.NodeID, "assignment_id", assignmentText)
		return
	}
	accountID, canaryNodeID := cmd.AccountID, cmd.NodeID
	go func() {
		detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		a, err := s.assignments.GetByID(detached, accountID, assignmentID)
		if err != nil {
			// Deleted while the canary ran: nothing to continue, and the
			// parked nodes' desired state is whatever the next resolve says.
			s.logger.InfoContext(detached, "canary settled but its assignment is gone; fan-out not continued",
				"assignment_id", assignmentID, "error", err)
			return
		}
		targets, err := s.targetNodes(detached, accountID, a)
		if err != nil {
			s.logger.WarnContext(detached, "canary continuation could not enumerate targets",
				"assignment_id", assignmentID, "error", err)
			return
		}
		rest := make([]node.Node, 0, len(targets))
		for _, n := range targets {
			if n.ID != canaryNodeID {
				rest = append(rest, n)
			}
		}
		if len(rest) == 0 {
			return
		}
		allowed := s.allowedUnderPacing(detached, accountID, len(rest))
		results, dispatched := s.fanOutToNodes(detached, accountID, userID, rest, allowed, nil)
		deferred := 0
		for _, r := range results {
			if r.Skipped {
				deferred++
			}
		}
		s.logger.InfoContext(detached, "canary settled; fan-out continued",
			"assignment_id", assignmentID, "canary_node_id", canaryNodeID,
			"dispatched", dispatched, "skipped_or_deferred", deferred)
	}()
}

// CleanupNode drops a deleted node's ladder (wired into node.Service.Delete).
func (s *Service) CleanupNode(ctx context.Context, accountID, nodeID uuid.UUID) error {
	return s.states.DeleteByNode(ctx, accountID, nodeID)
}

// truncateReason bounds a device-reported failure string before it becomes a
// column value.
func truncateReason(v string) string {
	const max = 512
	if len(v) > max {
		return v[:max]
	}
	return v
}
