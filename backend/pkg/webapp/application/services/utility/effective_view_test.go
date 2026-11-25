package utility

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

type effectiveFakeStates struct {
	repository.NodeUtilityStateRepository
	rows []utility.NodeState
}

func (f *effectiveFakeStates) ListByNode(context.Context, uuid.UUID, uuid.UUID) ([]utility.NodeState, error) {
	return f.rows, nil
}

// The effective view must hash the way the APPLY hashes — over RESOLVED params
// — or a config carrying {{node.attributes.*}} shows update_pending forever
// (desired computed over the reference, applied recorded over the value). The
// returned Config must KEEP the reference: the view answers at utility.read
// and resolved values in it would widen what a reader sees.
func TestEffectiveForNodeHashesResolvedParamsButReturnsReferences(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	rawConfig := map[string]interface{}{"url": "{{node.attributes.url}}/dash"}
	resolvedHash := utility.HashConfig("kiosk", map[string]interface{}{"url": "http://a/dash"})

	svc := &Service{
		catalog: &utility.Catalog{Utilities: []utility.Definition{{ID: "kiosk", Name: "Kiosk"}}},
		assignments: &settleFakeAssignments{rows: []utility.Assignment{{
			ID: uuid.New(), AccountID: accountID, UtilityID: "kiosk",
			AccountWide: true, Enabled: true, Config: rawConfig,
		}}},
		states:   &effectiveFakeStates{rows: []utility.NodeState{{UtilityID: "kiosk", AppliedHash: resolvedHash}}},
		nodeRepo: &settleFakeNodeRepo{},
		secretsResolver: fakeResolver{eff: services.EffectiveProvisioning{
			Secrets: []services.ResolvedSecret{
				{Name: secret_domain.NodeAttributeName(nodeID, "url"), Key: "url", Value: "http://a", Sensitive: false},
			},
		}},
		logger: slog.Default(),
	}

	state, err := svc.EffectiveForNode(context.Background(), accountID, nodeID)
	if err != nil {
		t.Fatalf("EffectiveForNode: %v", err)
	}
	if len(state.Resolved) != 1 {
		t.Fatalf("want 1 resolved utility, got %d", len(state.Resolved))
	}
	got := state.Resolved[0]
	if got.ConfigHash != resolvedHash {
		t.Fatalf("hash must be computed over RESOLVED params (apply semantics): got %q want %q", got.ConfigHash, resolvedHash)
	}
	if url, _ := got.Config["url"].(string); !strings.Contains(url, "{{node.attributes.url}}") {
		t.Fatalf("the view must return the UNRESOLVED reference, got %q", url)
	}
}

// An unresolvable reference must not fail the view: the apply would refuse the
// config too, so the honest answer is the unresolved hash (update_pending).
func TestEffectiveForNodeSurvivesUnresolvableReferences(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	rawConfig := map[string]interface{}{"url": "{{secrets.absent}}/x"}
	svc := &Service{
		catalog: &utility.Catalog{Utilities: []utility.Definition{{ID: "kiosk", Name: "Kiosk"}}},
		assignments: &settleFakeAssignments{rows: []utility.Assignment{{
			ID: uuid.New(), AccountID: accountID, UtilityID: "kiosk",
			AccountWide: true, Enabled: true, Config: rawConfig,
		}}},
		states:          &effectiveFakeStates{},
		nodeRepo:        &settleFakeNodeRepo{},
		secretsResolver: fakeResolver{eff: services.EffectiveProvisioning{}},
		logger:          slog.Default(),
	}
	state, err := svc.EffectiveForNode(context.Background(), accountID, nodeID)
	if err != nil {
		t.Fatalf("an unresolvable reference must not fail the view: %v", err)
	}
	if state.Resolved[0].ConfigHash != utility.HashConfig("kiosk", rawConfig) {
		t.Fatal("unresolvable references keep the unresolved hash")
	}
}
