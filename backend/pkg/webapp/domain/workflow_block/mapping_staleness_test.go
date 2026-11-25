package workflow_block

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// The other direction from TestEveryAgentBlockAManagerCannotDispatchIsPrivileged.
//
// That test catches a block MISSING from a hardcoded authorization map, which
// fails open. These two catch an entry that is PRESENT but names nothing: a
// typo, or a slug or block type renamed on one side only. The failure is
// quieter than fail-open — the map still looks complete, the dead entry
// authorizes nothing, and the real name is unmapped — so it is the shape most
// likely to survive a review.
//
// They live here for the reason the fail-open test does: only this package sees
// the registry, the slug constants and authz at once. authz deliberately does
// not import the slug constants (a shared constant would make a rename agree
// with itself), so the check cannot live there.

func TestEveryMappedCommandSlugExists(t *testing.T) {
	for _, slug := range authz.MappedCommandSlugs() {
		if _, ok := node_command.ResolveCommandName(slug); !ok {
			t.Errorf("commandSlugActions has an entry for %q, which is not a slug node_command "+
				"defines. Either it was misspelled — in which case the real slug is unmapped and "+
				"fails closed — or it was renamed and this entry now authorizes nothing", slug)
		}
	}
}

func TestEveryPrivilegedBlockTypeExists(t *testing.T) {
	known := map[string]bool{}
	for _, def := range Registry() {
		known[def.Type] = true
	}
	for _, blockType := range authz.PrivilegedBlockTypes() {
		if !known[blockType] {
			t.Errorf("privilegedBlocks marks %q privileged, but the registry has no such block. "+
				"If it was renamed, the real block is now UNPRIVILEGED — an unprivileged author "+
				"can run it", blockType)
		}
	}
}
