package http_server

import (
	"strings"
	"testing"
)

// The node routes spell their node parameter `:id` — 115 did and 13 said
// `:nodeId`, and nothing enforced convergence, so the split was only ever one
// copy-paste from growing. Echo binds parameter names per ROUTE, so both
// spellings resolve and a drift is invisible until someone reads the wrong
// name with c.Param and gets "" at runtime. This pins the convention where the
// router can check it: the parameter immediately after /api/v1/nodes is `:id`.
//
// A LATER parameter in the same route keeps a descriptive name on purpose —
// `/workflow-executions/:id/nodes/:nodeId/commands` cannot call both `:id`,
// and there the node is the subordinate resource.
func TestNodeRoutesSpellTheNodeParamID(t *testing.T) {
	for _, route := range registeredRoutes(t) {
		// registeredRoutes returns "METHOD /path".
		path := route[strings.Index(route, " ")+1:]
		rest, ok := strings.CutPrefix(path, "/api/v1/nodes/:")
		if !ok {
			continue
		}
		param := rest
		if i := strings.Index(rest, "/"); i >= 0 {
			param = rest[:i]
		}
		if param != "id" {
			t.Errorf("%s names its node parameter %q — the convention is :id", route, param)
		}
	}
}
