package workflow

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_template_import"
)

// A body that does not decode is described in the API's terms — the JSON
// field and the kind it must be — and never in the server's Go type names.
func TestBindErrorsAreWordedWithoutGoTypeNames(t *testing.T) {
	cases := map[string]string{
		`{"document":"a string"}`: "invalid request: document must be an object, not string",
		`{"document":`:            "invalid request",
		`"x"`:                     "invalid request: the body must be an object, not string",
	}
	for body, want := range cases {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		c := echo.New().NewContext(req, httptest.NewRecorder())
		var cmd workflow_template_import.WorkflowTemplateImportCommand
		err := c.Bind(&cmd)
		if err == nil {
			t.Fatalf("%s: bound without an error", body)
		}
		got := bindErrorMessage(err)
		if !strings.HasPrefix(got, want) {
			t.Errorf("%s: message = %q, want prefix %q", body, got, want)
		}
		for _, leak := range []string{"workflow_template_import", "PortableDocument", "Unmarshal", "code="} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: message %q leaks %q", body, got, leak)
			}
		}
	}
}
