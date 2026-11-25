package node

import "testing"

// Every key SetLED puts into params must survive the payload copy — the
// dispatch switch copies only keys it knows, and a dropped key reaches the
// agent as a zero value with a green command.
func TestBuildApplyLEDPayloadCopiesEveryKeySetLEDSends(t *testing.T) {
	params := map[string]interface{}{
		"config_hash":     "abc",
		"stealth_mode":    true,
		"act_led_off":     true,
		"pwr_led_off":     true,
		"digital_sources": []interface{}{"temperature", "load"},
		"digital_gpio":    float64(18),
		"stealth_gpio":    float64(21),
	}
	payload := map[string]interface{}{}
	if err := buildApplyLEDPayload(params, payload); err != nil {
		t.Fatalf("build failed: %v", err)
	}
	for key := range params {
		if _, ok := payload[key]; !ok {
			t.Errorf("param %q was dropped by the payload copy", key)
		}
	}
}

func TestBuildApplyLEDPayloadRequiresHash(t *testing.T) {
	if err := buildApplyLEDPayload(map[string]interface{}{}, map[string]interface{}{}); err == nil {
		t.Fatal("missing config_hash must refuse the dispatch")
	}
}
