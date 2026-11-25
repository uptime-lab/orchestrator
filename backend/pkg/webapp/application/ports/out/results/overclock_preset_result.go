package results

import (
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// OverclockPresetResult represents one overclock preset in API responses.
type OverclockPresetResult struct {
	Name        string   `json:"name"`        // API identifier: "stock" | "balanced" | "performance"
	Label       string   `json:"label"`       // Short human-readable name
	Description string   `json:"description"` // Trade-off explanation for the operator
	Directives  []string `json:"directives"`  // config.txt lines the preset writes (empty for stock)
}

// OverclockPresetsResult is the catalog applicable to one node's board.
type OverclockPresetsResult struct {
	// Board is the detected board family ("pi4"/"cm4"/"pi5"/"cm5"), or "" when
	// the node's model has no preset catalog (presets is then empty).
	Board   string                  `json:"board"`
	Presets []OverclockPresetResult `json:"presets"`
}

// OverclockApplyResult confirms a dispatched preset. The platform never
// reboots the node itself; RebootRequired is always true because config.txt is
// only read by the firmware at boot.
type OverclockApplyResult struct {
	Board          string `json:"board"`
	Preset         string `json:"preset"`
	RebootRequired bool   `json:"reboot_required"`
}

// NewOverclockPresetsResult converts domain presets to the API shape.
func NewOverclockPresetsResult(board nodeDomain.OverclockBoard, presets []nodeDomain.OverclockPreset) *OverclockPresetsResult {
	out := &OverclockPresetsResult{
		Board:   string(board),
		Presets: make([]OverclockPresetResult, 0, len(presets)),
	}
	for _, p := range presets {
		directives := p.Directives
		if directives == nil {
			directives = []string{}
		}
		out.Presets = append(out.Presets, OverclockPresetResult{
			Name:        p.Name,
			Label:       p.Label,
			Description: p.Description,
			Directives:  directives,
		})
	}
	return out
}
