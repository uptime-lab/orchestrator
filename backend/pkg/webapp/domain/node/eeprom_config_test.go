package node

import "testing"

func TestParseEEPROMConfig(t *testing.T) {
	out := `[all]
BOOT_UART=0
# a comment
NET_INSTALL_ENABLED=1
HTTP_HOST=boot.orchestrator.sh

HTTP_PATH=http-boot/abc123
BOOT_ORDER=0xf241
`
	entries := ParseEEPROMConfig(out)

	if len(entries) != 5 {
		t.Fatalf("expected 5 entries (comments/blanks/section skipped), got %d: %+v", len(entries), entries)
	}
	// Sorted by key.
	if entries[0].Key != "BOOT_ORDER" {
		t.Errorf("entries must be sorted by key, first = %q", entries[0].Key)
	}
	m := map[string]string{}
	for _, e := range entries {
		m[e.Key] = e.Value
	}
	if m["HTTP_HOST"] != "boot.orchestrator.sh" {
		t.Errorf("HTTP_HOST = %q", m["HTTP_HOST"])
	}
	if m["HTTP_PATH"] != "http-boot/abc123" {
		t.Errorf("HTTP_PATH = %q", m["HTTP_PATH"])
	}
	if _, ok := m["# a comment"]; ok {
		t.Error("comment line must be skipped")
	}
}

func TestParseEEPROMConfigEmpty(t *testing.T) {
	if got := ParseEEPROMConfig(""); len(got) != 0 {
		t.Errorf("empty output must yield no entries, got %+v", got)
	}
}
