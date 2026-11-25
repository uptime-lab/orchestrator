package node

import (
	"sort"
	"strings"
)

// EEPROMConfigScanScript is the fixed script an EEPROM-config scan dispatches.
// A constant (never operator-supplied) so the endpoint is a narrow read of the
// actually-flashed EEPROM config, not a general run_script. rpi-eeprom-config
// prints the live config as KEY=VALUE lines (BOOT_ORDER, NET_INSTALL_*,
// HTTP_HOST, HTTP_PATH, ...).
const EEPROMConfigScanScript = `sudo rpi-eeprom-config`

// EEPROMConfigEntry is one KEY=VALUE line of the live EEPROM config.
type EEPROMConfigEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ParseEEPROMConfig turns rpi-eeprom-config output into sorted KEY=VALUE
// entries, skipping comments and blank lines. Deterministic order so the UI
// renders stably.
func ParseEEPROMConfig(stdout string) []EEPROMConfigEntry {
	var entries []EEPROMConfigEntry
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		entries = append(entries, EEPROMConfigEntry{Key: key, Value: strings.TrimSpace(value)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

// EEPROMConfigScanResult is the polled outcome of an EEPROM-config scan.
type EEPROMConfigScanResult struct {
	Status  string              `json:"status"` // "running" | "completed" | "failed"
	Detail  string              `json:"detail,omitempty"`
	Entries []EEPROMConfigEntry `json:"entries,omitempty"`
}
