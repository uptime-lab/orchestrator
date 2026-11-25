// Package node_replace orchestrates replacing a failed node with a spare one:
// it transfers the source node's swappable identity (custom attributes +
// hostname + OU membership) onto the target, optionally kicks off a playbook on
// the target to re-apply that identity to external services (set_hostname,
// CI re-registration, ...), and strips the attributes off the source so the
// old node no longer claims the identity.
package node_replace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	ou_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/organizational_unit"
	secret_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/secret"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
)

// ErrSameNode is returned when source and target are the same node.
var ErrSameNode = errors.New("cannot replace a node with itself")

// ErrPlaybookRefused wraps the playbook start's own refusal (the template,
// its scope, a run cap), asked before anything is transferred, so the caller
// can answer it as the runs route does.
var ErrPlaybookRefused = errors.New("the playbook cannot be started")

// The attribute key under which the source hostname is transferred so a
// playbook can apply it on the target via {{node.attributes.previous_hostname}}.
// Deliberately NOT "hostname": that key collides with the node's own identity
// hostname fact and showed up as a confusing duplicate row in the UI.
const hostnameAttributeKey = "previous_hostname"

// Result summarizes a replace operation.
type Result struct {
	AttributesTransferred int        `json:"attributes_transferred"`
	Hostname              string     `json:"hostname"`
	OUTransferred         bool       `json:"ou_transferred"`
	ExecutionID           *uuid.UUID `json:"execution_id,omitempty"`
}

// Service performs node replacement. Every dependency is required.
type Service struct {
	nodeRepo  repository.NodeRepository
	secrets   attributeStore
	ous       unitAssigner
	workflows runStarter
	logger    *slog.Logger
}

// attributeStore, unitAssigner and runStarter are the parts of the secret,
// unit and run services a replace uses.
type attributeStore interface {
	ListNodeAttributes(ctx context.Context, accountID, nodeID uuid.UUID) ([]secret_domain.NodeAttribute, error)
	UpsertNodeAttribute(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, key, value string, sensitive bool) (*secret_domain.NodeAttribute, error)
	DeleteNodeAttribute(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, key string) error
}

type unitAssigner interface {
	AssignNode(ctx context.Context, actor audit.Actor, accountID, nodeID uuid.UUID, ouID *uuid.UUID) error
}

type runStarter interface {
	CheckStart(ctx context.Context, cmd workflow_execution_start.WorkflowExecutionStartCommand, ouOverride map[uuid.UUID]*uuid.UUID) error
	StartExecution(ctx context.Context, cmd workflow_execution_start.WorkflowExecutionStartCommand) (*workflow_execution.WorkflowExecution, error)
}

// NewService wires the node replace service.
func NewService(
	nodeRepo repository.NodeRepository,
	secrets *secret_service.Service,
	ous *ou_service.Service,
	workflows *workflow_execution_service.Service,
	logger *slog.Logger,
) *Service {
	if nodeRepo == nil || secrets == nil || ous == nil || workflows == nil {
		panic("node_replace.Service requires all dependencies")
	}
	return &Service{nodeRepo: nodeRepo, secrets: secrets, ous: ous, workflows: workflows, logger: logger.With("service", "node_replace")}
}

// Replace transfers the source node's identity onto the target, optionally runs
// templateID on the target, then strips the source's custom attributes. The
// node records themselves are kept.
func (s *Service) Replace(ctx context.Context, auth commands.AuthContext, sourceID, targetID uuid.UUID, templateID *uuid.UUID) (*Result, error) {
	if sourceID == targetID {
		return nil, ErrSameNode
	}
	accountID := auth.AccountID
	// A replace is a human action, so the attribute writes it performs are
	// attributed to that human rather than to the platform — the source node's
	// identity moving to different hardware is exactly the kind of change someone
	// asks about later.
	actor := audit.UserActor(auth.AccountID, auth.UserID)

	source, err := s.nodeRepo.GetByID(ctx, accountID, sourceID)
	if err != nil {
		return nil, err
	}
	if _, err := s.nodeRepo.GetByID(ctx, accountID, targetID); err != nil {
		return nil, err
	}

	// Everything that can refuse the playbook start is asked BEFORE the
	// identity moves: a refusal after step 3 would leave both nodes holding
	// the identity. The scope test sees the target in the unit it is about
	// to join.
	var start workflow_execution_start.WorkflowExecutionStartCommand
	if templateID != nil {
		start = workflow_execution_start.WorkflowExecutionStartCommand{
			TemplateID:  *templateID,
			NodeIDs:     []uuid.UUID{targetID},
			AuthContext: auth,
		}
		var moved map[uuid.UUID]*uuid.UUID
		if source.OUID != nil {
			moved = map[uuid.UUID]*uuid.UUID{targetID: source.OUID}
		}
		if err := s.workflows.CheckStart(ctx, start, moved); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPlaybookRefused, err)
		}
	}

	// 1. Clone the source's custom attributes onto the target (values decrypted
	//    by the secret repo, so sensitive ones carry over intact).
	attrs, err := s.secrets.ListNodeAttributes(ctx, accountID, sourceID)
	if err != nil {
		return nil, err
	}
	for i := range attrs {
		// An attribute the platform cannot decrypt reads as an empty string, and
		// copying that would hand the replacement node a blank credential while
		// reporting a successful transfer. Refuse the whole replace: half a
		// transferred identity is worse than none.
		if attrs[i].Unreadable {
			return nil, fmt.Errorf("attribute %q of the source node: %w", attrs[i].Key, app_errors.ErrSecretValueUnreadable)
		}
		if _, err := s.secrets.UpsertNodeAttribute(ctx, actor, accountID, targetID, attrs[i].Key, attrs[i].Value, attrs[i].Sensitive); err != nil {
			return nil, err
		}
	}

	// 2. Transfer the hostname as an attribute so a playbook can re-apply it on
	//    the target (set_hostname reading {{node.attributes.previous_hostname}}).
	result := &Result{AttributesTransferred: len(attrs)}
	if source.Hostname != "" {
		if _, err := s.secrets.UpsertNodeAttribute(ctx, actor, accountID, targetID, hostnameAttributeKey, source.Hostname, false); err != nil {
			return nil, err
		}
		result.Hostname = source.Hostname
	}

	// 3. Transfer OU membership so the target inherits the same policies/secrets.
	if source.OUID != nil {
		if err := s.ous.AssignNode(ctx, actor, accountID, targetID, source.OUID); err != nil {
			return nil, err
		}
		result.OUTransferred = true
	}

	// 4. Optionally run a playbook on the target to apply the identity to
	//    external services.
	//    The checks were asked before the transfer; only a cap filled in
	//    between can still refuse here, and that is NOT ErrPlaybookRefused:
	//    the identity has moved and the source was not stripped.
	if templateID != nil {
		exec, err := s.workflows.StartExecution(ctx, start)
		if err != nil {
			return nil, err
		}
		result.ExecutionID = &exec.ID
	}

	// 5. Strip the source's custom attributes so it no longer claims the
	//    identity (the node record is kept for history/diagnostics).
	for i := range attrs {
		if err := s.secrets.DeleteNodeAttribute(ctx, actor, accountID, sourceID, attrs[i].Key); err != nil {
			s.logger.WarnContext(ctx, "failed to strip source attribute", "error", err, "node_id", sourceID, "key", attrs[i].Key)
		}
	}

	s.logger.InfoContext(ctx, "node replaced", "source", sourceID, "target", targetID,
		"attributes", result.AttributesTransferred, "ou_transferred", result.OUTransferred, "execution", result.ExecutionID)
	return result, nil
}
