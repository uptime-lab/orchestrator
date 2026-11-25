package node_update

import (
	"errors"
	"net/http"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// A supplied name must name the node; an omitted one leaves it unchanged.
// An empty rename used to answer 200 and leave a row every list renders as a
// blank line.
func TestNodeUpdateRefusesAnEmptyName(t *testing.T) {
	for _, name := range []string{"", "   "} {
		n := name
		err := NodeUpdateCommand{Name: &n}.Validate()
		var refusal *app_errors.RequestRefusalError
		if !errors.As(err, &refusal) || refusal.HTTPStatus() != http.StatusBadRequest {
			t.Errorf("name %q: err = %v, want a typed 400", name, err)
		}
	}
	kept := "node-a"
	for _, cmd := range []NodeUpdateCommand{{}, {Name: &kept}} {
		if err := cmd.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", cmd, err)
		}
	}
}
