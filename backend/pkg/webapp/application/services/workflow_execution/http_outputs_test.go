package workflow_execution

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// The SSRF guard refuses an internal address at dial time — which is the whole
// point of it — so an httptest server on 127.0.0.1 cannot be reached through the
// production client. `httpClientFor` is the seam that lets these drive
// execHTTPRequest itself, so the test covers the CALL SITE and not just the
// helper it calls.
func outputsRun(policy *workflow_template.BlockPolicy) (*engineRun, workflow_template.GraphNode) {
	node := workflow_template.GraphNode{
		ID:     "api",
		Type:   workflow_block.TypeHTTPRequest,
		Policy: policy,
	}
	r := &engineRun{
		svc:   &Service{logger: slog.New(slog.DiscardHandler)},
		ctx:   context.Background(),
		graph: &workflow_template.WorkflowGraph{Nodes: []workflow_template.GraphNode{node}},
		execNode: &wen.WorkflowExecutionNode{
			ID: uuid.New(), AccountID: uuid.New(), NodeID: uuid.New(),
			GraphState: map[string]wen.GraphNodeState{},
		},
	}
	r.rc = resolveContext{nodeFacts: map[string]string{}, secrets: map[string]string{}, outputs: r.execNode.GraphState}
	return r, node
}

// A failed call must keep its status and body. `completeAndAdvance` is the only
// writer of Outputs, so handing straight off to handleFailure settled the block
// with an EMPTY map — and on an on_error=continue policy that is a COMPLETED
// block whose `status` the operator was told to branch on resolves to "". A
// condition then compares "" and takes the false port, and the API's own error
// message exists nowhere, because Error holds only "HTTP 429".
func TestOutputsSurviveAContinuePolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"slow down"}`))
	}))
	defer srv.Close()
	restore := useDirectHTTPClient(t)
	defer restore()

	r, node := outputsRun(&workflow_template.BlockPolicy{OnError: workflow_template.OnErrorContinue})
	node.Params = map[string]interface{}{"method": "GET", "url": srv.URL}

	r.execHTTPRequest("api", node)

	st := r.execNode.GraphState["api"]
	if st.Outputs["status"] != "429" {
		t.Errorf("outputs[status] = %q, want \"429\"", st.Outputs["status"])
	}
	if st.Outputs["body"] == "" {
		t.Error("the response body was discarded, so the API's own error message exists nowhere")
	}
	if st.Status != wen.GraphNodeCompleted {
		t.Errorf("block status = %q, want %q under on_error=continue", st.Status, wen.GraphNodeCompleted)
	}
	// The resolver reads exactly this map, so assert through it rather than
	// trusting the shape.
	if got := r.rc.lookup("blocks.api.outputs.status"); got != "429" {
		t.Errorf("{{blocks.api.outputs.status}} = %q, want \"429\"", got)
	}
}

// They survive a terminal failure too — the run's page is where an operator
// looks for what the call actually answered.
func TestOutputsSurviveATerminalFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	restore := useDirectHTTPClient(t)
	defer restore()

	r, node := outputsRun(nil)
	node.Params = map[string]interface{}{"method": "GET", "url": srv.URL}

	r.execHTTPRequest("api", node)

	st := r.execNode.GraphState["api"]
	if st.Status != wen.GraphNodeFailed {
		t.Fatalf("block status = %q, want %q", st.Status, wen.GraphNodeFailed)
	}
	if st.Outputs["body"] != "boom" {
		t.Errorf("outputs[body] = %q, want it kept", st.Outputs["body"])
	}
}

// The catalog must not advertise an output the engine never writes. `json` was
// promised and produced on no path, so
// {{blocks.<id>.outputs.json.token}} — the exact use the description invited,
// feeding server.secret_upsert — resolved to "" and wrote an EMPTY VALUE over a
// live secret while the run reported COMPLETED.
func TestHTTPRequestDescriptionOnlyPromisesWhatItWrites(t *testing.T) {
	block, ok := workflow_block.FindByType(workflow_block.TypeHTTPRequest)
	if !ok {
		t.Fatal("the http_request block is not in the registry")
	}
	// The operator-facing contract now spans two tiers: Description (inline)
	// plus Details (hover). The over-promise and coverage checks apply to the
	// combined text.
	doc := block.Description + " " + block.Details
	if strings.Contains(doc, "json") {
		t.Error("the block copy still promises a json output that execHTTPRequest never writes")
	}
	for _, produced := range []string{"status", "body"} {
		if !strings.Contains(doc, produced) {
			t.Errorf("the block copy no longer mentions %q, which the block does produce", produced)
		}
	}
}

// useDirectHTTPClient swaps the SSRF-guarded client for a plain one so a test
// server on loopback is reachable, and restores it afterwards.
func useDirectHTTPClient(t *testing.T) func() {
	t.Helper()
	previous := httpClientFor
	httpClientFor = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout}
	}
	return func() { httpClientFor = previous }
}
