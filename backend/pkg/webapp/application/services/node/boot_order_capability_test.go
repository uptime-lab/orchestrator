package node

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// update_boot_order without the per-serial capability is refused rather than
// dispatched. The agent would otherwise fall back to a SHARED EEPROM image,
// which overwrites this device's embedded HTTP_PATH and adoption token — a green
// run that costs the node its ability to netboot at all.
//
// The capability is minted only after eeprom_provision has baked and signed an
// image for this serial, so requiring it IS the "per-serial bake is mandatory"
// rule. Both halves must be present: a signature with no expiry is not a
// capability the CDN would accept either.
func TestRequireEEPROMCapability(t *testing.T) {
	nodeID := uuid.New()
	cases := []struct {
		name        string
		exp, sig    string
		wantRefusal bool
	}{
		{"both present", "1900000000", "deadbeef", false},
		{"nothing at all", "", "", true},
		{"signature without expiry", "", "deadbeef", true},
		{"expiry without signature", "1900000000", "", true},
		{"blank strings are not a capability", "   ", "  ", true},
	}
	for _, tc := range cases {
		err := requireEEPROMCapability(nodeID, tc.exp, tc.sig)
		if tc.wantRefusal {
			if err == nil {
				t.Errorf("%s: expected a refusal", tc.name)
				continue
			}
			if !strings.Contains(err.Error(), "provision-eeprom") {
				t.Errorf("%s: error = %q, want it to name the route that does this properly", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected refusal: %v", tc.name, err)
		}
	}
}
