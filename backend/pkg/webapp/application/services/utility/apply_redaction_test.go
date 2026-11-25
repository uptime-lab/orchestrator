package utility

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

type captureDispatcher struct {
	cmd node_command_cmd.BatchNodeCommandCommand
}

func (c *captureDispatcher) SendBatchCommand(_ context.Context, cmd node_command_cmd.BatchNodeCommandCommand) (*node_command_cmd.BatchCommandResult, error) {
	c.cmd = cmd
	id := uuid.New()
	return &node_command_cmd.BatchCommandResult{
		Results: []node_command_cmd.NodeCommandResultItem{{Success: true, CommandID: &id}},
	}, nil
}

type applyFakeStates struct {
	repository.NodeUtilityStateRepository
}

func (applyFakeStates) SetDesired(context.Context, uuid.UUID, uuid.UUID, string, string, string) error {
	return nil
}
func (applyFakeStates) DeleteMissing(context.Context, uuid.UUID, uuid.UUID, []string) error {
	return nil
}

// RecordFailed is exercised by the apply path when a stored config no longer
// validates (the utility is dropped and its rung records why), so the shared
// fake has to answer it rather than fall through to the nil embedded
// interface.
func (applyFakeStates) RecordFailed(context.Context, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}

// applyFixture wires the narrowest ApplyToNode that dispatches: one
// account-wide assignment for a catalog utility whose `url` param carries the
// caller's text.
func applyFixture(t *testing.T, urlParam string, secrets []services.ResolvedSecret) (*Service, *captureDispatcher, uuid.UUID, uuid.UUID) {
	t.Helper()
	accountID, nodeID := uuid.New(), uuid.New()
	catalog := &utility.Catalog{Utilities: []utility.Definition{{
		ID: "kiosk", Name: "Kiosk", Class: utility.ClassReconciled,
		// The param must be DECLARED: ApplyToNode re-validates the resolved
		// config against the schema and skips on an unknown or missing
		// required param rather than dispatching it.
		Params: []workflow_block.ParamSpec{{Name: "url", Type: workflow_block.ParamString}},
	}}}
	rows := []utility.Assignment{{
		ID:          uuid.New(),
		AccountID:   accountID,
		UtilityID:   "kiosk",
		AccountWide: true,
		Enabled:     true,
		Config:      map[string]interface{}{"url": urlParam},
	}}
	dispatcher := &captureDispatcher{}
	svc := &Service{
		catalog:         catalog,
		assignments:     &settleFakeAssignments{rows: rows},
		states:          applyFakeStates{},
		nodeRepo:        &settleFakeNodeRepo{},
		logger:          slog.Default(),
		dispatcher:      dispatcher,
		secretsResolver: fakeResolver{eff: services.EffectiveProvisioning{Secrets: secrets}},
	}
	return svc, dispatcher, accountID, nodeID
}

// A resolved {{node.attributes.*}} / {{secrets.*}} value rides inside
// params_json, so without the mark it lands in a payload every node.read caller
// can list — Tailscale's auth_key is the motivating case. The mark is what
// routes it through the same redaction the workflow
// engine's blocks use.
func TestApplyToNodeMarksResolvedParamsForRedaction(t *testing.T) {
	nodeID := uuid.New()
	svc, dispatcher, accountID, _ := applyFixture(t,
		"http://{{node.attributes.dash_host}}/panel",
		[]services.ResolvedSecret{{
			Name:  secret_domain.NodeAttributeName(nodeID, "dash_host"),
			Key:   "dash_host",
			Value: "kiosk.internal",
		}},
	)

	if _, err := svc.ApplyToNode(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := dispatcher.cmd.SecretDerivedParams
	if len(got) != 1 || got[0] != "utilities" {
		t.Fatalf("SecretDerivedParams = %v, want [utilities] — the resolved value is readable at node.read without it", got)
	}

	// The other half — that marking "utilities" actually hides the value — is
	// asserted in the results package (TestRedactCommandPayloadHidesUtilitySet);
	// importing it here is an import cycle.
	if !strings.Contains(fmt.Sprintf("%v", dispatcher.cmd.Params["utilities"]), "kiosk.internal") {
		t.Fatalf("fixture is not exercising a resolved value: %v", dispatcher.cmd.Params["utilities"])
	}
}

// The common case — no references at all — must stay fully readable, or every
// apply_utility_config becomes opaque for the sake of the few that resolve
// something.
func TestApplyToNodeLeavesLiteralParamsReadable(t *testing.T) {
	svc, dispatcher, accountID, nodeID := applyFixture(t, "http://kiosk.example/panel", nil)

	if _, err := svc.ApplyToNode(context.Background(), accountID, uuid.New(), nodeID); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := dispatcher.cmd.SecretDerivedParams; len(got) != 0 {
		t.Fatalf("SecretDerivedParams = %v, want none for a payload with no resolved references", got)
	}
}
