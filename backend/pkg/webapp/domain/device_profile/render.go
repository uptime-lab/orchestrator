package device_profile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// ConfigPath is the file the rendered block patches.
	ConfigPath = "/boot/firmware/config.txt"
	// Marker names the managed block. Distinct from "overclock" and "watchdog"
	// so the three never rewrite each other's content — and the apply travels
	// on its own slug (apply_device_profile), so the QUEUE cannot conflate
	// them either (the supersede key is slug-scoped).
	Marker = "device-profile"
)

// Rendered is the device-bound outcome of an equipage: the marker-block
// content, its hash, and the typed setup steps the agent runs after writing.
type Rendered struct {
	// Content is the config.txt managed-block body.
	Content string
	// Hash identifies THIS content. Computed at dispatch and carried in the
	// command payload; the settle hook records the payload's hash verbatim.
	// Never recomputed at settle time — the equipage may have been edited
	// while the command sat queued, and recording the then-current desired
	// hash would mark an apply the device never performed as applied.
	Hash string
	// Setup is the deduplicated, order-preserving list of setup steps from
	// every equipped option.
	Setup []SetupStep
	// ButtonGPIO is the BCM line of the board's physical button (0 = none) —
	// travels in the apply payload so the agent knows to watch it.
	ButtonGPIO int
}

// Render produces the config.txt block for an equipage. An unassigned (or
// unresolvable) equipage renders the comment-only block: patch_file-style
// managed blocks cannot be REMOVED by an apply, only emptied (the overclock
// "stock" precedent), so "no profile" must still be a renderable state whose
// hash participates in the applied/desired comparison — otherwise unassigning
// a profile would leave stale overlays loading forever with no drift signal.
func Render(c *Catalog, e Equipage) Rendered {
	header := "# Device profile (managed by orchestrator)"
	if e.IsZero() {
		return finish([]string{header, "# no profile assigned - no directives"}, nil)
	}
	board, ok := c.Board(e.ProfileID)
	if !ok {
		return finish([]string{header, "# no profile assigned - no directives"}, nil)
	}

	label := board.ID
	if e.VariantID != "" {
		label += "/" + e.VariantID
	}
	lines := []string{fmt.Sprintf("# Device profile: %s (managed by orchestrator)", label)}

	var overlays []string
	var setup []SetupStep
	buttonGPIO := 0
	collectPer := func(p Peripheral) {
		overlays = append(overlays, p.Overlays...)
		setup = append(setup, p.Setup...)
		if buttonGPIO == 0 && p.ButtonGPIO > 0 {
			buttonGPIO = p.ButtonGPIO
		}
	}
	for _, per := range board.Fixed {
		collectPer(per)
	}
	if variant, ok := board.Variant(e.VariantID); ok {
		for _, per := range variant.Peripherals {
			collectPer(per)
		}
	}
	for _, em := range e.Modules {
		if mod, ok := c.Module(em.ModuleID); ok {
			overlays = append(overlays, mod.Overlays...)
			setup = append(setup, mod.Setup...)
		}
	}

	if len(overlays) == 0 {
		lines = append(lines, "# no boot config required by this equipage")
		return finishWithButton(lines, dedupSetup(setup), buttonGPIO)
	}
	// config.txt is SECTION-SCOPED: the block is appended at end-of-file, so a
	// file ending in a conditional filter like [cm4] or [none] would silently
	// swallow the overlays — the TPM would never appear and the ladder would
	// read "not confirmed", pointing the debugging session at absent hardware
	// instead of at this line. [all] resets the filter (the overclock lesson).
	lines = append(lines, "[all]")
	lines = append(lines, dedupLines(overlays)...)
	return finishWithButton(lines, dedupSetup(setup), buttonGPIO)
}

func finish(lines []string, setup []SetupStep) Rendered {
	return finishWithButton(lines, setup, 0)
}

// finishWithButton hashes the CONTENT only: the button line is agent desired
// state, not config.txt bytes, and folding it into the hash would flip every
// existing equipage to update_pending when a profile gains a button.
func finishWithButton(lines []string, setup []SetupStep, buttonGPIO int) Rendered {
	content := strings.Join(lines, "\n")
	sum := sha256.Sum256([]byte(content))
	return Rendered{Content: content, Hash: hex.EncodeToString(sum[:]), Setup: setup, ButtonGPIO: buttonGPIO}
}

// dedupLines keeps first occurrence order: two options may both require
// dtparam=i2c_arm=on and it must appear once.
func dedupLines(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range in {
		if l = strings.TrimSpace(l); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func dedupSetup(in []SetupStep) []SetupStep {
	seen := map[string]bool{}
	var out []SetupStep
	for _, s := range in {
		if !seen[s.Kind] {
			seen[s.Kind] = true
			out = append(out, s)
		}
	}
	return out
}
