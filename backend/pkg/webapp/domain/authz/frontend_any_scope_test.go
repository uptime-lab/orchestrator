package authz

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// canPath is the frontend module holding the copy of anyScopeReadActions.
var canPath = filepath.Join("..", "..", "..", "..", "..",
	"frontend", "src", "entities", "permissions", "can.ts")

var frontendAnyScopeBlock = regexp.MustCompile(`(?s)ANY_SCOPE_READS = new Set\(\[(.*?)\]\)`)
var frontendActionString = regexp.MustCompile(`'([^']+)'`)

// The frontend keeps its own copy of this set, and it has to: the permission map
// it receives is a per-action scope, not a rule set, so there is nothing on that
// side to derive the rule from.
//
// A copy that can drift silently fails in both directions and neither is
// visible. Missing an action there hides an affordance the server would have
// allowed, which an operator reads as a permission they do not have. Adding one
// there that is not here puts a button in front of a 403.
//
// The check lives HERE rather than in vitest because it has to read Go source,
// and the browser tsconfig deliberately does not expose Node's `fs` — a
// component that could import it is a worse problem than this one. It caught a
// real drift on its first run: the frontend list said `ssh_key.read` while the
// action is `sshkey.read`, so that quarter of the fix did nothing.
func TestFrontendAnyScopeReadsMatch(t *testing.T) {
	raw, err := os.ReadFile(canPath)
	if err != nil {
		if os.IsNotExist(err) {
			// The docker module test builds from backend/ alone, so the
			// frontend tree is structurally absent there. The guard is NOT
			// optional for it: the static-checks CI step runs this test from
			// the full checkout and fails the run if it SKIPS.
			t.Skipf("frontend tree not present in this build context (%s); enforced by static-checks", canPath)
		}
		t.Fatalf("reading %s: %v — if the module moved, this test moves with it rather than being deleted", canPath, err)
	}
	block := frontendAnyScopeBlock.FindSubmatch(raw)
	if block == nil {
		t.Fatalf("ANY_SCOPE_READS not found in %s; the copy still exists somewhere and is now unchecked", canPath)
	}

	var frontend []string
	for _, m := range frontendActionString.FindAllSubmatch(block[1], -1) {
		frontend = append(frontend, string(m[1]))
	}

	var backend []string
	for action := range anyScopeReadActions {
		backend = append(backend, string(action))
	}
	sort.Strings(frontend)
	sort.Strings(backend)

	if strings.Join(frontend, ",") != strings.Join(backend, ",") {
		t.Errorf("the frontend's ANY_SCOPE_READS is %v, the backend's anyScopeReadActions is %v.\n"+
			"Missing there hides an affordance the server allows; extra there puts a button in front of a 403.",
			frontend, backend)
	}
}
