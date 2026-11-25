package workflow_execution

import (
	"testing"
	"time"

	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

func TestBlockTimedOut(t *testing.T) {
	r := &engineRun{}
	past := time.Now().Add(-30 * time.Second)
	recent := time.Now().Add(-2 * time.Second)

	cases := []struct {
		name    string
		policy  *workflow_template.BlockPolicy
		started *time.Time
		want    bool
	}{
		{"no policy", nil, &past, false},
		{"zero timeout", &workflow_template.BlockPolicy{TimeoutSeconds: 0}, &past, false},
		{"not started", &workflow_template.BlockPolicy{TimeoutSeconds: 10}, nil, false},
		{"within deadline", &workflow_template.BlockPolicy{TimeoutSeconds: 10}, &recent, false},
		{"past deadline", &workflow_template.BlockPolicy{TimeoutSeconds: 10}, &past, true},
	}
	for _, tc := range cases {
		node := workflow_template.GraphNode{ID: "b", Policy: tc.policy}
		st := wen.GraphNodeState{StartedAt: tc.started}
		if got := r.blockTimedOut(node, st); got != tc.want {
			t.Errorf("%s: blockTimedOut = %v, want %v", tc.name, got, tc.want)
		}
	}
}
