package workflow_template

import "strings"

// RedactedParamPlaceholder is the literal the API substitutes for a free-text
// parameter value when the caller lacks secret.read_value — the SAME literal
// the run views substitute for resolved secret material, so the UI renders one
// consistent explanation everywhere.
//
// It lives in the domain because the SAVE path must refuse it: a caller whose
// read was redacted would otherwise round-trip the placeholder back through
// the full-document PUT and silently overwrite a script, a header or a url
// with the placeholder text. That refusal is Validate's job, so it covers
// create, update and import in one place.
const RedactedParamPlaceholder = "[redacted — requires secret.read_value]"

// ContainsRedactionPlaceholder reports whether any param value in the graph
// still carries the placeholder. Values are walked recursively: a placeholder
// inside a list or a nested map is the same round-trip hazard as a top-level
// one.
func (g *WorkflowGraph) ContainsRedactionPlaceholder() bool {
	if g == nil {
		return false
	}
	for _, node := range g.Nodes {
		if valueCarriesPlaceholder(node.Params) {
			return true
		}
	}
	return false
}

// StepsContainRedactionPlaceholder is the legacy-steps twin: a graphless
// template's payloads are the document, and they redact and round-trip the
// same way.
func StepsContainRedactionPlaceholder(steps []WorkflowStep) bool {
	for _, step := range steps {
		if valueCarriesPlaceholder(step.Command.Payload) {
			return true
		}
	}
	return false
}

func valueCarriesPlaceholder(v interface{}) bool {
	switch tv := v.(type) {
	case string:
		return strings.Contains(tv, RedactedParamPlaceholder)
	case map[string]interface{}:
		for _, inner := range tv {
			if valueCarriesPlaceholder(inner) {
				return true
			}
		}
	case []interface{}:
		for _, inner := range tv {
			if valueCarriesPlaceholder(inner) {
				return true
			}
		}
	}
	return false
}
