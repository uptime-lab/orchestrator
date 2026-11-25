package workflow_execution

import (
	"log/slog"

	"github.com/google/uuid"

	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// resolveContext carries everything a {{...}} reference can resolve against
// during a run: node facts, the run's effective secrets, and completed block
// outputs. The same resolver powers condition params, secret.upsert values,
// HTTP request fields and any command param.
type resolveContext struct {
	nodeFacts map[string]string
	secrets   map[string]string                                 // secret name -> value
	outputs   map[string]workflow_execution_node.GraphNodeState // graph node id -> state (with Outputs)
	// For reporting a {{blocks.*}} reference that resolved to nothing. Optional:
	// resolution works without them, it just says nothing.
	logger     *slog.Logger
	execNodeID uuid.UUID
}

// nodeFactsOf builds the {{node.*}} fact map from a node's current state.
func nodeFactsOf(n *node.Node) map[string]string {
	facts := map[string]string{
		// The logical node id — what the attribute and node APIs key on, so a run
		// can name the row it is operating on (e.g. registering an external CI
		// agent under the node's identity). Stable across a hardware swap, unlike
		// the serial below.
		"id":            n.ID.String(),
		"hostname":      n.Hostname,
		"name":          n.Name,
		"status":        string(n.Status),
		"online":        boolStr(n.Status == node.StatusOnline),
		"agent_version": n.AgentVersion,
	}
	if n.Fingerprint != nil {
		facts["serial"] = n.Fingerprint.HardwareSerial
	}
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil {
		hw := n.SystemInfo.Hardware
		if hw.CPU != nil {
			facts["cpu_model"] = hw.CPU.Model
			// The plain "model" fact is the most useful board discriminator we
			// have (e.g. condition on CM4 vs CM5); CPU model is the closest.
			facts["model"] = hw.CPU.Model
		}
		if hw.Boot != nil {
			facts["boot_device"] = hw.Boot.CurrentBootDevice
		}
	}
	return facts
}

// reportUnresolved logs a {{blocks.*}} reference that resolved to nothing. Best
// effort: a run with no logger wired still resolves, it just says nothing.
func (rc resolveContext) reportUnresolved(expr, why string) {
	if rc.logger == nil {
		return
	}
	rc.logger.Warn("a {{blocks.*}} reference resolved to an empty string",
		"reference", expr, "reason", why, "execution_node_id", rc.execNodeID)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// resolve substitutes every {{...}} reference in s with its value ("" when
// unknown), leaving the rest of the string intact.
func (rc resolveContext) resolve(s string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	// The grammar is the domain's, shared with the playbook export and the
	// authoring gate, so what they list is what a run resolves.
	pattern := workflow_template.ReferencePattern
	return pattern.ReplaceAllStringFunc(s, func(match string) string {
		return rc.lookup(workflow_template.ReferenceExpression(pattern.FindStringSubmatch(match)))
	})
}

func (rc resolveContext) lookup(expr string) string {
	if name, ok := workflow_template.SecretReferenceName(expr); ok {
		return rc.secrets[name]
	}
	parts := strings.Split(expr, ".")
	switch parts[0] {
	case "node":
		if len(parts) >= 2 {
			return rc.nodeFacts[strings.Join(parts[1:], ".")]
		}
	case "blocks":
		// blocks.<id>.outputs.<key>
		//
		// Reported when it resolves to nothing, unlike the two above. A missing
		// {{node.*}} fact is ordinary — optional facts depend on empty
		// substitution — but a {{blocks.*}} reference names a block the author
		// wired by hand, so an empty one means the block did not run, did not
		// produce that key, or the key is misspelt. Silently substituting ""
		// there is how an advertised-but-never-written output put an empty value
		// into a live secret with the run reporting COMPLETED.
		if len(parts) >= 4 && parts[2] == "outputs" {
			key := strings.Join(parts[3:], ".")
			if state, ok := rc.outputs[parts[1]]; ok {
				if v, present := state.Outputs[key]; present {
					return v
				}
				rc.reportUnresolved(expr, "the block produced no output with that key")
				return ""
			}
			rc.reportUnresolved(expr, "no block with that id has run in this execution")
		}
	}
	return ""
}

// resolveParams returns a copy of a block's params with every string value
// (recursively) resolved.
func (rc resolveContext) resolveParams(params map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(params))
	for k, v := range params {
		out[k] = rc.resolveValue(v)
	}
	return out
}

// SecretDerivedParams re-exports the domain's answer so engine call sites read
// naturally. The detection itself lives in domain/workflow_template because the
// authoring gate asks the same question, and two copies of the pattern would
// drift.
func SecretDerivedParams(params map[string]interface{}) []string {
	return workflow_template.SecretDerivedParams(params)
}

func (rc resolveContext) resolveValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return rc.resolve(val)
	case []interface{}:
		out := make([]interface{}, len(val))
		for i := range val {
			out[i] = rc.resolveValue(val[i])
		}
		return out
	case map[string]interface{}:
		return rc.resolveParams(val)
	default:
		return v
	}
}
