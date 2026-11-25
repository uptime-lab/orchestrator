package node_ssh_adopt

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_ssh_preflight"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
)

// On a server where SSH adoption is off, the adoption answers what the
// preflight answers: a 400 with the same text and a code. The adoption used
// to answer 500 for the same refusal.
func TestSSHAdoptDisabledIsTheSameRefusalOnBothRoutes(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.Enabled = false })

	_, adoptErr := h.svc.StartAdoption(context.Background(), h.adoptCmd())
	_, preflightErr := h.svc.Preflight(context.Background(), node_ssh_preflight.SSHPreflightCommand{AuthContext: h.actor})

	for name, err := range map[string]error{"adopt": adoptErr, "preflight": preflightErr} {
		var facing results.OperatorFacingError
		if !errors.As(err, &facing) || facing.HTTPStatus() != http.StatusBadRequest || facing.ErrorCode() != "ssh_adopt_disabled" {
			t.Errorf("%s: err = %v, want a typed 400 ssh_adopt_disabled", name, err)
			continue
		}
		if facing.Error() != "SSH adoption is not enabled on this server" {
			t.Errorf("%s: message = %q", name, facing.Error())
		}
	}
}
