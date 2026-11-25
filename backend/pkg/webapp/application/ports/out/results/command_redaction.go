package results

import "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"

// SecretBearingPayloadFields are node-command payload keys that ALWAYS carry
// credential material, whatever the command. They must be redacted from any
// caller lacking authz.ActionSecretReadValue, on EVERY endpoint that returns a
// command payload.
//
// download_file.headers (e.g. "Authorization: Bearer {{secrets.repo_token}}"),
// manage_service.unit_content (units routinely embed credentials) and any URL
// param ({{secrets.*}} in a query string) carry resolved secrets just like
// script/content/value. password/ssh_key are the provisioning credentials
// install_os and provision_connection carry directly from OU policy — not from
// a {{secrets.*}} reference, so nothing else would ever mark them.
//
// This list is a FLOOR, not the whole coverage: a {{secrets.*}} reference can be
// authored into any param at all, so the run records the ones it resolved
// secrets into under SecretDerivedParamsKey and those are redacted too. Adding a
// field here is still worthwhile for params that are credentials by nature.
var SecretBearingPayloadFields = []string{"script", "content", "value", "headers", "unit_content", "url", "password", "ssh_key"}

// CapabilityPayloadFields are bearer capabilities baked into a command payload
// for gRPC delivery to the AGENT, and they must never appear in an API response
// to ANY caller — unlike a secret VALUE, which authz.ActionSecretReadValue is
// allowed to reveal. pkg_exp/pkg_sig are the HMAC that lets the holder fetch the
// per-serial EEPROM package unauthenticated (update_boot_order,
// eeprom_provision), and that package carries the device's UPTIME_TOKEN — so a
// caller handed the capability can download the token and open a gRPC stream AS
// the node, reading its access-config hashes, materialized secrets and file
// contents. That is a straight bypass of ActionSecretReadValue, and gating the
// capability on ActionSecretReadValue would only move the leak to RoleAdmin, who
// has no more business impersonating a device than a viewer does. The UI never
// reads these fields (they exist only for the agent's command delivery), so
// stripping them unconditionally breaks nothing.
var CapabilityPayloadFields = []string{"pkg_exp", "pkg_sig"}

// SecretDerivedParamsKey is the reserved payload key under which the workflow
// engine records which params it resolved secret material into. It is internal
// bookkeeping: RedactCommandPayload strips it from every response, so it never
// reaches an API caller.
//
// It exists because the alternative — a fixed list of "secret-bearing" params —
// is inherently incomplete: {{secrets.repo_token}} is just as valid inside
// download_file.dest_path or an install_packages spec as inside script. The
// engine knows which params it substituted into; nothing downstream
// can reconstruct that from the resolved value.
const SecretDerivedParamsKey = "_secret_derived_params"

// WorkflowBlockIDKey is the reserved payload key naming the GRAPH BLOCK a
// workflow-dispatched command executes. Internal bookkeeping like
// SecretDerivedParamsKey, and stripped from every API response the same way.
//
// It exists because the command row's link column names only the EXECUTION
// NODE, not the block: after a pass dies between dispatch and finalize, the
// stored GraphState has no idea which block an orphaned command belonged to,
// and the engine's only safe answers were "fail the run" (active orphan) or
// "look away" (terminal orphan) — the second of which dispatched the same
// device command twice. With the block id on the command, the
// engine can instead adopt the orphan back into its block and settle it
// through the ordinary progress path.
const WorkflowBlockIDKey = "_workflow_block_id"

// TrackFileForKey is the reserved payload key that marks a read_file
// dispatched by the tracked-file flow (track or refresh), naming the user who
// asked. The command-result settle reads it to record the file the moment the
// device's answer arrives, so tracking no longer depends on a client polling
// for the result. Internal bookkeeping like the keys above: it is written only
// by the tracked-file service, never copied from a caller's params, and
// stripped from every API response.
const TrackFileForKey = "_track_file_for"

// redactedPlaceholder is what every redacted field is replaced with, so the UI
// can render one consistent explanation of why a value is missing. It is the
// domain's constant because the SAVE path refuses documents that carry it
// (workflow_template.Validate) — the two must never drift apart.
const redactedPlaceholder = workflow_template.RedactedParamPlaceholder

// PublicCommandPayload prepares a command payload for an API response: redacted
// unless the caller holds authz.ActionSecretReadValue, and stripped of internal
// bookkeeping either way. Endpoints should call THIS rather than deciding for
// themselves — the redact-or-not branch is exactly where a new endpoint forgets.
func PublicCommandPayload(payload map[string]interface{}, allowSecretValues bool) map[string]interface{} {
	if !allowSecretValues {
		// RedactCommandPayload already strips CapabilityPayloadFields, so the
		// redact branch is covered there.
		return RedactCommandPayload(payload)
	}
	if payload == nil {
		return nil
	}
	out := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		if k == SecretDerivedParamsKey || k == WorkflowBlockIDKey || k == TrackFileForKey {
			continue
		}
		out[k] = v
	}
	// Bearer capabilities are stripped even for a caller holding
	// ActionSecretReadValue — see CapabilityPayloadFields.
	for _, f := range CapabilityPayloadFields {
		delete(out, f)
	}
	return out
}

// CommandCarriesSecretMaterial reports whether a stored command payload holds
// resolved credential material — either a field that is a credential by nature
// or a param the workflow engine recorded as secret-derived.
//
// It must be given the STORED payload, not the output of PublicCommandPayload:
// the redacted copy has SecretDerivedParamsKey stripped, so asking it would
// answer "no" for exactly the commands that carry the most.
func CommandCarriesSecretMaterial(payload map[string]interface{}) bool {
	if payload == nil {
		return false
	}
	if len(SecretDerivedParams(payload)) > 0 {
		return true
	}
	for _, f := range SecretBearingPayloadFields {
		if _, present := payload[f]; present {
			return true
		}
	}
	return false
}

// PublicCommandDetails prepares a command's free-text details for an API
// response, gated on the same permission as its payload.
//
// details is what the AGENT reported, and an agent echoes params back: a
// rejected value, a transport error, a tool's stderr. When the param it echoes
// was a resolved {{secrets.*}}, the plaintext lands in a column every node.read
// caller can list — while the payload field it came from is redacted two lines
// above. So the gate follows the payload.
//
// It is NOT unconditional. For a command that never touched a secret this text
// is the only thing an operator has to triage a failure with (the same reason
// RedactURLCredentialsInDetails keeps host and path), so withholding it from
// every caller below secret.read_value would cost far more than it buys.
func PublicCommandDetails(details string, payload map[string]interface{}, allowSecretValues bool) string {
	if details == "" || allowSecretValues || !CommandCarriesSecretMaterial(payload) {
		return details
	}
	return redactedPlaceholder
}

// CommandDetailsForBroadcast prepares details for a WebSocket frame, which has
// no caller to evaluate ActionSecretReadValue against — so it can only omit,
// exactly as the frame already omits the payload. Returning EMPTY rather than
// the placeholder is required, not cosmetic: clients merge pushed frames into a
// store that may already hold the real text a privileged user fetched over
// HTTP, and the field is `omitempty`, so an empty string leaves that untouched
// while a placeholder would overwrite it.
func CommandDetailsForBroadcast(details string, payload map[string]interface{}) string {
	if CommandCarriesSecretMaterial(payload) {
		return ""
	}
	return details
}

// PublicCommandResult prepares an agent-reported result map for an API
// response.
//
// These are the SAME bytes the run panel sees as block outputs — run_script
// stdout, read_file content — and RedactGraphState replaces every one of those
// with a placeholder for a caller without secret.read_value, unconditionally
// and regardless of what the block's params carried. This follows that rule
// rather than the softer one PublicCommandDetails uses, because details is
// agent prose about a failure while a result is the file or the output itself.
//
// The KEYS survive, so an operator can still see that output exists and how
// much of it — an absent map and an empty one are otherwise the same response.
func PublicCommandResult(result map[string]string, allowSecretValues bool) map[string]string {
	if len(result) == 0 || allowSecretValues {
		return result
	}
	out := make(map[string]string, len(result))
	for k := range result {
		out[k] = redactedPlaceholder
	}
	return out
}

// RedactCommandPayload returns a shallow copy of the payload with secret-bearing
// string fields replaced by a placeholder. nil-safe. This is the single source of
// truth so a new command-reading endpoint can't silently forget to redact.
func RedactCommandPayload(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return nil
	}
	out := make(map[string]interface{}, len(payload))
	for k, v := range payload {
		out[k] = v
	}
	// Redact on PRESENCE, not on being a string. The type assertion this used to
	// do was a silent no-op for every non-string value, and the fields most
	// likely not to be strings are the ones that matter: download_file.headers
	// is a map, and a JSON round-trip through jsonb turns anything nested into
	// map[string]interface{} or []interface{}. A param the engine itself
	// recorded as secret-derived was skipped on exactly the same rule.
	for _, f := range SecretBearingPayloadFields {
		if _, present := out[f]; present {
			out[f] = redactedPlaceholder
		}
	}
	for _, f := range SecretDerivedParams(payload) {
		if _, present := out[f]; present {
			out[f] = redactedPlaceholder
		}
	}
	// Bearer capabilities are removed outright, not placeholder-redacted: there is
	// no caller they should reach and no UI that renders them.
	for _, f := range CapabilityPayloadFields {
		delete(out, f)
	}
	// Internal bookkeeping, never part of the response.
	delete(out, SecretDerivedParamsKey)
	delete(out, WorkflowBlockIDKey)
	delete(out, TrackFileForKey)
	return out
}

// SecretDerivedParams reads back the param names the engine resolved secret
// material into. Tolerant of both []string and the []interface{} a JSON
// round-trip through the database produces.
func SecretDerivedParams(payload map[string]interface{}) []string {
	raw, ok := payload[SecretDerivedParamsKey]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
