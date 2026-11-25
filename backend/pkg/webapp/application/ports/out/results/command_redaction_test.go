package results

import (
	"fmt"
	"strings"
	"testing"
)

// TestRedactCommandPayloadCoversSecretBearingFields pins that every payload key
// which can carry a resolved {{secrets.*}} value is redacted. Regression guard:
// download_file.headers and manage_service.unit_content were added after the
// original script/content/value set and were leaking bearer tokens.
func TestRedactCommandPayloadCoversSecretBearingFields(t *testing.T) {
	payload := map[string]interface{}{
		"script":       "echo {{secrets.x}}",
		"content":      "token=abc",
		"value":        "s3cret",
		"headers":      "Authorization: Bearer abc",
		"unit_content": "[Service]\nEnvironment=TOKEN=abc",
		"url":          "https://h/?t=abc",
		"path":         "/etc/app.conf", // not secret-bearing — must survive
	}
	out := RedactCommandPayload(payload)

	for _, f := range []string{"script", "content", "value", "headers", "unit_content", "url"} {
		if out[f] != redactedPlaceholder {
			t.Errorf("field %q was not redacted (got %v)", f, out[f])
		}
	}
	if out["path"] != "/etc/app.conf" {
		t.Errorf("non-secret field 'path' must not be redacted (got %v)", out["path"])
	}
	// The input must not be mutated.
	if payload["headers"] == redactedPlaceholder {
		t.Error("RedactCommandPayload must not mutate its input")
	}
}

// The whitelist above is a floor. A {{secrets.*}} reference can be authored into
// any param at all — dest_path here — and the run records where it went, so
// redaction follows the actual secret rather than a guess at where it could be.
func TestRedactCommandPayloadFollowsRecordedSecretParams(t *testing.T) {
	payload := map[string]interface{}{
		"dest_path":            "/srv/deploy/gh_pat_realtoken/artifact",
		"url":                  "https://cdn/x",
		"mode":                 "0644", // no secret went here — must survive
		SecretDerivedParamsKey: []interface{}{"dest_path"},
	}
	out := RedactCommandPayload(payload)

	if out["dest_path"] != redactedPlaceholder {
		t.Errorf("a param the run resolved a secret into must be redacted (got %v)", out["dest_path"])
	}
	if out["mode"] != "0644" {
		t.Errorf("params with no secret in them must survive (got %v)", out["mode"])
	}
	if _, present := out[SecretDerivedParamsKey]; present {
		t.Error("the bookkeeping key is internal and must never reach a caller")
	}
}

// Params recorded as secret-derived must be redacted for a caller that cannot
// read secret values even when the value itself looks harmless — the point is
// that we know a secret produced it.
func TestSecretDerivedParamsAcceptsBothShapes(t *testing.T) {
	fromEngine := map[string]interface{}{SecretDerivedParamsKey: []string{"a", "b"}}
	fromDatabase := map[string]interface{}{SecretDerivedParamsKey: []interface{}{"a", "b"}}

	for name, payload := range map[string]map[string]interface{}{"engine": fromEngine, "database": fromDatabase} {
		got := SecretDerivedParams(payload)
		if len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Errorf("%s shape did not decode: %v", name, got)
		}
	}
	if got := SecretDerivedParams(map[string]interface{}{}); got != nil {
		t.Errorf("absent key must yield nil, got %v", got)
	}
}

// A caller that MAY read secret values still must not see internal bookkeeping
// rendered as if it were a command param.
func TestPublicCommandPayloadStripsBookkeepingEvenWhenAllowed(t *testing.T) {
	payload := map[string]interface{}{
		"script":               "echo s3cret",
		SecretDerivedParamsKey: []interface{}{"script"},
	}

	allowed := PublicCommandPayload(payload, true)
	if allowed["script"] != "echo s3cret" {
		t.Errorf("a permitted caller must still see the value, got %v", allowed["script"])
	}
	if _, present := allowed[SecretDerivedParamsKey]; present {
		t.Error("bookkeeping key leaked to a permitted caller")
	}

	denied := PublicCommandPayload(payload, false)
	if denied["script"] != redactedPlaceholder {
		t.Errorf("a caller without secret.read_value must be redacted, got %v", denied["script"])
	}
	if _, present := denied[SecretDerivedParamsKey]; present {
		t.Error("bookkeeping key leaked to a denied caller")
	}
	if PublicCommandPayload(nil, true) != nil {
		t.Error("nil payload must stay nil")
	}
}

// pkg_exp/pkg_sig are a bearer capability to fetch the device's EEPROM package
// (and thereby its UPTIME_TOKEN), so they must be stripped for EVERY caller —
// including one that holds secret.read_value, who has no business impersonating a
// node. Reproduces the update_boot_order payload shape.
func TestPublicCommandPayloadStripsBearerCapabilitiesForEveryCaller(t *testing.T) {
	payload := map[string]interface{}{
		"boot_order": "0xf461",
		"pkg_exp":    "1754083200",
		"pkg_sig":    "deadbeefcafe",
	}
	for _, allowSecrets := range []bool{true, false} {
		out := PublicCommandPayload(payload, allowSecrets)
		if _, present := out["pkg_exp"]; present {
			t.Errorf("pkg_exp leaked (allowSecrets=%v): %#v", allowSecrets, out["pkg_exp"])
		}
		if _, present := out["pkg_sig"]; present {
			t.Errorf("pkg_sig leaked (allowSecrets=%v): %#v", allowSecrets, out["pkg_sig"])
		}
		if out["boot_order"] != "0xf461" {
			t.Errorf("a non-capability field must survive (allowSecrets=%v): %v", allowSecrets, out["boot_order"])
		}
	}
	// The direct redaction entrypoint must strip them too, not placeholder them.
	direct := RedactCommandPayload(payload)
	if _, present := direct["pkg_sig"]; present {
		t.Error("RedactCommandPayload left pkg_sig present")
	}
}

// A secret-bearing field is redacted whatever its TYPE. The type assertion this
// used to do skipped every non-string value silently, and the fields most likely
// not to be strings are the ones that matter: download_file.headers is a map,
// and a jsonb round-trip turns anything nested into map[string]interface{}.
func TestRedactCommandPayloadRedactsNonStringValues(t *testing.T) {
	payload := map[string]interface{}{
		"headers":              map[string]interface{}{"Authorization": "Bearer s3cret"},
		"value":                []interface{}{"s3cret"},
		"script":               42,
		"mode":                 "0644",
		SecretDerivedParamsKey: []interface{}{"env"},
		"env":                  map[string]interface{}{"TOKEN": "s3cret"},
	}
	out := RedactCommandPayload(payload)

	for _, f := range []string{"headers", "value", "script", "env"} {
		if out[f] != redactedPlaceholder {
			t.Errorf("field %q was not redacted (got %#v)", f, out[f])
		}
	}
	if out["mode"] != "0644" {
		t.Errorf("non-secret field must survive (got %v)", out["mode"])
	}
}

// The agent echoes params back in its free-text report — a rejected value, a
// tool's stderr — so the details of a command that resolved a secret can carry
// that secret in plaintext. They have to be gated on the same permission as the
// payload field they came from, or that redaction is decorative.
func TestDetailsOfASecretBearingCommandAreGatedLikeItsPayload(t *testing.T) {
	secretDerived := map[string]interface{}{
		"hostname":             "ab9-token-material",
		SecretDerivedParamsKey: []string{"hostname"},
	}
	byNature := map[string]interface{}{"script": "echo hi"}

	const leak = `invalid hostname "ab9-token-material"`

	for name, payload := range map[string]map[string]interface{}{
		"engine-resolved param": secretDerived,
		"credential by nature":  byNature,
	} {
		if got := PublicCommandDetails(leak, payload, false); got != redactedPlaceholder {
			t.Errorf("%s: details reached a caller without secret.read_value: %q", name, got)
		}
		if got := PublicCommandDetails(leak, payload, true); got != leak {
			t.Errorf("%s: a secret.read_value holder must still see the details, got %q", name, got)
		}
		if got := CommandDetailsForBroadcast(leak, payload); got != "" {
			t.Errorf("%s: details must be OMITTED from a frame that has no caller, got %q", name, got)
		}
	}

	// A command that never touched a secret keeps its triage text for everyone;
	// that is the whole reason the gate is not unconditional.
	benign := map[string]interface{}{"target_device": "/dev/nvme0n1"}
	const triage = "Restart scheduled for 0 seconds"
	if got := PublicCommandDetails(triage, benign, false); got != triage {
		t.Errorf("details of a secret-free command must survive, got %q", got)
	}
	if got := CommandDetailsForBroadcast(triage, benign); got != triage {
		t.Errorf("details of a secret-free command must still be broadcast, got %q", got)
	}
}

// CommandCarriesSecretMaterial must be asked about the STORED payload: the
// redacted copy has the engine's bookkeeping stripped, so consulting it would
// answer "no" for exactly the commands that carry the most.
func TestSecretMaterialIsDetectedOnTheStoredPayloadNotTheRedactedCopy(t *testing.T) {
	stored := map[string]interface{}{
		"dest_path":            "/srv/tenant",
		SecretDerivedParamsKey: []interface{}{"dest_path"},
	}
	if !CommandCarriesSecretMaterial(stored) {
		t.Fatal("a payload the engine recorded a secret-derived param on was not detected")
	}
	if CommandCarriesSecretMaterial(PublicCommandPayload(stored, false)) {
		t.Error("the redacted copy answered yes; it no longer carries the bookkeeping, so it must not be the source of this decision")
	}
	if CommandCarriesSecretMaterial(nil) {
		t.Error("a nil payload carries nothing")
	}
}

// A command's result carries the same bytes as a block's outputs — run_script
// stdout, read_file content — and RedactGraphState placeholders those for every
// caller without secret.read_value. The result map follows that rule, not the
// softer one details uses: details is agent prose about a failure, a result is
// the output itself.
func TestPublicCommandResultRedactsForAnUnprivilegedCaller(t *testing.T) {
	result := map[string]string{"stdout": "hunter2", "exit_code": "0"}

	out := PublicCommandResult(result, false)

	if out["stdout"] == "hunter2" {
		t.Error("stdout was returned in the clear to a caller without secret.read_value")
	}
	// The KEYS survive: an absent map and an empty one are otherwise the same
	// response, and the operator cannot tell there was output at all.
	if len(out) != 2 {
		t.Errorf("keys = %v, want both preserved", out)
	}
	if result["stdout"] != "hunter2" {
		t.Error("PublicCommandResult must not mutate its input")
	}
}

func TestPublicCommandResultPassesThroughForAPrivilegedCaller(t *testing.T) {
	result := map[string]string{"stdout": "hunter2"}

	if out := PublicCommandResult(result, true); out["stdout"] != "hunter2" {
		t.Errorf("stdout = %q, want the real value", out["stdout"])
	}
}

// apply_utility_config carries resolved {{secrets.*}}/{{node.attributes.*}}
// values nested inside each utility's params_json — a JSON STRING, which no
// key-level rule can reach into. The dispatch therefore marks the whole
// "utilities" key as secret-derived (utility.Service.ApplyToNode), and this is
// the assertion that the mark actually hides the value.
func TestRedactCommandPayloadHidesUtilitySet(t *testing.T) {
	payload := map[string]interface{}{
		"union_hash": "u1",
		"utilities": []interface{}{map[string]interface{}{
			"utility_id":  "tailscale",
			"config_hash": "h1",
			"params_json": `{"auth_key":"tskey-live-abc123"}`,
		}},
		SecretDerivedParamsKey: []string{"utilities"},
	}

	out := RedactCommandPayload(payload)
	if s := fmt.Sprintf("%v", out); strings.Contains(s, "tskey-live-abc123") {
		t.Fatalf("the resolved value survived redaction: %s", s)
	}
	if out["union_hash"] != "u1" {
		t.Fatalf("union_hash should stay readable, got %v", out["union_hash"])
	}
	if _, present := out[SecretDerivedParamsKey]; present {
		t.Fatal("internal bookkeeping must never reach a response")
	}
}

// The block-id bookkeeping is internal exactly like SecretDerivedParamsKey:
// no API caller renders it, and leaving it in would let a response diff
// reveal engine internals. Both public paths must strip it.
func TestWorkflowBlockIDNeverReachesAResponse(t *testing.T) {
	payload := map[string]interface{}{
		"hostname":         "node-01",
		WorkflowBlockIDKey: "b2",
	}
	if _, present := RedactCommandPayload(payload)[WorkflowBlockIDKey]; present {
		t.Fatal("the redacted payload carries the block-id bookkeeping")
	}
	if _, present := PublicCommandPayload(payload, true)[WorkflowBlockIDKey]; present {
		t.Fatal("the privileged payload carries the block-id bookkeeping")
	}
}

// The tracked-file mark names a user id; it is bookkeeping for the result
// settle and reaches no response on either path.
func TestTrackFileMarkNeverReachesAResponse(t *testing.T) {
	payload := map[string]interface{}{
		"path":          "/etc/hosts",
		TrackFileForKey: "00000000-0000-0000-0000-000000000001",
	}
	if _, present := RedactCommandPayload(payload)[TrackFileForKey]; present {
		t.Fatal("the redacted payload carries the tracked-file mark")
	}
	if _, present := PublicCommandPayload(payload, true)[TrackFileForKey]; present {
		t.Fatal("the privileged payload carries the tracked-file mark")
	}
}
