package workflow_execution

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
)

// A start that names no node is the request's fault, refused before any
// lookup; it reached the caller as a 500.
func TestAStartWithNoTargetsIsARequestRefusal(t *testing.T) {
	err := (&Service{}).CheckStart(context.Background(), workflow_execution_start.WorkflowExecutionStartCommand{
		TemplateID: uuid.New(), AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	}, nil)
	var refusal *app_errors.RequestRefusalError
	if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusBadRequest || refusal.ErrorCode() != "no_target_nodes" {
		t.Fatalf("err = %v, want a typed 400 no_target_nodes", err)
	}
}
