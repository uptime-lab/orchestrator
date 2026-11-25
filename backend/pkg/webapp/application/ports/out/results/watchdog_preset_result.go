package results

import (
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// WatchdogPresetResult is one preset in the catalog response. It carries the
// operator wording alongside the configuration so a client renders the picker
// and its confirmation prompt without holding a second copy of either.
type WatchdogPresetResult struct {
	Name        string `json:"name"`  // "agent-health" | "os-hang" | "network" | "full"
	Label       string `json:"label"` // Short human-readable name
	Description string `json:"description"`
	// Consequence is what the device will do once armed — the sentence a
	// confirmation must show. For the network preset it contains the literal
	// placeholder {{target}}, which the client replaces with the address the
	// operator named.
	Consequence string `json:"consequence"`
	// RequiresPingTarget marks a preset the client must collect an address for
	// before arming; the config below then has no ping targets.
	RequiresPingTarget bool                      `json:"requires_ping_target"`
	Config             nodeDomain.WatchdogConfig `json:"config"`
}

// WatchdogPresetsResult is the preset catalog. Presets are node-independent
// today; the endpoint is node-scoped for authorization, matching the overclock
// catalog.
type WatchdogPresetsResult struct {
	Presets []WatchdogPresetResult `json:"presets"`
}

// NewWatchdogPresetsResult converts the domain catalog to the API shape.
func NewWatchdogPresetsResult(presets []nodeDomain.WatchdogPresetInfo) *WatchdogPresetsResult {
	out := &WatchdogPresetsResult{Presets: make([]WatchdogPresetResult, 0, len(presets))}
	for _, p := range presets {
		out.Presets = append(out.Presets, WatchdogPresetResult{
			Name:               p.Name,
			Label:              p.Label,
			Description:        p.Description,
			Consequence:        p.Consequence,
			RequiresPingTarget: p.RequiresPingTarget,
			Config:             p.Config,
		})
	}
	return out
}
