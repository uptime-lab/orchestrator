package results

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// A refusal marked at its site answers with the status and code the site
// chose, through every handler's generic path, wrapped however deep — and
// stays matchable as the error inside it.
func TestARequestRefusalIsAnsweredAsTheSiteDeclared(t *testing.T) {
	inner := errors.New("at least one node ID is required")
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{app_errors.InvalidRequest("no_target_nodes", inner), http.StatusBadRequest, "no_target_nodes"},
		{app_errors.StateConflict("not_netbooted", inner), http.StatusConflict, "not_netbooted"},
	}
	for _, tc := range cases {
		wrapped := fmt.Errorf("start: %w", tc.err)
		rec, body := sendInternal(t, wrapped)
		if rec.Code != tc.status || body.Code != tc.code {
			t.Errorf("answered %d %q, want %d %q", rec.Code, body.Code, tc.status, tc.code)
		}
		if body.Message != inner.Error() {
			t.Errorf("message = %q, want the refusal's own text without the wrapping", body.Message)
		}
		if !errors.Is(wrapped, inner) {
			t.Error("the inner error is no longer matchable")
		}
		if !IsClientRefusal(wrapped) {
			t.Error("a typed 4xx refusal must log at WARN, not ERROR")
		}
	}
	if app_errors.InvalidRequest("x", nil) != nil || app_errors.StateConflict("x", nil) != nil {
		t.Error("wrapping nil must stay nil")
	}
}
