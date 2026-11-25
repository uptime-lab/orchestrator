package workflow_execution

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
)

type rcFakeNodeRepo struct {
	repository.NodeRepository
	n   *node.Node
	err error
}

func (r *rcFakeNodeRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	return r.n, r.err
}

type rcFakeResolver struct {
	eff services.EffectiveProvisioning
	err error
}

func (r *rcFakeResolver) ResolveEffectiveProvisioning(_ context.Context, _, _ uuid.UUID) (services.EffectiveProvisioning, error) {
	return r.eff, r.err
}

// buildResolveContext must NOT swallow a resolution error. Substituting an empty
// context into device commands and condition branches is the exact silent-wrong
// behaviour to avoid, so a failure to read node facts or resolve secrets has
// to propagate — the caller then fails the execution node visibly.
func TestBuildResolveContextPropagatesErrors(t *testing.T) {
	execNode := &wen.WorkflowExecutionNode{ID: uuid.New(), AccountID: uuid.New(), NodeID: uuid.New()}
	goodNode := &node.Node{ID: execNode.NodeID, AccountID: execNode.AccountID}

	t.Run("secret resolution error propagates", func(t *testing.T) {
		s := &Service{
			logger:          slog.Default(),
			nodeRepo:        &rcFakeNodeRepo{n: goodNode},
			secretsResolver: &rcFakeResolver{err: errors.New("boom: secret undecryptable")},
		}
		if _, err := s.buildResolveContext(context.Background(), execNode); err == nil {
			t.Fatal("expected an error when secret resolution fails, got nil (secrets would silently be empty)")
		}
	})

	t.Run("node read error propagates", func(t *testing.T) {
		s := &Service{
			logger:          slog.Default(),
			nodeRepo:        &rcFakeNodeRepo{err: errors.New("boom: db down")},
			secretsResolver: &rcFakeResolver{},
		}
		if _, err := s.buildResolveContext(context.Background(), execNode); err == nil {
			t.Fatal("expected an error when the node read fails, got nil")
		}
	})

	t.Run("happy path returns a populated context and no error", func(t *testing.T) {
		s := &Service{
			logger:          slog.Default(),
			nodeRepo:        &rcFakeNodeRepo{n: goodNode},
			secretsResolver: &rcFakeResolver{eff: services.EffectiveProvisioning{Secrets: []services.ResolvedSecret{{Name: "api_token", Value: "V"}}}},
		}
		rc, err := s.buildResolveContext(context.Background(), execNode)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.secrets["api_token"] != "V" {
			t.Errorf("secret not resolved: %v", rc.secrets)
		}
	})
}
