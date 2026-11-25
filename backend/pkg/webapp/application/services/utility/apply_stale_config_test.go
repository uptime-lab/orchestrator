package utility

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// An assignment saved before its schema tightened (a param made required with
// no default — e.g. a kiosk image param) must be refused at apply with a
// reason naming the utility and the remedy, never dispatched: the agent would
// fail it on every node the assignment reaches.
func TestApplyToNodeSkipsConfigMissingRequiredParam(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	catalog := &utility.Catalog{Utilities: []utility.Definition{{
		ID: "kiosk", Name: "Kiosk", Class: utility.ClassReconciled,
		Params: []workflow_block.ParamSpec{
			{Name: "url", Type: workflow_block.ParamString},
			{Name: "image", Type: workflow_block.ParamString, Required: true},
		},
	}}}
	rows := []utility.Assignment{{
		ID:          uuid.New(),
		AccountID:   accountID,
		UtilityID:   "kiosk",
		AccountWide: true,
		Enabled:     true,
		// Saved when image still had a default: no image key at all.
		Config: map[string]interface{}{"url": "http://kiosk.example/panel"},
	}}
	dispatcher := &captureDispatcher{}
	svc := &Service{
		catalog:     catalog,
		assignments: &settleFakeAssignments{rows: rows},
		states:      applyFakeStates{},
		nodeRepo:    &settleFakeNodeRepo{},
		logger:      slog.Default(),
		dispatcher:  dispatcher,
	}

	res, err := svc.ApplyToNode(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The invalid utility is the ONLY one here, so there is nothing left to
	// dispatch and the report carries the reason.
	if !res.Skipped {
		t.Fatal("a config missing a required param must not dispatch")
	}
	if !strings.Contains(res.Reason, "kiosk") || !strings.Contains(res.Reason, "edit the assignment") {
		t.Fatalf("reason must name the utility and the remedy, got %q", res.Reason)
	}
	if dispatcher.cmd.Command != "" {
		t.Fatalf("nothing may be dispatched, got %q", dispatcher.cmd.Command)
	}
}

// A stale config must not hold back the utilities that are fine. Refusing the
// whole node meant one unfixed assignment silently blocked every sibling on
// that node forever — and, once the reconcile loop existed, on every report.
func TestApplyToNodeDropsOnlyTheInvalidUtility(t *testing.T) {
	accountID, nodeID := uuid.New(), uuid.New()
	catalog := &utility.Catalog{Utilities: []utility.Definition{
		{
			ID: "kiosk", Name: "Kiosk", Class: utility.ClassReconciled,
			Params: []workflow_block.ParamSpec{
				{Name: "image", Type: workflow_block.ParamString, Required: true},
			},
		},
		{
			ID: "docker", Name: "Docker", Class: utility.ClassReconciled,
			Params: []workflow_block.ParamSpec{
				{Name: "channel", Type: workflow_block.ParamString, Default: "distro"},
			},
		},
	}}
	rows := []utility.Assignment{
		{ID: uuid.New(), AccountID: accountID, UtilityID: "kiosk", AccountWide: true, Enabled: true,
			Config: map[string]interface{}{}}, // saved when image still had a default
		{ID: uuid.New(), AccountID: accountID, UtilityID: "docker", AccountWide: true, Enabled: true,
			Config: map[string]interface{}{}},
	}
	recorder := &recordingStates{}
	dispatcher := &captureDispatcher{}
	svc := &Service{
		catalog:     catalog,
		assignments: &settleFakeAssignments{rows: rows},
		states:      recorder,
		nodeRepo:    &settleFakeNodeRepo{},
		logger:      slog.Default(),
		dispatcher:  dispatcher,
	}

	res, err := svc.ApplyToNode(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Skipped {
		t.Fatalf("the valid utility must still dispatch, got skipped: %s", res.Reason)
	}
	// Exactly one utility rides the payload: docker.
	list, _ := dispatcher.cmd.Params["utilities"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("payload carries %d utilities, want only the valid one", len(list))
	}
	entry, _ := list[0].(map[string]interface{})
	if entry["utility_id"] != "docker" {
		t.Fatalf("payload carries %v, want docker", entry["utility_id"])
	}
	// The dropped one's rung records WHY, which is also what feeds the
	// reconcile loop's 3-strike cap.
	if reason, ok := recorder.failed["kiosk"]; !ok || !strings.Contains(reason, "edit the assignment") {
		t.Fatalf("kiosk failure = %q (recorded: %v), want the remedy", reason, recorder.failed)
	}
}

// recordingStates captures the per-utility failures the apply records.
type recordingStates struct {
	applyFakeStates
	failed map[string]string
}

func (r *recordingStates) RecordFailed(_ context.Context, _, _ uuid.UUID, utilityID, msg string) error {
	if r.failed == nil {
		r.failed = map[string]string{}
	}
	r.failed[utilityID] = msg
	return nil
}
