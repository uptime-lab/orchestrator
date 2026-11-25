package workflow_execution

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// Why these tests exist. The block discarded the body read
// error and completed every response below 400: a server that sent 200 and
// part of a body before dropping the connection produced a COMPLETED block
// whose downstream ran on incomplete data with no word about it. And a body
// deliberately cut at the 64 KiB cap looked exactly like a body that ended.

func TestHTTPBlockFailsWhenTheBodyBreaksMidStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			// A hard close mid-body: the client sees an unexpected EOF.
			if tc, ok := conn.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	restore := useDirectHTTPClient(t)
	defer restore()

	r, node := outputsRun(nil)
	node.Params = map[string]interface{}{"method": "GET", "url": srv.URL}
	r.execHTTPRequest("api", node)

	st := r.execNode.GraphState["api"]
	if st.Status != wen.GraphNodeFailed {
		t.Fatalf("a broken body completed the block: status %q, error %q", st.Status, st.Error)
	}
	if !strings.Contains(st.Error, "response body read failed") {
		t.Fatalf("error = %q, want the body read failure named", st.Error)
	}
	if st.Outputs["status"] != "200" || st.Outputs["body"] != "partial" {
		t.Fatalf("the partial outputs must be kept for the on_error branch: %+v", st.Outputs)
	}
}

func TestHTTPBlockNamesADeliberateTruncation(t *testing.T) {
	big := strings.Repeat("x", 70*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	restore := useDirectHTTPClient(t)
	defer restore()

	r, node := outputsRun(nil)
	node.Params = map[string]interface{}{"method": "GET", "url": srv.URL}
	r.execHTTPRequest("api", node)

	st := r.execNode.GraphState["api"]
	if st.Status != wen.GraphNodeCompleted {
		t.Fatalf("a large but complete body failed the block: %q", st.Error)
	}
	if len(st.Outputs["body"]) != 64*1024 || st.Outputs["truncated"] != "true" {
		t.Fatalf("body len %d truncated=%q, want 65536 and \"true\"", len(st.Outputs["body"]), st.Outputs["truncated"])
	}
}

func TestHTTPBlockCompletesACompleteBodyWithoutATruncationMark(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("whole"))
	}))
	defer srv.Close()
	restore := useDirectHTTPClient(t)
	defer restore()

	r, node := outputsRun(nil)
	node.Params = map[string]interface{}{"method": "GET", "url": srv.URL}
	r.execHTTPRequest("api", node)

	st := r.execNode.GraphState["api"]
	if st.Status != wen.GraphNodeCompleted || st.Outputs["body"] != "whole" {
		t.Fatalf("control: %q %+v", st.Status, st.Outputs)
	}
	if _, marked := st.Outputs["truncated"]; marked {
		t.Fatal("a complete body carried the truncation mark")
	}
	_ = workflow_template.OnErrorContinue
}
