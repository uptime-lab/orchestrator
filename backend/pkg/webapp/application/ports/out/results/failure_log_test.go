package results

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

type statusError int

func (e statusError) Error() string     { return "typed refusal" }
func (e statusError) HTTPStatus() int   { return int(e) }
func (e statusError) ErrorCode() string { return "typed" }

// The level follows the answer: what the API answers with a 4xx is a refusal
// (WARN), everything else is the server failing (ERROR).
func TestLogFailurePicksTheLevelFromTheAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		err       error
		wantLevel string
	}{
		"a 409 typed refusal":        {fmt.Errorf("admitting: %w", statusError(http.StatusConflict)), "WARN"},
		"a typed 5xx":                {statusError(http.StatusServiceUnavailable), "ERROR"},
		"a declared 404 sentinel":    {fmt.Errorf("target: %w", app_errors.ErrNodeNotFound), "WARN"},
		"a plain 404 sentinel":       {fmt.Errorf("get: %w", app_errors.ErrMasterTokenNotFound), "WARN"},
		"an operator refusal":        {app_errors.OperatorRefusal(errors.New("delay_seconds must be a number")), "WARN"},
		"a wrong password":           {app_errors.ErrInvalidCredentials, "WARN"},
		"a throttled sign-in":        {&app_errors.LoginThrottledError{}, "WARN"},
		"an unclassified failure":    {errors.New("connection reset by peer"), "ERROR"},
		"a wrapped unclassified one": {fmt.Errorf("listing: %w", errors.New("pool exhausted")), "ERROR"},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			LogFailure(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)), "request failed", tc.err, "id", 1)
			if !strings.Contains(buf.String(), "level="+tc.wantLevel) {
				t.Errorf("logged %q, want level %s", buf.String(), tc.wantLevel)
			}
			if !strings.Contains(buf.String(), "id=1") {
				t.Errorf("the caller's attributes were dropped: %q", buf.String())
			}
		})
	}
}
