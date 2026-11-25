package utility

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

// paramRefPattern matches {{secrets.<name>}} and {{node.attributes.<key>}} in
// string params — the workflow engine's reference vocabulary, deliberately
// nothing more (no {{blocks.*}}: utilities have no run to read outputs from).
var paramRefPattern = regexp.MustCompile(`\{\{\s*(secrets|node\.attributes)\.([A-Za-z0-9_.\-]+)\s*\}\}`)

// resolveParamRefs substitutes references in the resolved set's string params
// for ONE node and recomputes the per-utility hashes over the RESOLVED values
// — so a changed attribute changes the desired hash and the ladder honestly
// shows update_pending (hashes are per-node rows, which is what makes this
// coherent). Three rules, each with a reason:
//
//   - An UNRESOLVABLE reference fails the node's apply, never substitutes
//     empty — the workflow engine's rule, restated here: a silently empty
//     value is the worst outcome.
//   - A SENSITIVE secret is refused: a param an operator reads back off the
//     device is a read oracle, and the privileged-authoring rule that would
//     make it safe does not exist for utilities yet (v1 doctrine: variables
//     and attributes only).
//   - No references anywhere → no resolver call at all, and a nil resolver
//     only fails sets that actually contain references.
func (s *Service) resolveParamRefs(ctx context.Context, accountID, nodeID uuid.UUID, set []utility.Resolved) ([]utility.Resolved, error) {
	hasRefs := false
	for _, r := range set {
		for _, v := range r.Config {
			if str, ok := v.(string); ok && strings.Contains(str, "{{") {
				hasRefs = true
				break
			}
		}
	}
	if !hasRefs {
		return set, nil
	}
	if s.secretsResolver == nil {
		return nil, fmt.Errorf("utility params reference secrets/attributes but no resolver is wired")
	}
	eff, err := s.secretsResolver.ResolveEffectiveProvisioning(ctx, accountID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("resolving effective secrets: %w", err)
	}
	secrets := map[string]struct {
		value     string
		sensitive bool
	}{}
	// Attributes carry the Sensitive flag too: a node attribute IS an
	// env-kind secret (the workflow doctrine states it explicitly), and the
	// documented use case — per-node CI agent tokens — is exactly the value
	// the sensitive refusal below exists to keep out of readable params.
	attributes := map[string]struct {
		value     string
		sensitive bool
	}{}
	for _, sec := range eff.Secrets {
		secrets[sec.Name] = struct {
			value     string
			sensitive bool
		}{sec.Value, sec.Sensitive}
		if key, ok := secret_domain.ParseNodeAttributeKey(sec.Name, nodeID); ok {
			attributes[key] = struct {
				value     string
				sensitive bool
			}{sec.Value, sec.Sensitive}
		}
	}

	out := make([]utility.Resolved, len(set))
	copy(out, set)
	for i := range out {
		var refErr error
		changed := false
		resolved := make(map[string]interface{}, len(out[i].Config))
		for k, v := range out[i].Config {
			str, ok := v.(string)
			if !ok || !strings.Contains(str, "{{") {
				resolved[k] = v
				continue
			}
			next := paramRefPattern.ReplaceAllStringFunc(str, func(match string) string {
				if refErr != nil {
					return match
				}
				groups := paramRefPattern.FindStringSubmatch(match)
				switch groups[1] {
				case "secrets":
					sec, found := secrets[groups[2]]
					if !found {
						refErr = fmt.Errorf("%s: param %q references secret %q which does not reach this node", out[i].Definition.ID, k, groups[2])
						return match
					}
					if sec.sensitive {
						refErr = fmt.Errorf("%s: param %q references sensitive secret %q; utility params accept only non-sensitive variables and attributes", out[i].Definition.ID, k, groups[2])
						return match
					}
					return sec.value
				default: // node.attributes
					attr, found := attributes[groups[2]]
					if !found {
						refErr = fmt.Errorf("%s: param %q references attribute %q which this node does not have", out[i].Definition.ID, k, groups[2])
						return match
					}
					if attr.sensitive {
						refErr = fmt.Errorf("%s: param %q references sensitive attribute %q; utility params accept only non-sensitive variables and attributes", out[i].Definition.ID, k, groups[2])
						return match
					}
					return attr.value
				}
			})
			if refErr != nil {
				return nil, refErr
			}
			if next != str {
				changed = true
			}
			resolved[k] = next
		}
		if changed {
			out[i].Config = resolved
			out[i].ConfigHash = utility.HashConfig(out[i].Definition.ID, resolved)
			// The dispatch marks the payload for redaction off this flag. It is
			// per utility and set HERE, at the only place that knows a value was
			// substituted: once the reference is replaced, a resolved secret and
			// a literal look exactly alike.
			out[i].ParamsSecretDerived = true
		}
	}
	return out, nil
}
