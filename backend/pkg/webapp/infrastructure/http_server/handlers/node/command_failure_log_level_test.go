package node

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	node_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	nodeCommandDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// A refusal the route answers with a 4xx is the API working and logs at
// WARN; only a failure it answers with a 5xx logs at ERROR, which is what an
// operator is alerted on.
func TestACommandRefusalIsNotLoggedAsAnError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		level  string
	}{
		{"400 refused parameter", app_errors.OperatorRefusal(errors.New("read_file: path must be absolute")), http.StatusBadRequest, "WARN"},
		{"400 invalid hostname", nodeCommandDomain.ErrInvalidHostname, http.StatusBadRequest, "WARN"},
		{"409 offline node", &node_service.NodeUnavailableError{NodeID: uuid.New(), Slug: "reboot", State: "offline"}, http.StatusConflict, "WARN"},
		{"500 real failure", errors.New("pq: connection refused"), http.StatusInternalServerError, "ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, accountID, userID := refusingHandler(t, tc.err)
			var logs bytes.Buffer
			h.logger = slog.New(slog.NewTextHandler(&logs, nil))

			rec := postCommand(t, h, accountID, userID, "reboot")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			var line string
			for _, l := range strings.Split(logs.String(), "\n") {
				if strings.Contains(l, `msg="failed to send command"`) {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("no failure line logged: %s", logs.String())
			}
			if !strings.Contains(line, "level="+tc.level) {
				t.Errorf("logged %q, want level=%s", line, tc.level)
			}
		})
	}
}
