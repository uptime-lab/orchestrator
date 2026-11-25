package device_profile_test

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The fits vocabulary in validate.go is a MIRROR of domain/node's BoardKind
// canon: device_profile cannot import node (node imports it), so the compiler
// cannot hold the two together — this external test package can. A new
// BoardKind constant must be added to knownBoardKinds or a profile written
// for the new board is refused by validation.
func TestKnownBoardKindsMatchNodeCanon(t *testing.T) {
	canon := []node.BoardKind{
		node.BoardPi4B, node.BoardPi400, node.BoardCM4, node.BoardCM4S,
		node.BoardPi5, node.BoardCM5, node.BoardPi500, node.BoardCM5Lite,
	}
	known := device_profile.KnownBoardKinds()
	if len(known) != len(canon) {
		t.Fatalf("knownBoardKinds has %d entries, canon has %d — sync validate.go with domain/node/board_revision.go", len(known), len(canon))
	}
	set := map[string]bool{}
	for _, k := range known {
		set[k] = true
	}
	for _, k := range canon {
		if !set[string(k)] {
			t.Fatalf("canon kind %q missing from knownBoardKinds in validate.go", k)
		}
	}
}
