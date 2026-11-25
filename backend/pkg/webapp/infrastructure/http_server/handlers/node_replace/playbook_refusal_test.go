package node_replace

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	node_replace_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node_replace"
)

// A replace whose playbook cannot start is an ordinary refusal, answered as
// the runs route answers it, not a server fault.
func TestReplacePlaybookRefusalsAreTyped(t *testing.T) {
	cases := map[error]int{
		app_errors.ErrWorkflowTemplateNotActive: http.StatusConflict,
		app_errors.ErrTooManyActiveExecutions:   http.StatusTooManyRequests,
		app_errors.ErrTooManyRunningNodes:       http.StatusTooManyRequests,
		app_errors.ErrForbidden:                 http.StatusForbidden,
		app_errors.ErrNotFound:                  http.StatusNotFound,
	}
	for cause, want := range cases {
		err := fmt.Errorf("%w: %w", node_replace_service.ErrPlaybookRefused, cause)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)
		if e := sendReplaceError(c, err); e != nil {
			t.Fatal(e)
		}
		if rec.Code != want {
			t.Errorf("%v: status %d, want %d", cause, rec.Code, want)
		}
	}
}
