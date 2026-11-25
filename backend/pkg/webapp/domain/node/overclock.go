package node

import (
	"errors"
	"fmt"
	"strings"
)

// Overclock presets per board, applied to /boot/firmware/config.txt via the
// existing patch_file marker-block command (agent >=0.2.16: atomic write +
// one-time .orchestrator.bak). The platform NEVER reboots the node for an
// overclock change — config.txt is only read by the firmware at boot, so the
// operator must reboot explicitly to apply (and an unstable overclock can fail
// to boot; the boot-loop failsafe serves netboot recovery in that case).
//
// The directive values below are deliberately CONSERVATIVE defaults, taken
// from the widely documented community-safe overclocks for each board. They
// favour "boots everywhere with a heatsink" over benchmark records:
//   - no force_turbo anywhere: it pins the clocks at max (disables idle and
//     thermal downclocking) and, combined with over_voltage, permanently sets
//     the warranty bit;
//   - BCM2711 (Pi 4 / CM4): balanced 1.8 GHz @ over_voltage=2, performance
//     2.0 GHz @ over_voltage=6 + gpu_freq=600 — the classic documented recipe;
//   - BCM2712 (Pi 5 / CM5): balanced 2.6 GHz with stock voltage (commonly
//     stable), performance 2.8 GHz @ over_voltage_delta=50000 (+50 mV).
// Adequate cooling is expected for the performance tier; the firmware still
// thermal-throttles at 80-85 C either way.

// OverclockBoard is the board family a preset catalog entry applies to.
type OverclockBoard string

const (
	OverclockBoardPi4 OverclockBoard = "pi4"
	OverclockBoardCM4 OverclockBoard = "cm4"
	OverclockBoardPi5 OverclockBoard = "pi5"
	OverclockBoardCM5 OverclockBoard = "cm5"
)

const (
	// OverclockConfigPath is the file the presets patch.
	OverclockConfigPath = "/boot/firmware/config.txt"
	// OverclockMarker names the patch_file managed block so overclock edits
	// never collide with other orchestrator-managed blocks (e.g. "watchdog").
	OverclockMarker = "overclock"
	// OverclockPresetStock removes all overclock directives (comment-only
	// block), returning the board to firmware-default clocks.
	OverclockPresetStock = "stock"
)

// Sentinel errors let the HTTP layer map validation failures to 400 instead of
// a blanket 500 (checked with errors.Is).
var (
	// ErrOverclockUnsupportedBoard: the node's reported CPU model is not a
	// board we ship presets for (or SystemInfo has not been reported yet).
	ErrOverclockUnsupportedBoard = errors.New("overclock presets are not available for this device model")
	// ErrOverclockUnknownPreset: the preset name is not in this board's catalog.
	ErrOverclockUnknownPreset = errors.New("unknown overclock preset")
)

// OverclockPreset is one catalog entry: a named set of config.txt directives.
type OverclockPreset struct {
	// Name is the API identifier ("stock" | "balanced" | "performance").
	Name string
	// Label is the short human-readable name for pickers.
	Label string
	// Description explains the trade-off to the operator.
	Description string
	// Directives are the raw config.txt lines; empty for stock.
	Directives []string
}

// ConfigContent renders the preset as the patch_file managed-block content.
// The stock preset renders as a comment-only block: patch_file has no
// "remove block" operation, and a comment block both documents the state and
// is ignored by the frontend's overclock detection (parseOverclock skips
// comments), so the OC badge clears.
func (p OverclockPreset) ConfigContent() string {
	lines := []string{fmt.Sprintf("# Overclock preset: %s (managed by orchestrator)", p.Name)}
	if len(p.Directives) == 0 {
		lines = append(lines, "# stock clocks - no overclock directives")
		return strings.Join(lines, "\n")
	}
	// config.txt is SECTION-SCOPED: every directive belongs to the most recent
	// conditional filter. patch_file appends this block at end-of-file, so a
	// config.txt ending in e.g. "[pi4]" or "[none]" would silently swallow the
	// overclock (applied to the wrong model, or to nothing at all). Opening with
	// [all] resets the filter so the directives always apply to this device.
	lines = append(lines, "[all]")
	lines = append(lines, p.Directives...)
	return strings.Join(lines, "\n")
}

// stockPreset is shared by every board: same semantics (remove directives),
// same rendering.
var stockPreset = OverclockPreset{
	Name:        OverclockPresetStock,
	Label:       "Stock",
	Description: "Default clock speeds. Removes all overclock settings from config.txt.",
}

// bcm2711Presets covers Pi 4 and CM4 (same SoC, same documented safe range).
var bcm2711Presets = []OverclockPreset{
	stockPreset,
	{
		Name:        "balanced",
		Label:       "Balanced",
		Description: "1.8 GHz CPU with a small voltage bump. Widely stable; passive cooling is usually enough.",
		Directives:  []string{"arm_freq=1800", "over_voltage=2"},
	},
	{
		Name:        "performance",
		Label:       "Performance",
		Description: "2.0 GHz CPU, 600 MHz GPU with a moderate voltage bump. Requires good cooling (heatsink or fan).",
		Directives:  []string{"arm_freq=2000", "over_voltage=6", "gpu_freq=600"},
	},
}

// bcm2712Presets covers Pi 5 and CM5. over_voltage_delta is the BCM2712 knob
// (microvolts), replacing the legacy over_voltage steps.
var bcm2712Presets = []OverclockPreset{
	stockPreset,
	{
		Name:        "balanced",
		Label:       "Balanced",
		Description: "2.6 GHz CPU at stock voltage. Commonly stable with the standard active cooler.",
		Directives:  []string{"arm_freq=2600"},
	},
	{
		Name:        "performance",
		Label:       "Performance",
		Description: "2.8 GHz CPU with a +50 mV bump. Requires active cooling; not every chip sustains it.",
		Directives:  []string{"arm_freq=2800", "over_voltage_delta=50000"},
	},
}

// overclockCatalog keys the preset sets by board. Pi 4/CM4 and Pi 5/CM5 share
// values today but stay separate entries so a board can diverge later (e.g.
// CM4 thermals) without an API change.
var overclockCatalog = map[OverclockBoard][]OverclockPreset{
	OverclockBoardPi4: bcm2711Presets,
	OverclockBoardCM4: bcm2711Presets,
	OverclockBoardPi5: bcm2712Presets,
	OverclockBoardCM5: bcm2712Presets,
}

// OverclockBoardForModel maps the CPU model string the agent reports (the
// /proc/cpuinfo "Model" line, e.g. "Raspberry Pi Compute Module 5 Rev 1.0")
// to a preset board. Order matters: "Compute Module 4S" must be tested before
// the plain "Compute Module 4" substring (the 4S is the same BCM2711 silicon,
// so it shares the CM4 catalog). Returns false for anything without a catalog.
func OverclockBoardForModel(model string) (OverclockBoard, bool) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "compute module 5"):
		return OverclockBoardCM5, true
	case strings.Contains(m, "raspberry pi 5"):
		return OverclockBoardPi5, true
	case strings.Contains(m, "compute module 4"): // includes CM4S (same BCM2711)
		return OverclockBoardCM4, true
	case strings.Contains(m, "raspberry pi 4"):
		return OverclockBoardPi4, true
	default:
		return "", false
	}
}

// OverclockPresetsForBoard returns the catalog for a board (nil when unknown).
func OverclockPresetsForBoard(board OverclockBoard) []OverclockPreset {
	return overclockCatalog[board]
}

// ResolveOverclockPreset returns the named preset from the board's catalog.
func ResolveOverclockPreset(board OverclockBoard, name string) (OverclockPreset, error) {
	presets, ok := overclockCatalog[board]
	if !ok {
		return OverclockPreset{}, ErrOverclockUnsupportedBoard
	}
	for _, p := range presets {
		if p.Name == name {
			return p, nil
		}
	}
	return OverclockPreset{}, fmt.Errorf("%w: %q is not available for this board", ErrOverclockUnknownPreset, name)
}

// OverclockBoard reports the preset board for this node from its reported
// CPU model, false when SystemInfo is missing or the model has no catalog.
func (n *Node) OverclockBoard() (OverclockBoard, bool) {
	if n.SystemInfo == nil || n.SystemInfo.Hardware == nil || n.SystemInfo.Hardware.CPU == nil {
		return "", false
	}
	// The revision code first: it is the only board fact that survives the
	// recovery bootstrap's forced device_tree=, under which every BCM2712 board
	// reports itself as a Compute Module 5 and would be offered CM5 presets.
	// The model string stays as an unconditional fallback for agents that send
	// no revision.
	if rev, ok := n.BoardRevision(); ok {
		if b, ok := overclockBoardForKind(rev.Kind); ok {
			return b, true
		}
	}
	return OverclockBoardForModel(n.SystemInfo.Hardware.CPU.Model)
}

// overclockBoardForKind maps a decoded board to its preset catalog. A board
// with no catalog returns false, which the caller reports as "unsupported"
// rather than substituting another board's voltages.
func overclockBoardForKind(kind BoardKind) (OverclockBoard, bool) {
	switch kind {
	case BoardCM5, BoardCM5Lite:
		return OverclockBoardCM5, true
	case BoardPi5, BoardPi500:
		return OverclockBoardPi5, true
	case BoardCM4, BoardCM4S:
		return OverclockBoardCM4, true
	case BoardPi4B, BoardPi400:
		return OverclockBoardPi4, true
	default:
		return "", false
	}
}
