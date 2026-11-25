package node_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

func TestValidateNotes(t *testing.T) {
	t.Run("empty notes are valid", func(t *testing.T) {
		if err := node.ValidateNotes(""); err != nil {
			t.Fatalf("expected empty notes to be valid, got %v", err)
		}
	})

	t.Run("notes at the limit are valid", func(t *testing.T) {
		if err := node.ValidateNotes(strings.Repeat("a", node.MaxNotesLength)); err != nil {
			t.Fatalf("expected notes of exactly MaxNotesLength to be valid, got %v", err)
		}
	})

	t.Run("notes past the limit are rejected", func(t *testing.T) {
		err := node.ValidateNotes(strings.Repeat("a", node.MaxNotesLength+1))
		if !errors.Is(err, node.ErrNotesTooLong) {
			t.Fatalf("expected ErrNotesTooLong, got %v", err)
		}
	})

	// The limit counts runes, not bytes: a Cyrillic note is two bytes per
	// character, so a byte-based cap would silently halve the limit for it.
	t.Run("multibyte notes are measured in runes", func(t *testing.T) {
		if err := node.ValidateNotes(strings.Repeat("я", node.MaxNotesLength)); err != nil {
			t.Fatalf("expected MaxNotesLength multibyte runes to be valid, got %v", err)
		}
	})
}

// The command's Validate is called by the service, not by an echo.Validator —
// none is installed — so this asserts the check the update path actually runs.
func TestNodeUpdateCommandValidate(t *testing.T) {
	tooLong := strings.Repeat("a", node.MaxNotesLength+1)
	ok := "Rack 3, port 12."

	t.Run("omitted notes pass", func(t *testing.T) {
		cmd := node_update.NodeUpdateCommand{ID: uuid.New()}
		if err := cmd.Validate(); err != nil {
			t.Fatalf("expected nil notes to validate, got %v", err)
		}
	})

	t.Run("notes within the limit pass", func(t *testing.T) {
		cmd := node_update.NodeUpdateCommand{ID: uuid.New(), Notes: &ok}
		if err := cmd.Validate(); err != nil {
			t.Fatalf("expected valid notes to pass, got %v", err)
		}
	})

	t.Run("oversized notes are rejected", func(t *testing.T) {
		cmd := node_update.NodeUpdateCommand{ID: uuid.New(), Notes: &tooLong}
		if err := cmd.Validate(); !errors.Is(err, node.ErrNotesTooLong) {
			t.Fatalf("expected ErrNotesTooLong, got %v", err)
		}
	})
}
