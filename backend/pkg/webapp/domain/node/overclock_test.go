package node

import (
	"errors"
	"strings"
	"testing"
)

func TestOverclockBoardForModel(t *testing.T) {
	tests := []struct {
		model string
		want  OverclockBoard
		ok    bool
	}{
		{"Raspberry Pi Compute Module 5 Rev 1.0", OverclockBoardCM5, true},
		{"Raspberry Pi 5 Model B Rev 1.0", OverclockBoardPi5, true},
		{"Raspberry Pi Compute Module 4 Rev 1.1", OverclockBoardCM4, true},
		// CM4S is the same BCM2711 silicon; it must land on the CM4 catalog,
		// not fall through to unsupported.
		{"Raspberry Pi Compute Module 4S Rev 1.0", OverclockBoardCM4, true},
		{"Raspberry Pi 4 Model B Rev 1.5", OverclockBoardPi4, true},
		// No catalog for older/other boards — must be refused, not guessed.
		{"Raspberry Pi 3 Model B Plus Rev 1.3", "", false},
		{"Raspberry Pi Zero 2 W Rev 1.0", "", false},
		{"Intel(R) Xeon(R) CPU", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := OverclockBoardForModel(tt.model)
		if got != tt.want || ok != tt.ok {
			t.Errorf("OverclockBoardForModel(%q) = (%q, %v), want (%q, %v)",
				tt.model, got, ok, tt.want, tt.ok)
		}
	}
}

func TestOverclockCatalog_EveryBoardHasStockBalancedPerformance(t *testing.T) {
	boards := []OverclockBoard{
		OverclockBoardPi4, OverclockBoardCM4, OverclockBoardPi5, OverclockBoardCM5,
	}
	for _, board := range boards {
		presets := OverclockPresetsForBoard(board)
		if len(presets) != 3 {
			t.Fatalf("board %s: expected 3 presets, got %d", board, len(presets))
		}
		names := map[string]OverclockPreset{}
		for _, p := range presets {
			names[p.Name] = p
		}
		for _, want := range []string{"stock", "balanced", "performance"} {
			if _, ok := names[want]; !ok {
				t.Errorf("board %s: missing preset %q", board, want)
			}
		}
		// Stock must carry no directives (it clears the overclock block);
		// the tiers must carry at least an arm_freq.
		if len(names["stock"].Directives) != 0 {
			t.Errorf("board %s: stock preset must have no directives, got %v",
				board, names["stock"].Directives)
		}
		for _, tier := range []string{"balanced", "performance"} {
			hasArmFreq := false
			for _, d := range names[tier].Directives {
				if strings.HasPrefix(d, "arm_freq=") {
					hasArmFreq = true
				}
				// force_turbo is deliberately banned from the catalog: it pins
				// max clocks and (with over_voltage) sets the warranty bit.
				if strings.HasPrefix(d, "force_turbo") {
					t.Errorf("board %s preset %s: force_turbo is not allowed in presets", board, tier)
				}
			}
			if !hasArmFreq {
				t.Errorf("board %s preset %s: expected an arm_freq directive, got %v",
					board, tier, names[tier].Directives)
			}
		}
	}
}

func TestOverclockPresetsForBoard_UnknownBoard(t *testing.T) {
	if presets := OverclockPresetsForBoard(OverclockBoard("pi3")); presets != nil {
		t.Errorf("expected nil catalog for unknown board, got %v", presets)
	}
}

func TestResolveOverclockPreset(t *testing.T) {
	p, err := ResolveOverclockPreset(OverclockBoardPi5, "performance")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "performance" {
		t.Errorf("expected preset name 'performance', got %q", p.Name)
	}

	// Unknown preset on a known board.
	if _, err := ResolveOverclockPreset(OverclockBoardPi4, "extreme"); !errors.Is(err, ErrOverclockUnknownPreset) {
		t.Errorf("expected ErrOverclockUnknownPreset, got %v", err)
	}

	// Known preset name on an unknown board still fails on the board.
	if _, err := ResolveOverclockPreset(OverclockBoard("pi3"), "balanced"); !errors.Is(err, ErrOverclockUnsupportedBoard) {
		t.Errorf("expected ErrOverclockUnsupportedBoard, got %v", err)
	}
}

func TestOverclockPreset_ConfigContent(t *testing.T) {
	p, err := ResolveOverclockPreset(OverclockBoardCM4, "performance")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content := p.ConfigContent()
	for _, want := range []string{"arm_freq=2000", "over_voltage=6", "gpu_freq=600"} {
		if !strings.Contains(content, want) {
			t.Errorf("expected content to contain %q, got:\n%s", want, content)
		}
	}
	if !strings.HasPrefix(content, "#") {
		t.Errorf("expected a leading comment header, got:\n%s", content)
	}
	// config.txt is section-scoped and this block is appended at end-of-file, so
	// it must reset the conditional filter or a trailing [pi4]/[none] section in
	// the existing file would capture (and misapply) the overclock.
	if !strings.Contains(content, "\n[all]\n") {
		t.Errorf("overclock directives must be preceded by an [all] filter reset, got:\n%s", content)
	}
	allIdx := strings.Index(content, "[all]")
	for _, d := range []string{"arm_freq=2000", "over_voltage=6", "gpu_freq=600"} {
		if strings.Index(content, d) < allIdx {
			t.Errorf("directive %q appears before the [all] reset:\n%s", d, content)
		}
	}
}

// The stock block must NOT carry an [all] line: it is comment-only precisely so
// parseOverclock ignores it and the OC badge clears.
func TestOverclockPreset_StockHasNoFilterLine(t *testing.T) {
	stock, err := ResolveOverclockPreset(OverclockBoardCM4, "stock")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stock.ConfigContent(), "[all]") {
		t.Errorf("stock block must stay comment-only, got:\n%s", stock.ConfigContent())
	}
}

func TestOverclockPreset_StockContentIsCommentOnly(t *testing.T) {
	stock, err := ResolveOverclockPreset(OverclockBoardCM5, "stock")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, line := range strings.Split(stock.ConfigContent(), "\n") {
		if line == "" {
			continue
		}
		// Every line must be a comment so (a) the firmware ignores the block
		// and (b) the frontend's parseOverclock clears the OC badge.
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			t.Errorf("stock preset content must be comment-only, got line %q", line)
		}
	}
	// And it must be non-empty: the backend's patch_file dispatch rejects
	// empty content, so an empty stock render would make revert impossible.
	if strings.TrimSpace(stock.ConfigContent()) == "" {
		t.Error("stock preset content must be non-empty (patch_file rejects empty content)")
	}
}

func TestNodeOverclockBoard(t *testing.T) {
	n := &Node{}
	if _, ok := n.OverclockBoard(); ok {
		t.Error("expected no board when SystemInfo is missing")
	}

	n.SystemInfo = &SystemInfo{Hardware: &HardwareInfo{CPU: &CPUInfo{
		Model: "Raspberry Pi 5 Model B Rev 1.0",
	}}}
	board, ok := n.OverclockBoard()
	if !ok || board != OverclockBoardPi5 {
		t.Errorf("expected (pi5, true), got (%q, %v)", board, ok)
	}
}
