package results

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The two derived booleans are the whole point of the endpoint and false is a
// real answer for both, so neither may be omitempty; and an empty account must
// serialise an array, never null, because the UI's "nothing to show" is a
// length check.
func TestAdoptionRefusalListResultShape(t *testing.T) {
	empty, _ := json.Marshal(NewAdoptionRefusalListResult(nil))
	if !strings.Contains(string(empty), `"adoption_refusals":[]`) {
		t.Errorf("empty list must be an array: %s", empty)
	}
	if !strings.Contains(string(empty), `"live_window_seconds":300`) {
		t.Errorf("live window must be published: %s", empty)
	}
	one, _ := json.Marshal(NewAdoptionRefusalListResult([]node.AdoptionRefusal{{HardwareSerial: "1000000000000001"}}))
	for _, key := range []string{`"live":false`, `"still_bound_elsewhere":false`, `"occurrences":0`, `"hardware_serial":"1000000000000001"`} {
		if !strings.Contains(string(one), key) {
			t.Errorf("missing %s in %s", key, one)
		}
	}
	if strings.Contains(string(one), "owner") {
		t.Errorf("no owner field may ever appear: %s", one)
	}
}
