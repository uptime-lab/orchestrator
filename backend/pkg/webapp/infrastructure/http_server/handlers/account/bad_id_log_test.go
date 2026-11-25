package account

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// A malformed id in the path is the client's mistake: 400 to the client and a
// WARN in the log, never an ERROR that reads like a server fault.
func TestAMalformedAccountIDIsAWarningNotAnError(t *testing.T) {
	var buf bytes.Buffer
	h := NewAccountHandler(&getAccountService{}, 365, slog.New(slog.NewTextHandler(&buf, nil)))
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/accounts/x/policy", nil), rec)
	c.SetParamNames("id")
	c.SetParamValues(uuid.NewString() + "/policy")
	c.Set("accountID", uuid.NewString())

	if err := h.GetAccount(c); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("logged at ERROR: %s", buf.String())
	}
}
