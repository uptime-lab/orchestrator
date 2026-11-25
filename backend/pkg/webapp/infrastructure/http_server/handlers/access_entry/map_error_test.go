package access_entry

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
)

// An invalid certificate principal is a client error, not a server error: it must
// map to 400, not 500 (it answered 500 while ErrPrincipalInvalid was missing
// from mapError's known-error list).
func TestMapErrorMapsInvalidPrincipalTo400(t *testing.T) {
	h := &Handler{logger: slog.Default()}
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)

	if err := h.mapError(c, access_entry.ErrPrincipalInvalid); err != nil {
		t.Fatalf("mapError returned an error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// A target outside the account answers 404 through the generic path; it is
// a refusal the API gives, and is not logged at ERROR. A real failure still
// is.
func TestMapErrorLogsARefusalBelowErrorAndAFailureAtError(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		wantStatus int
		wantError  bool
	}{
		"a node outside the account": {fmt.Errorf("resolving the target: %w", app_errors.ErrNodeNotFound), http.StatusNotFound, false},
		"a database failure":         {errors.New("connection reset"), http.StatusInternalServerError, true},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			h := &Handler{logger: slog.New(slog.NewTextHandler(&buf, nil))}
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)
			if err := h.mapError(c, tc.err); err != nil {
				t.Fatalf("mapError returned an error: %v", err)
			}
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := strings.Contains(buf.String(), "level=ERROR"); got != tc.wantError {
				t.Errorf("logged at ERROR = %v, want %v:\n%s", got, tc.wantError, buf.String())
			}
		})
	}
}
