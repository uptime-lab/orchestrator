package workflow_execution

import (
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// A server block's write is recorded against the member the run acts for: the
// one who started it, or for a scheduled run the playbook's author, whose
// right the scheduler re-checks before each fire. "system" is left for a run
// with neither, so an audit reader can tell who caused a secret to change.
func TestSecretUpsertRecordsTheMemberTheRunActsFor(t *testing.T) {
	starter, author := uuid.New(), uuid.New()
	cases := []struct {
		name      string
		execution *workflow_execution.WorkflowExecution
		authorID  *uuid.UUID
		want      uuid.UUID
	}{
		{"started by a member", &workflow_execution.WorkflowExecution{StartedBy: &starter, StartedKind: workflow_execution.StartedByUser}, &author, starter},
		{"scheduled, author recorded", &workflow_execution.WorkflowExecution{StartedKind: workflow_execution.StartedByScheduler}, &author, author},
		{"scheduled, no author", &workflow_execution.WorkflowExecution{StartedKind: workflow_execution.StartedByScheduler}, nil, uuid.Nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer := &stubSecretWriter{}
			r, _ := attributeUpsertRun(writer, nil, resolveContext{})
			r.execNode.ExecutionID = uuid.New()
			r.execution = tc.execution
			r.template = &workflow_template.WorkflowTemplate{AuthorID: tc.authorID}
			node := workflow_template.GraphNode{ID: "b", Type: workflow_block.TypeSecretUpsert,
				Params: map[string]interface{}{"name": "token", "value": "v", "kind": "opaque"}}
			r.execSecretUpsert("b", node)

			if writer.upsertCalls != 1 {
				t.Fatalf("expected one UpsertForNode call, got %d", writer.upsertCalls)
			}
			if writer.upsertActor.UserID != tc.want || writer.upsertActor.AccountID != r.execNode.AccountID {
				t.Errorf("actor = %+v, want user %s in the run's account", writer.upsertActor, tc.want)
			}
			if writer.upsertOpts.RunID != r.execNode.ExecutionID {
				t.Errorf("run id = %s, want %s", writer.upsertOpts.RunID, r.execNode.ExecutionID)
			}
		})
	}
}
