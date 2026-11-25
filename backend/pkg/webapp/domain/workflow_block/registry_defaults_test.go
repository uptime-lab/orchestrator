package workflow_block

import "testing"

// Registry() must hand out a copy nobody can use to rewrite the catalog. A
// shallow copy shares the Params backing array, so one caller decorating a spec
// changes what every account sees, for the life of the process.
func TestRegistryHandsOutAnIsolatedCopy(t *testing.T) {
	first := Registry()
	var idx int
	for i, def := range first {
		if len(def.Params) > 0 {
			idx = i
			break
		}
	}
	original := first[idx].Params[0]

	first[idx].Params[0].Label = "mutated"
	first[idx].Params[0].Required = !original.Required
	if len(first[idx].Outputs) > 0 {
		first[idx].Outputs[0].Name = "mutated"
	}

	second := Registry()
	if second[idx].Params[0].Label != original.Label {
		t.Errorf("param label = %q after a caller mutated its copy, want %q",
			second[idx].Params[0].Label, original.Label)
	}
	if second[idx].Params[0].Required != original.Required {
		t.Error("param Required leaked through the copy")
	}
	if len(second[idx].Outputs) > 0 && second[idx].Outputs[0].Name == "mutated" {
		t.Error("output ports leaked through the copy")
	}
}

func TestApplyDefaultsFillsOnlyWhatTheCallerOmitted(t *testing.T) {
	def, ok := FindByType(TypeHTTPRequest)
	if !ok {
		t.Fatal("http_request block missing from the registry")
	}

	filled := def.ApplyDefaults(map[string]interface{}{"url": "https://example.test"})
	if filled["method"] != "GET" {
		t.Errorf("method = %v, want the declared default GET", filled["method"])
	}
	if filled["timeout_seconds"] != 30 {
		t.Errorf("timeout_seconds = %v, want the declared default 30", filled["timeout_seconds"])
	}
	if filled["url"] != "https://example.test" {
		t.Errorf("url = %v, want the supplied value untouched", filled["url"])
	}

	supplied := def.ApplyDefaults(map[string]interface{}{"method": "POST", "timeout_seconds": 5})
	if supplied["method"] != "POST" || supplied["timeout_seconds"] != 5 {
		t.Errorf("supplied values were overwritten: %+v", supplied)
	}
}

// A zero and a false are VALUES, not omissions. An operator who types 0 into a
// delay, or unticks a box whose default is true, must get what they asked for —
// the alternative is the platform quietly doing something else on the device.
func TestApplyDefaultsDoesNotOverrideAZeroOrAFalse(t *testing.T) {
	def, ok := FindBySlug("install_packages")
	if !ok {
		t.Skip("install_packages not in the registry")
	}
	var hasUpdateDefault bool
	for _, p := range def.Params {
		if p.Name == "update" && p.Default == true {
			hasUpdateDefault = true
		}
	}
	if !hasUpdateDefault {
		t.Skip("install_packages.update no longer defaults to true")
	}

	out := def.ApplyDefaults(map[string]interface{}{"update": false})
	if out["update"] != false {
		t.Errorf("update = %v, want the explicit false preserved", out["update"])
	}
}

// The caller's map must not be modified in place: the engine applies defaults
// to a block read out of the pinned template snapshot, which several passes
// share.
func TestApplyDefaultsDoesNotMutateTheInput(t *testing.T) {
	def, _ := FindByType(TypeHTTPRequest)
	in := map[string]interface{}{"url": "https://example.test"}

	def.ApplyDefaults(in)

	if _, present := in["method"]; present {
		t.Error("ApplyDefaults wrote into the caller's map")
	}
}

// The engine no longer carries its own copy of these values, so the registry
// declaring them IS the behaviour. Removing one would silently change what a
// running playbook does: a wait of 0 seconds, an empty HTTP method.
func TestTheDefaultsTheEngineReliesOnAreDeclared(t *testing.T) {
	for _, want := range []struct {
		blockType string
		param     string
		value     interface{}
	}{
		{TypeWait, "seconds", 30},
		{TypeHTTPRequest, "method", "GET"},
		{TypeHTTPRequest, "timeout_seconds", 30},
		{TypeSecretUpsert, "kind", "opaque"},
		{TypeSecretUpsert, "sensitive", true},
	} {
		def, ok := FindByType(want.blockType)
		if !ok {
			t.Errorf("block %s missing from the registry", want.blockType)
			continue
		}
		found := false
		for _, p := range def.Params {
			if p.Name != want.param {
				continue
			}
			found = true
			if p.Default != want.value {
				t.Errorf("%s.%s default = %v, want %v — the engine reads this and no longer has a fallback",
					want.blockType, want.param, p.Default, want.value)
			}
		}
		if !found {
			t.Errorf("%s has no param %q", want.blockType, want.param)
		}
	}
}
