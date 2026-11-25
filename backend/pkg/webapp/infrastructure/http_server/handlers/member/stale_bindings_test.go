package member

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// A stale bindings write is a 409 the client can branch on (reload, then
// re-apply), told apart from the last-super-admin 409 by its code.
func TestAStaleBindingsWriteIsAConflictWithAStableCode(t *testing.T) {
	h := &MemberHandler{logger: slog.Default()}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodPut, "/", nil), rec)

	if err := h.sendMemberError(c, fmt.Errorf("wrapped: %w", app_errors.ErrMemberBindingsStale)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != app_errors.MemberBindingsStaleCode {
		t.Fatalf("code = %q, want %q", body.Code, app_errors.MemberBindingsStaleCode)
	}
}
