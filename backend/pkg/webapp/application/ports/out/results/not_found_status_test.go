package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// The lookup sentinels a request names by id answer 404 through the generic
// error path, wrapped however deep, with the sentinel's own text and never the
// wrapping context — and errors.Is still matches them.
func TestLookupSentinelsAnswer404ThroughTheGenericPath(t *testing.T) {
	for _, sentinel := range []error{
		app_errors.ErrNodeNotFound,
		app_errors.ErrOrganizationalUnitNotFound,
		app_errors.ErrSSHKeyNotFound,
		app_errors.ErrNodeCommandNotFound,
	} {
		wrapped := fmt.Errorf("invalid target /internal/path: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Fatalf("errors.Is lost the sentinel %v", sentinel)
		}
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		if err := SendInternalServerError(c, "operation failed", wrapped); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%v answered %d, want 404", sentinel, rec.Code)
		}
		var body struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Message != sentinel.Error() {
			t.Fatalf("message = %q, want the sentinel text %q only", body.Message, sentinel.Error())
		}
	}
}
