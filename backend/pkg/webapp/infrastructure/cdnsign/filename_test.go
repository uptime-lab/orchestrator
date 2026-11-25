package cdnsign

import "testing"

func TestSanitizeFileNameKeepsUsableNames(t *testing.T) {
	cases := map[string]string{
		"ubuntu-24.04.img.xz":    "ubuntu-24.04.img.xz",
		"golden_node1.img.zst":   "golden_node1.img.zst",
		"My Backup 2026.img":     "My_Backup_2026.img",
		"  spaced.img  ":         "spaced.img",
		"image+variant.img":      "image+variant.img",
		"/srv/store/abc/uuid.xz": "uuid.xz",
		`C:\images\windows.img`:  "windows.img",
		"../../etc/shadow":       "shadow",
		"ünïcode-näme.img":       "ncode-nme.img",
		".hidden.img":            "hidden.img",
		"--flag-looking.img":     "flag-looking.img",
	}
	for in, want := range cases {
		if got := SanitizeFileName(in); got != want {
			t.Errorf("SanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The header-injection cases are the reason this function exists: the parameter
// it filters is NOT covered by the URL signature, so anything that survives here
// lands verbatim in a Content-Disposition header.
func TestSanitizeFileNameRejectsHeaderInjection(t *testing.T) {
	cases := []string{
		"a.img\r\nSet-Cookie: x=1",
		"a.img\nX-Injected: 1",
		`a.img"; x="y`,
		"a.img\x00.txt",
	}
	for _, in := range cases {
		got := SanitizeFileName(in)
		for _, bad := range []string{"\r", "\n", `"`, "\x00", ";", ":"} {
			if contains(got, bad) {
				t.Errorf("SanitizeFileName(%q) = %q, still carries %q", in, got, bad)
			}
		}
	}
}

func TestSanitizeFileNameReturnsEmptyWhenNothingSurvives(t *testing.T) {
	// The caller must supply its own fallback; a placeholder returned from here
	// would hide that the name it passed was unusable.
	for _, in := range []string{"", "   ", "...", "///", "€€€", "..", "-.-"} {
		if got := SanitizeFileName(in); got != "" {
			t.Errorf("SanitizeFileName(%q) = %q, want empty", in, got)
		}
	}
}

func TestSanitizeFileNameIsBounded(t *testing.T) {
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'a'
	}
	if got := SanitizeFileName(string(long)); len(got) != maxFileNameLen {
		t.Errorf("length = %d, want %d", len(got), maxFileNameLen)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
