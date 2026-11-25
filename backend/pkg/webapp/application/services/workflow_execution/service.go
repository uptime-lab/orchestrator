package workflow_execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_execution_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/workflow_execution_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

const (
	workerCount = 500
	queueSize   = 1000
)

// secretsResolver resolves a node's effective secrets for the {{secrets.*}}
// context (satisfied by the organizational unit service).
type secretsResolver interface {
	ResolveEffectiveProvisioning(ctx context.Context, accountID, nodeID uuid.UUID) (services.EffectiveProvisioning, error)
}

// secretWriter upserts a node-attached secret (satisfied by the secret
// service) for the secret_upsert and attribute_upsert blocks.
type secretWriter interface {
	UpsertForNode(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, name string, kind secret_domain.SecretKind, key, value string, opts secret_domain.UpsertOptions) error
	// UpsertNodeAttribute is the same method the attribute HTTP endpoints call,
	// so the block inherits key validation, the secret quota and the audit
	// record instead of re-implementing them.
	UpsertNodeAttribute(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, key, value string, sensitive bool) (*secret_domain.NodeAttribute, error)
}

// nodePolicyWriter overrides a node's boot policy flags (satisfied by the
// organizational unit service) for the set_boot_policy block.
type nodePolicyWriter interface {
	SetNodePolicy(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, networkFirst, bootToOS, sshPasswordAuth *organizational_unit.PolicyFlag) error
}

// Service handles workflow execution-related business logic
type Service struct {
	executionRepo      repository.WorkflowExecutionRepository
	executionNodeRepo  repository.WorkflowExecutionNodeRepository
	templateRepo       repository.WorkflowTemplateRepository
	nodeRepo           repository.NodeRepository
	nodeCommandRepo    repository.NodeCommandRepository
	ouRepo             repository.OrganizationalUnitRepository
	nodeService        services.NodeService
	nodeCommandService services.NodeCommandService
	websocketClient    websockets.WebsocketClient
	cfg                config.WorkflowConfig
	// Control-flow dependencies (Phase 3+), wired via SetControlDeps.
	secretsResolver  secretsResolver
	secretWriter     secretWriter
	nodePolicyWriter nodePolicyWriter
	// authzChecker re-checks a scheduled playbook's author at fire time, wired
	// via SetAuthzChecker (a narrow local interface to avoid an import cycle).
	authzChecker authzChecker
	// heartbeat lets the scheduler report that it is still ticking. Optional:
	// unset means the console cannot tell a wedged scheduler from a quiet week,
	// which is how it was before this existed, not a failure to run.
	heartbeat repository.InstanceHeartbeatRepository
	// anomalyRepo reports playbooks that were due and deliberately not fired
	// (SetAnomalyRepo). Optional: unset means those skips stay invisible, which
	// is how it was before skip reporting existed, not a failure to run.
	anomalyRepo    repository.InstanceAnomalyRepository
	schedulerStart time.Time
	// reportedOverCap remembers which templates the last tick reported as past
	// the per-account cron cap, so the NEXT tick can resolve the ones that came
	// back under it.
	//
	// The condition cannot be resolved the way every other skip reason is —
	// resolveScheduleSkips runs when a playbook FIRES, and a playbook past the
	// cap is precisely one that never fires, so recording it without this would
	// have created a row that can only ever say "firing". That is worse than not
	// reporting at all: an operator who fixes the cap and still sees the alert
	// learns to ignore the kind.
	//
	// In memory, and owned by the single scheduler goroutine — no lock. Losing it
	// on restart costs nothing: the playbook that came back under the cap fires at
	// its next scheduled minute, and resolveScheduleSkips clears it there.
	reportedOverCap map[uuid.UUID]bool
	nodeQueue       chan uuid.UUID
	// droppedEnqueue is raised when a send to nodeQueue found it full. The
	// sweeper clears it and re-enqueues from the database — see enqueue.go for
	// why the send cannot simply block.
	droppedEnqueue atomic.Bool

	// inFlight claims an execution node for exactly one worker, keyed by
	// execution-node id and holding a *nodeClaim.
	//
	// It replaces a declared-and-never-used `mu sync.RWMutex` on this struct,
	// which could not have been the right shape anyway: a service-wide lock would
	// serialise all 500 workers. What actually needs to be exclusive is one
	// execution node, and nothing was.
	//
	// Without it, two workers can load independent copies of the same row, both
	// decide the same block is ready, and dispatchAgent registers a command EACH
	// — while last-write-wins finalize records only one. The registry contains
	// erase_drive, install_os, capture_image and reboot, so a duplicate is not a
	// cosmetic problem. Exposure was limited only because the editor leaves
	// timeout_seconds blank by default; it grows the moment anyone sets timeouts,
	// because that is what arms the re-enqueue timers.
	inFlight sync.Map
	// armedDeadlines records the absolute deadline a re-enqueue timer is already
	// pending for, per execution node, so the same deadline is not armed twice.
	armedDeadlines sync.Map
	// auditRecorder writes the run-start row for every started run; see
	// SetAuditRecorder.
	auditRecorder *audit.Recorder
	logger        *slog.Logger
}

// SetAuditRecorder wires the run-start audit row. StartExecution is the one
// choke point every start passes (the runs route, node replace, the
// scheduler), so the row is written there and no caller can forget it.
func (s *Service) SetAuditRecorder(r *audit.Recorder) {
	s.auditRecorder = r
}

// nodeClaim is one execution node's exclusive ownership token.
type nodeClaim struct {
	mu sync.Mutex
	// dirty means another worker arrived while this one held the claim, so the
	// owner must walk the graph again rather than let that wake-up be lost. A
	// dropped wake-up would stall the workflow, which is worse than the duplicate
	// dispatch this claim exists to prevent.
	dirty bool
	// retired means the owner has finished and removed the claim. A late setter
	// holding a retired claim must re-queue instead of marking a token nobody
	// will read.
	retired bool
}

// SetAnomalyRepo wires operator-facing reporting of scheduler skips.
func (s *Service) SetAnomalyRepo(repo repository.InstanceAnomalyRepository) {
	s.anomalyRepo = repo
}

// SetHeartbeatRepo wires scheduler liveness reporting.
func (s *Service) SetHeartbeatRepo(repo repository.InstanceHeartbeatRepository) {
	s.heartbeat = repo
}

// SetControlDeps wires the dependencies the graph engine's server-executed
// blocks need (secrets context + secret upsert + boot policy). Optional:
// blocks that use them fail gracefully if unset. resolver and policyWriter are
// both satisfied by the organizational unit service.
func (s *Service) SetControlDeps(resolver secretsResolver, writer secretWriter, policyWriter nodePolicyWriter) {
	s.secretsResolver = resolver
	s.secretWriter = writer
	s.nodePolicyWriter = policyWriter
}

// authzChecker answers whether a user still holds an action, used to re-check a
// scheduled playbook's author before an unattended fire.
type authzChecker interface {
	Can(ctx context.Context, accountID, userID uuid.UUID, action authz.Action, targetOU *uuid.UUID) bool
}

// SetAuthzChecker wires the fire-time authorization re-check. Required for the
// cron scheduler to run privileged playbooks: with no checker wired the
// scheduler refuses them (fail-closed).
func (s *Service) SetAuthzChecker(checker authzChecker) {
	s.authzChecker = checker
}

// NewService creates a new workflow execution service
func NewService(
	executionRepo repository.WorkflowExecutionRepository,
	executionNodeRepo repository.WorkflowExecutionNodeRepository,
	templateRepo repository.WorkflowTemplateRepository,
	nodeRepo repository.NodeRepository,
	nodeCommandRepo repository.NodeCommandRepository,
	ouRepo repository.OrganizationalUnitRepository,
	nodeService services.NodeService,
	nodeCommandService services.NodeCommandService,
	websocketClient websockets.WebsocketClient,
	cfg config.WorkflowConfig,
	logger *slog.Logger,
) *Service {
	return &Service{
		executionRepo:      executionRepo,
		executionNodeRepo:  executionNodeRepo,
		templateRepo:       templateRepo,
		nodeRepo:           nodeRepo,
		nodeCommandRepo:    nodeCommandRepo,
		ouRepo:             ouRepo,
		nodeService:        nodeService,
		nodeCommandService: nodeCommandService,
		websocketClient:    websocketClient,
		cfg:                cfg,
		nodeQueue:          make(chan uuid.UUID, queueSize),
		logger:             logger.With("service", "workflow_execution"),
	}
}

// Start initializes and starts the worker pool
func (s *Service) Start(ctx context.Context) {
	// Start 500 workers
	for i := 0; i < workerCount; i++ {
		go s.worker(ctx, i)
	}
	s.logger.Info("workflow execution service started", "workers", workerCount)

	// Recovers ids dropped when the queue was full (see enqueue.go). Idle unless a
	// drop actually happened.
	s.startDropSweeper(ctx)

	// The work queue is in-memory: without a re-enqueue, every restart (i.e.
	// every deploy) strands in-flight execution nodes forever — and stranded
	// pending/running executions hold active-cap slots until the account is
	// locked out. The engine re-entry is idempotent (same path as
	// ProcessNodeCommandCompletion), so re-enqueueing a node that was merely
	// waiting is safe. Blocking send in a goroutine: the backlog may exceed
	// the queue size and the workers are already draining it.
	go func() {
		ids, err := s.executionNodeRepo.GetActiveIDs(ctx)
		if err != nil {
			s.logger.Error("failed to list in-flight execution nodes for re-enqueue", "error", err)
			return
		}
		for _, id := range ids {
			select {
			case s.nodeQueue <- id:
			case <-ctx.Done():
				return
			}
		}
		if len(ids) > 0 {
			s.logger.Info("re-enqueued in-flight execution nodes after restart", "count", len(ids))
		}
	}()
}

// worker processes one node at a time from the queue
func (s *Service) worker(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case executionNodeID, ok := <-s.nodeQueue:
			if !ok {
				return
			}
			s.processNodeGuarded(ctx, executionNodeID, workerID)
		}
	}
}

// processNodeGuarded contains a panic to the one execution node that caused it.
//
// The gRPC and HTTP surfaces have had recovery from the start; these 500
// workers did not, and here the consequence is worse than one dead request.
// Start() re-enqueues every in-flight execution node on boot, so a panic in a
// graph step would kill the process, the supervisor would restart it, the same
// node would be re-enqueued, and it would panic again — an unbounded restart
// loop that takes the API and every agent stream with it and is breakable only
// by an UPDATE against the database.
//
// The recovered node is FAILED rather than retried, on purpose: a panic is
// almost always deterministic, so retrying reproduces it, and leaving the node
// pending would hold one of the per-account active-execution slots forever
// (completed_at is what both the cap and the retention sweep key off).
func (s *Service) processNodeGuarded(ctx context.Context, executionNodeID uuid.UUID, workerID int) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		s.logger.Error("panic while advancing an execution node; failing it",
			"worker_id", workerID,
			"execution_node_id", executionNodeID,
			"panic", r,
			"stack", string(debug.Stack()))
		s.settlePanickedNode(ctx, executionNodeID, r)
	}()
	s.processNode(ctx, executionNodeID, workerID)
}

// settlePanickedNode fails the execution node a panic left mid-walk, reusing
// the same settler the node-delete and cancel paths use so the in-flight device
// command is retired too.
//
// It recovers again around itself: this runs from a deferred function, so a
// second panic here (a nil repo, a broken row) would escape the first recover
// and kill the process anyway — reintroducing exactly the crash loop the guard
// exists to prevent.
func (s *Service) settlePanickedNode(ctx context.Context, executionNodeID uuid.UUID, cause any) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic while settling a panicked execution node",
				"execution_node_id", executionNodeID, "panic", r)
		}
	}()

	execNode, err := s.executionNodeRepo.GetByID(ctx, executionNodeID)
	if err != nil {
		s.logger.Error("failed to load the panicked execution node to fail it",
			"execution_node_id", executionNodeID, "error", err)
		return
	}
	// The reason reaches the operator in the run view, so it says what happened
	// without pasting a Go panic string into the UI.
	s.settleExecutionNodes(ctx,
		[]workflow_execution_node.WorkflowExecutionNode{*execNode},
		fmt.Sprintf("the platform hit an internal error while advancing this step (%v)", cause))
}

// processNode drives one execution node one graph step forward (see engine.go),
// under an exclusive claim so no two workers walk the same graph at once.
//
// A worker that finds the node claimed does NOT re-queue immediately — that
// would spin at the rate the queue drains. It marks the claim dirty, and the
// owner loops. The one case that cannot be handled that way is a claim retired
// between the map lookup and the flag being set, and there the arriving worker
// re-queues so the wake-up is not lost.
func (s *Service) processNode(ctx context.Context, executionNodeID uuid.UUID, workerID int) {
	s.withNodeClaim(executionNodeID, workerID, func() {
		s.processNodeGraph(ctx, executionNodeID, workerID)
	})
}

// withNodeClaim runs walk under an exclusive claim on one execution node,
// re-running it if another worker arrived while it was running.
//
// Separate from processNode so the claim can be exercised without a database —
// the graph walk needs one, the claim does not.
func (s *Service) withNodeClaim(executionNodeID uuid.UUID, workerID int, walk func()) {
	claim := &nodeClaim{}
	if existing, loaded := s.inFlight.LoadOrStore(executionNodeID, claim); loaded {
		owner := existing.(*nodeClaim)
		owner.mu.Lock()
		if owner.retired {
			owner.mu.Unlock()
			// The owner finished in the gap. Nobody will read a flag on this
			// claim, so hand the work back to the queue.
			s.enqueueNode(executionNodeID, "raced claim hand-back")
			return
		}
		owner.dirty = true
		owner.mu.Unlock()
		s.logger.Debug("execution node already in flight; owner will re-run",
			"execution_node_id", executionNodeID, "worker_id", workerID)
		return
	}

	// Release the claim even if walk() panics. Without this, a panicking step
	// would leave the node claimed forever: every later enqueue would find a
	// live claim, mark it dirty and return, so the execution node would never
	// advance again and its run would never reach completed_at — permanently
	// consuming one of the per-account active-execution slots, invisibly.
	//
	// This was harmless while a panic took the whole process down, because the
	// map is in-memory and died with it. It stops being harmless the moment the
	// worker recovers, which is why the two changes belong together.
	defer func() {
		claim.mu.Lock()
		if !claim.retired {
			claim.retired = true
			s.inFlight.Delete(executionNodeID)
		}
		claim.mu.Unlock()
	}()

	for {
		walk()

		claim.mu.Lock()
		if claim.dirty {
			claim.dirty = false
			claim.mu.Unlock()
			continue
		}
		// Retire and remove inside the same critical section, so a worker that
		// already holds this claim can tell it is too late to mark it.
		claim.retired = true
		s.inFlight.Delete(executionNodeID)
		claim.mu.Unlock()
		return
	}
}

// updateExecutionStatus updates the overall execution status based on all nodes
func (s *Service) updateExecutionStatus(ctx context.Context, execution *workflow_execution.WorkflowExecution) {
	nodes, err := s.executionNodeRepo.GetByExecutionID(ctx, execution.ID)
	if err != nil {
		s.logger.Error("failed to get execution nodes", "execution_id", execution.ID, "error", err)
		return
	}

	allCompleted := true
	allFailed := true
	anyFailed := false
	allTerminal := true

	for _, node := range nodes {
		if node.Status != workflow_execution_node.StatusCompleted {
			allCompleted = false
		}
		if node.Status != workflow_execution_node.StatusFailed {
			allFailed = false
		}
		if node.Status == workflow_execution_node.StatusFailed {
			anyFailed = true
		}
		if node.Status == workflow_execution_node.StatusPending ||
			node.Status == workflow_execution_node.StatusRunning {
			allTerminal = false
		}
	}

	if allCompleted {
		execution.Complete(workflow_execution.StatusCompleted)
	} else if allFailed {
		execution.Complete(workflow_execution.StatusFailed)
	} else if allTerminal && anyFailed {
		// Mixed outcome with every node finished: this run is DONE. Completing
		// (not just setting the status) matters — completed_at is what the
		// active-executions cap and the retention sweep key off.
		execution.Complete(workflow_execution.StatusPartiallyFailed)
	} else if anyFailed {
		// Some nodes failed but others are still running: transient
		// partially_failed, deliberately without completed_at.
		execution.Status = workflow_execution.StatusPartiallyFailed
		execution.UpdatedAt = time.Now()
	} else {
		// Still running
		return
	}

	if err := s.executionRepo.Update(ctx, execution); err != nil {
		// The run settled underneath this evaluation (a cancel, a node delete,
		// another worker finishing last). The recorded outcome stands and this
		// copy is stale — nothing to report and nothing to push to the clients.
		if errors.Is(err, app_errors.ErrExecutionAlreadyCompleted) {
			return
		}
		s.logger.Error("failed to update execution status", "execution_id", execution.ID, "error", err)
		return
	}

	s.notifyExecutionUpdated(execution)
}

// CheckStart asks every question StartExecution refuses a start on — the
// caps, the template, the targets, the playbook's unit scope — without
// writing anything. ouOverride gives a target node the unit it is about to be
// moved into, for a caller that moves it before starting the run.
func (s *Service) CheckStart(ctx context.Context, cmd workflow_execution_start.WorkflowExecutionStartCommand, ouOverride map[uuid.UUID]*uuid.UUID) error {
	_, err := s.checkStart(ctx, cmd, ouOverride)
	return err
}

// checkStart is CheckStart returning the template it resolved.
func (s *Service) checkStart(ctx context.Context, cmd workflow_execution_start.WorkflowExecutionStartCommand, ouOverride map[uuid.UUID]*uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	if len(cmd.NodeIDs) == 0 {
		return nil, app_errors.InvalidRequest("no_target_nodes", errors.New("at least one node ID is required"))
	}

	// Per-account in-flight cap: bounds runaway cron schedules and manual
	// spam alike (manual and scheduled starts share this choke point). A
	// failed count is logged and lets the start through — the cap protects
	// scale, availability of legitimate runs wins on a transient DB error.
	if s.cfg.MaxActiveExecutionsPerAccount > 0 {
		active, err := s.executionRepo.CountActiveByAccountID(ctx, cmd.AuthContext.AccountID)
		if err != nil {
			s.logger.Warn("failed to count active executions, skipping cap check", "account_id", cmd.AuthContext.AccountID, "error", err)
		} else if active >= int64(s.cfg.MaxActiveExecutionsPerAccount) {
			return nil, fmt.Errorf("%d of %d in flight: %w", active, s.cfg.MaxActiveExecutionsPerAccount, app_errors.ErrTooManyActiveExecutions)
		}
	}

	// Per-account RUNNING-NODE cap. The execution cap above does not imply this
	// one: 20 executions times an unbounded node count is
	// unbounded, node rows are free, and execution nodes are what actually
	// occupy the 500 process-global workers. A single run over a large fleet can
	// hit this with the execution count at one.
	//
	// Checked against the requested node count, not just the current total, so
	// the refusal happens BEFORE an execution row exists — a run created and
	// then truncated would leave a partial execution nobody asked for. Same
	// fail-open-on-error posture as the cap above: a transient count failure
	// must not stop legitimate work.
	if s.cfg.MaxRunningNodesPerAccount > 0 {
		cap := int64(s.cfg.MaxRunningNodesPerAccount)
		running, err := s.executionNodeRepo.CountActiveByAccountID(ctx, cmd.AuthContext.AccountID)
		if err != nil {
			s.logger.Warn("failed to count running execution nodes, skipping cap check",
				"account_id", cmd.AuthContext.AccountID, "error", err)
		} else if running+int64(len(cmd.NodeIDs)) > cap {
			// Logged, because a cap nobody can see binding is indistinguishable
			// from a broken run picker.
			s.logger.WarnContext(ctx, "refused a workflow start at the per-account running-node cap",
				"account_id", cmd.AuthContext.AccountID, "running", running,
				"requested", len(cmd.NodeIDs), "cap", cap)
			return nil, fmt.Errorf("%d running + %d requested exceeds %d: %w",
				running, len(cmd.NodeIDs), cap, app_errors.ErrTooManyRunningNodes)
		}
	}

	// Verify template exists
	template, err := s.templateRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.TemplateID)
	if err != nil {
		return nil, fmt.Errorf("template not found: %w", err)
	}

	// Drafts are hidden from run pickers, but that is UI-only; enforce here so
	// they cannot be started through the API either.
	if template.Status != workflow_template.StatusActive {
		return nil, fmt.Errorf("template %s: %w", cmd.TemplateID, app_errors.ErrWorkflowTemplateNotActive)
	}

	// A member's run names its targets explicitly, so one target that is not
	// in this account refuses the whole run — the answer the batch command
	// route gives — rather than starting on the rest and dropping it without
	// a word. A node of another account and a node that does not exist get
	// the same error. A scheduled fire resolved its targets a moment ago from
	// the scope, and a node deleted since is skipped below as before.
	if !cmd.Scheduled {
		for _, nodeID := range cmd.NodeIDs {
			if _, err := s.nodeRepo.GetByID(ctx, cmd.AuthContext.AccountID, nodeID); err != nil {
				if errors.Is(err, app_errors.ErrNodeNotFound) {
					return nil, fmt.Errorf("target node %s: %w", nodeID, app_errors.ErrNodeNotFound)
				}
				return nil, fmt.Errorf("failed to resolve target node %s: %w", nodeID, err)
			}
		}
	}

	// A scoped playbook may only target nodes inside its OU scope — the OU
	// itself or one of its children, mirroring the run picker's availability
	// rule (a node sees playbooks of its own OU and its parent). The run grant
	// was authorized against the template's OU, so without this a caller
	// scoped to OU A could run an OU-A playbook against unrelated nodes.
	// Validate before creating the execution to avoid a dangling row on
	// rejection.
	if template.IsScoped() {
		allowed, err := s.scopeTargetOUs(ctx, cmd.AuthContext.AccountID, template.OUIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve playbook OU scope: %w", err)
		}
		for _, nodeID := range cmd.NodeIDs {
			n, err := s.nodeRepo.GetByID(ctx, cmd.AuthContext.AccountID, nodeID)
			if err != nil {
				continue // unknown ids are skipped below, as before
			}
			ouID := n.OUID
			if moved, ok := ouOverride[nodeID]; ok {
				ouID = moved
			}
			if ouID == nil || !allowed[*ouID] {
				return nil, fmt.Errorf("%w: node %s is outside the playbook's organizational unit", app_errors.ErrForbidden, nodeID)
			}
		}
	}
	return template, nil
}

// StartExecution creates a workflow execution and queues nodes for processing
func (s *Service) StartExecution(ctx context.Context, cmd workflow_execution_start.WorkflowExecutionStartCommand) (*workflow_execution.WorkflowExecution, error) {
	template, err := s.checkStart(ctx, cmd, nil)
	if err != nil {
		return nil, err
	}

	// Create execution with the template pinned: the engine runs the snapshot,
	// so editing the template cannot change this execution mid-flight. The OU
	// scope is frozen the same way — a run's visibility cannot change after
	// launch.
	execution := workflow_execution.NewWorkflowExecution(cmd.TemplateID, cmd.AuthContext.AccountID)
	execution.TemplateSnapshot = template.Snapshot()
	// A run is pinned to ONE unit for live-update visibility, so a playbook
	// spanning several pins none: nil reaches account-wide holders only
	// (mayReceive treats a nil scope as "unassigned"), which is narrower than
	// picking one member and wrong in the safe direction. Per-node frames are
	// scoped by the NODE's unit either way, which is the visibility that
	// matters while a run is in flight.
	execution.OUID = template.PrimaryOUID()
	if cmd.Scheduled {
		execution.StartedKind = workflow_execution.StartedByScheduler
	} else {
		startedBy := cmd.AuthContext.UserID
		execution.StartedBy = &startedBy
		execution.StartedKind = workflow_execution.StartedByUser
	}
	if err := s.executionRepo.Create(ctx, execution); err != nil {
		return nil, fmt.Errorf("failed to create execution: %w", err)
	}

	// Create execution nodes and enqueue them
	nodeCount := 0
	for _, nodeID := range cmd.NodeIDs {
		// Verify node exists and belongs to account
		_, err := s.nodeRepo.GetByID(ctx, cmd.AuthContext.AccountID, nodeID)
		if err != nil {
			s.logger.Warn("node not found, skipping", "node_id", nodeID, "execution_id", execution.ID)
			continue
		}

		executionNode := workflow_execution_node.NewWorkflowExecutionNode(execution.ID, nodeID, cmd.AuthContext.AccountID)
		if err := s.executionNodeRepo.Create(ctx, executionNode); err != nil {
			s.logger.Error("failed to create execution node", "node_id", nodeID, "execution_id", execution.ID, "error", err)
			continue
		}

		nodeCount++

		// Enqueue for processing
		s.enqueueNode(executionNode.ID, "execution start")
	}

	// Every target id was unknown: fail the run instead of leaving a pending
	// row no engine worker will ever touch — a permanently-pending execution
	// would hold an active-cap slot forever (deliberate garbage ids could
	// lock the whole account out of workflows).
	if nodeCount == 0 {
		execution.Complete(workflow_execution.StatusFailed)
		if err := s.executionRepo.Update(ctx, execution); err != nil {
			s.logger.Error("failed to fail zero-node execution", "execution_id", execution.ID, "error", err)
		}
		s.notifyExecutionUpdated(execution)
		return nil, fmt.Errorf("execution %s: %w", execution.ID, app_errors.ErrNoValidTargetNodes)
	}

	// Update execution with node count. Only that column: the workers were
	// started inside the loop above, so this copy of the execution may already
	// describe a run that has moved on — writing it whole would put `pending`
	// and a null completed_at back over a run that had finished.
	execution.NodeCount = nodeCount
	if err := s.executionRepo.UpdateNodeCount(ctx, execution.AccountID, execution.ID, nodeCount); err != nil {
		s.logger.Warn("failed to update execution node count", "execution_id", execution.ID, "error", err)
		// Continue anyway, node count is not critical
	}

	// The engine dispatches the run's commands as the platform, so this row
	// is what names the member who started it, and it outlives the execution
	// rows the run-history retention deletes. A scheduled fire is the
	// platform's own.
	if s.auditRecorder != nil {
		actor := audit.SystemActor(cmd.AuthContext.AccountID)
		if !cmd.Scheduled {
			actor = audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID)
		}
		s.auditRecorder.RecordDetail(ctx, actor,
			audit_event.ActionPlaybookRunStart, audit_event.TargetPlaybookExecution, execution.ID.String(),
			fmt.Sprintf("playbook=%s nodes=%d", execution.TemplateID, execution.NodeCount))
	}

	s.notifyExecutionUpdated(execution)
	return execution, nil
}

// scopeTargetOUs returns the set of OU ids a playbook scoped to ouIDs may
// target: each unit plus its direct children. This mirrors the run picker's
// availability rule (a node sees playbooks of its own OU and its parent) — the
// OU tree is two levels deep, so direct children cover the whole subtree.
//
// The UNION is the whole point of a scope set: a playbook attached to two units
// may target the nodes of both. It is also why an empty set must never reach
// here — an empty allow-map would refuse every node, and "unscoped" means the
// opposite, so the callers test IsScoped first.
func (s *Service) scopeTargetOUs(ctx context.Context, accountID uuid.UUID, ouIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	ous, err := s.ouRepo.GetAllByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	scoped := make(map[uuid.UUID]bool, len(ouIDs))
	allowed := make(map[uuid.UUID]bool, len(ouIDs)*2)
	for _, ouID := range ouIDs {
		scoped[ouID] = true
		allowed[ouID] = true
	}
	for i := range ous {
		if ous[i].ParentID != nil && scoped[*ous[i].ParentID] {
			allowed[ous[i].ID] = true
		}
	}
	return allowed, nil
}

// ProcessNodeCommandCompletion is called when a node command completes
// This re-enqueues the execution node for processing
func (s *Service) ProcessNodeCommandCompletion(ctx context.Context, nodeCommandID uuid.UUID) {
	// Find execution node by command ID
	executionNodeID := s.findExecutionNodeByCommandID(ctx, nodeCommandID)
	if executionNodeID == uuid.Nil {
		return
	}

	// Re-enqueue the execution node
	if s.enqueueNode(executionNodeID, "device command completed") {
		s.logger.Debug("re-enqueued execution node", "execution_node_id", executionNodeID, "command_id", nodeCommandID)
	}
}

// findExecutionNodeByCommandID finds the execution node waiting on a device
// command: via the command's workflow_execution_node_id link, falling back to
// the execution node that records the command as its current one (for commands
// whose link was never written).
//
// This runs on EVERY device command completion — a manual reboot, a status
// scan, an EEPROM flash — and the overwhelming majority belong to no run at
// all, i.e. the fallback is the COMMON path, not the exceptional one. So both
// steps are single indexed reads. They used to be a row-locking read of the
// whole command followed by loading the node's entire execution history and
// scanning it in Go, which grew without bound for any node running a frequent
// playbook.
func (s *Service) findExecutionNodeByCommandID(ctx context.Context, commandID uuid.UUID) uuid.UUID {
	link, err := s.nodeCommandRepo.GetWorkflowLink(ctx, commandID)
	if err != nil {
		s.logger.Warn("failed to get command for execution node lookup", "command_id", commandID, "error", err)
		return uuid.Nil
	}
	if link.ExecutionNodeID != nil {
		return *link.ExecutionNodeID
	}

	executionNodeID, err := s.executionNodeRepo.FindIDByCurrentCommandID(ctx, link.AccountID, link.NodeID, commandID)
	if err != nil {
		s.logger.Warn("failed to search execution nodes by current command",
			"command_id", commandID, "node_id", link.NodeID, "error", err)
		return uuid.Nil
	}
	if executionNodeID != uuid.Nil {
		s.logger.Debug("found execution node by CurrentCommandID fallback",
			"execution_node_id", executionNodeID, "command_id", commandID)
	}
	return executionNodeID
}

// GetByID retrieves a workflow execution by ID if it belongs to the account
func (s *Service) GetByID(ctx context.Context, query workflow_execution_get.WorkflowExecutionGetQuery) (*workflow_execution.WorkflowExecution, error) {
	execution, err := s.executionRepo.GetByID(ctx, query.AuthContext.AccountID, query.ID)
	if err != nil {
		return nil, err
	}

	if nodeIDs, err := s.executionNodeRepo.GetNodeIDsByExecutionIDs(ctx, []uuid.UUID{execution.ID}); err == nil {
		execution.NodeIDs = nodeIDs[execution.ID]
	}

	return execution, nil
}

// attachNodeIDs enriches executions with their target node ids (one batch
// query); list views use them for OU-based filtering. Failures are logged
// and non-fatal.
func (s *Service) attachNodeIDs(ctx context.Context, executions []workflow_execution.WorkflowExecution) {
	if len(executions) == 0 {
		return
	}
	ids := make([]uuid.UUID, len(executions))
	for i := range executions {
		ids[i] = executions[i].ID
	}
	nodeIDs, err := s.executionNodeRepo.GetNodeIDsByExecutionIDs(ctx, ids)
	if err != nil {
		s.logger.Warn("failed to load execution node ids", "error", err)
		return
	}
	for i := range executions {
		executions[i].NodeIDs = nodeIDs[executions[i].ID]
	}
}

// List returns one page of the account's workflow executions (newest first)
// plus the total count for the filter.
func (s *Service) List(ctx context.Context, query workflow_execution_list.WorkflowExecutionListQuery) ([]workflow_execution.WorkflowExecution, int64, error) {
	if err := query.Validate(); err != nil {
		return nil, 0, err
	}

	filter := repository.WorkflowExecutionListFilter{
		TemplateID: query.TemplateID,
		Offset:     (query.Page - 1) * query.PerPage,
		Limit:      query.PerPage,
	}

	// Expand the OU filter to its subtree (the OU + its direct children — the
	// tree is 2 levels) so the repository matches runs by target-node OU the
	// same way the Runs page rail does.
	if query.OUID != nil {
		filter.NodeOUIDs = []uuid.UUID{*query.OUID}
		ous, err := s.ouRepo.GetAllByAccountID(ctx, query.AuthContext.AccountID)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to expand OU filter: %w", err)
		}
		for i := range ous {
			if ous[i].ParentID != nil && *ous[i].ParentID == *query.OUID {
				filter.NodeOUIDs = append(filter.NodeOUIDs, ous[i].ID)
			}
		}
	}

	executions, total, err := s.executionRepo.List(ctx, query.AuthContext.AccountID, filter)
	if err != nil {
		return nil, 0, err
	}

	s.attachNodeIDs(ctx, executions)
	return executions, total, nil
}

// GetExecutionNodes retrieves all execution nodes for an execution
func (s *Service) GetExecutionNodes(ctx context.Context, executionID uuid.UUID) ([]workflow_execution_node.WorkflowExecutionNode, error) {
	nodes, err := s.executionNodeRepo.GetByExecutionID(ctx, executionID)
	if err != nil {
		return nil, err
	}

	return nodes, nil
}

// GetExecutionNodeCommands retrieves the commands of one execution node.
//
// allowSecretValues decides whether the commands of blocks hidden from the run's
// graph_state are listed at all. They must not be: RedactGraphStateForGraph
// OMITS the state of a secret-fed unprivileged block and everything downstream
// of it, because `completed` on one branch and `skipped` on the other is the
// same fact as the condition's redacted output — and the mere PRESENCE of a
// downstream block's command row answers exactly that question, one comparison
// at a time. Redacting the payload of those rows was never the issue; listing
// them at all is.
func (s *Service) GetExecutionNodeCommands(ctx context.Context, executionID, nodeID uuid.UUID, accountID uuid.UUID, allowSecretValues bool) ([]node_command_domain.NodeCommand, error) {
	// First verify the execution node exists and belongs to the account
	executionNodes, err := s.executionNodeRepo.GetByExecutionID(ctx, executionID)
	if err != nil {
		return nil, err
	}

	// Find the execution node for this node ID
	var executionNode *workflow_execution_node.WorkflowExecutionNode
	for i := range executionNodes {
		if executionNodes[i].NodeID == nodeID && executionNodes[i].AccountID == accountID {
			executionNode = &executionNodes[i]
			break
		}
	}

	if executionNode == nil {
		return nil, fmt.Errorf("execution node not found for node_id %s in execution %s", nodeID, executionID)
	}

	// Get commands for this execution node
	commands, err := s.nodeCommandRepo.GetByWorkflowExecutionNodeID(ctx, executionNode.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get commands for execution node: %w", err)
	}
	if allowSecretValues {
		return commands, nil
	}
	return s.withoutHiddenBlockCommands(ctx, executionID, accountID, executionNode, commands)
}

// withoutHiddenBlockCommands drops the commands belonging to blocks whose state
// the run view withholds.
//
// Two links tie a command to its block, and both are needed. The graph state's
// CommandID covers everything settled — including rows from before the block-id
// bookkeeping existed. The command payload's own block id
// (results.WorkflowBlockIDKey) covers the window GraphState structurally
// cannot: between dispatch and finalize the command row exists while no state
// records it, on EVERY pass — so a poller could catch a hidden command's
// presence during any ordinary run of a secret-conditioned playbook, not only
// after a crash.
//
// A failed execution read now FAILS the listing instead of returning it
// unfiltered: a transient error must not widen what a caller below
// secret.read_value can see, and a visible 5xx the caller retries is cheaper
// than a leak nobody notices. A run with no snapshot stays unfiltered
// deliberately — pre-snapshot runs predate graph playbooks entirely, so nothing
// in them can be secret-fed, and there is no hidden set to enforce.
func (s *Service) withoutHiddenBlockCommands(
	ctx context.Context,
	executionID, accountID uuid.UUID,
	executionNode *workflow_execution_node.WorkflowExecutionNode,
	commands []node_command_domain.NodeCommand,
) ([]node_command_domain.NodeCommand, error) {
	execution, err := s.executionRepo.GetByID(ctx, accountID, executionID)
	if err != nil {
		return nil, fmt.Errorf("cannot decide which commands this caller may see: %w", err)
	}
	if execution == nil || execution.TemplateSnapshot == nil || execution.TemplateSnapshot.Graph == nil {
		return commands, nil
	}
	hidden := execution.TemplateSnapshot.Graph.BlocksDownstreamOfSecretConditions()
	if len(hidden) == 0 {
		return commands, nil
	}
	hiddenCommands := make(map[uuid.UUID]bool, len(hidden))
	for blockID, state := range executionNode.GraphState {
		if hidden[blockID] && state.CommandID != nil {
			hiddenCommands[*state.CommandID] = true
		}
	}
	out := make([]node_command_domain.NodeCommand, 0, len(commands))
	for _, cmd := range commands {
		if hiddenCommands[cmd.ID] {
			continue
		}
		if blockID, _ := cmd.Payload[results.WorkflowBlockIDKey].(string); blockID != "" && hidden[blockID] {
			continue
		}
		out = append(out, cmd)
	}
	return out, nil
}

// WithoutHiddenRunCommands applies the run view's omission to commands read
// through another path (a node's command list, one command by id): for a
// caller without secret.read_value it drops every command of a block whose
// state the command's own run withholds — see GetExecutionNodeCommands for
// why presence alone answers the secret-fed condition. A command no run
// dispatched passes; one whose run is gone is dropped, since nothing is left
// to say which of its blocks were hidden. A lookup that fails refuses the
// whole read, as the run view does.
func (s *Service) WithoutHiddenRunCommands(ctx context.Context, accountID uuid.UUID, commands []node_command_domain.NodeCommand) ([]node_command_domain.NodeCommand, error) {
	byStep := map[uuid.UUID][]node_command_domain.NodeCommand{}
	for _, cmd := range commands {
		if cmd.WorkflowExecutionNodeID != nil {
			byStep[*cmd.WorkflowExecutionNodeID] = append(byStep[*cmd.WorkflowExecutionNodeID], cmd)
		}
	}
	if len(byStep) == 0 {
		return commands, nil
	}
	kept := map[uuid.UUID]bool{}
	for stepID, stepCommands := range byStep {
		step, err := s.executionNodeRepo.GetByID(ctx, stepID)
		if errors.Is(err, app_errors.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot decide which commands this caller may see: %w", err)
		}
		if step.AccountID != accountID {
			// Not this account's run: nothing of it is shown.
			continue
		}
		visible, err := s.withoutHiddenBlockCommands(ctx, step.ExecutionID, accountID, step, stepCommands)
		if errors.Is(err, app_errors.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, cmd := range visible {
			kept[cmd.ID] = true
		}
	}
	out := make([]node_command_domain.NodeCommand, 0, len(commands))
	for _, cmd := range commands {
		if cmd.WorkflowExecutionNodeID == nil || kept[cmd.ID] {
			out = append(out, cmd)
		}
	}
	return out, nil
}

// GetWorkflowExecutionNodeIDForCommand retrieves the workflow execution node ID for a command
func (s *Service) GetWorkflowExecutionNodeIDForCommand(ctx context.Context, commandID uuid.UUID) (*uuid.UUID, error) {
	return s.nodeCommandRepo.GetWorkflowExecutionNodeID(ctx, commandID)
}

// GetExecutionNodeCount returns the count of nodes for an execution
func (s *Service) GetExecutionNodeCount(ctx context.Context, executionID uuid.UUID) (int64, error) {
	return s.executionNodeRepo.CountByExecutionID(ctx, executionID)
}

// notifyExecutionUpdated sends an execution updated event via websocket
func (s *Service) notifyExecutionUpdated(execution *workflow_execution.WorkflowExecution) {
	if s.websocketClient == nil {
		return
	}
	executionResult := results.NewWorkflowExecutionResult(execution)
	// The pinned snapshot is the authored document, so the frame follows the
	// REST read of the same run: the full snapshot only to recipients who may
	// read secret values, the redacted one to everyone else.
	redacted := *executionResult
	redacted.RedactAuthoring()
	// Scoped to the OU the run was started under (frozen at start alongside the
	// template snapshot), so members outside it do not learn the run's shape,
	// node count or timing; and to holders of playbook.read, the action every
	// execution read route demands.
	msg := websockets3.NewAccountMessageForOU(execution.AccountID, execution.OUID, websockets3.MessageWrapper{
		Type:    "workflow_execution_updated",
		Payload: executionResult,
	}).RequiringAction(authz.ActionPlaybookRead).
		WithRedactedVariant(authz.ActionSecretReadValue, websockets3.MessageWrapper{
			Type:    "workflow_execution_updated",
			Payload: &redacted,
		})
	if err := s.websocketClient.Send(msg); err != nil {
		s.logger.Error("failed to send workflow execution updated event", "error", err, "execution_id", execution.ID)
	}
}

// notifyExecutionNodeUpdated sends an execution node updated event via websocket
func (s *Service) notifyExecutionNodeUpdated(node *workflow_execution_node.WorkflowExecutionNode) {
	if s.websocketClient == nil {
		return
	}
	// Block outputs are OMITTED from the push, not redacted into placeholders:
	// there is no recipient here to evaluate secret.read_value against, and a
	// placeholder would overwrite the real output a privileged client already
	// fetched over HTTP. Clients merge, so an absent field keeps what they have.
	nodeResult := results.NewWorkflowExecutionNodeResultForPush(node)

	// OU-scoped through the node the run step belongs to.
	var ouID *uuid.UUID
	if n, err := s.nodeRepo.GetByIDAndAccount(context.Background(), node.NodeID, node.AccountID); err == nil && n != nil {
		ouID = n.OUID
	}
	msg := websockets3.NewAccountMessageForOU(node.AccountID, ouID, websockets3.MessageWrapper{
		Type:    "workflow_execution_node_updated",
		Payload: nodeResult,
	}).RequiringAction(authz.ActionPlaybookRead)
	if err := s.websocketClient.Send(msg); err != nil {
		s.logger.Error("failed to send workflow execution node updated event", "error", err, "execution_node_id", node.ID)
	}
}
