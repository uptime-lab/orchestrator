package node_command

import (
	"strings"
	"testing"
)

// A target_device reaches an agent that MOUNTS it or streams a disk image over
// it, and the platform never checked the string at all: seven dispatch sites
// read it with bare type assertions, and the only validation in the system was
// on the AGENT, reached from install_os alone — while
// apply_access_config_offline and patch_file_offline mount whatever they are
// handed.
func TestRealDevicePathsAreAccepted(t *testing.T) {
	for _, p := range []string{
		"/dev/sda",
		"/dev/nvme0n1",
		"/dev/mmcblk0",
		"/dev/disk/by-id/nvme-Samsung_SSD_980_1TB_S1234",
		"/dev/disk/by-path/pci-0000:01:00.0-nvme-1",
		"/dev/vda",
	} {
		if err := ValidateTargetDevice(p); err != nil {
			t.Errorf("%s was refused: %v", p, err)
		}
	}
}

func TestValuesThatAreNotDevicePathsAreRefused(t *testing.T) {
	for _, tc := range []struct{ value, why string }{
		{"", "empty"},
		{"sda", "not absolute"},
		{"/etc/passwd", "outside /dev"},
		{"/dev/../etc/shadow", "traversal out of /dev"},
		{"/dev/./sda", "not a clean path"},
		{"/dev//sda", "not a clean path"},
		{"/dev", "names no device"},
		{"/dev/", "names no device"},
		{"/dev/sda; rm -rf /", "shell metacharacters"},
		{"/dev/sda`id`", "command substitution"},
		{"/dev/sda $(id)", "command substitution"},
		{"/dev/sd a", "whitespace"},
		{"/dev/sda\n", "trailing newline"},
		{"/dev/sda\x00", "null byte"},
		{"/dev/" + strings.Repeat("a", 300), "absurdly long"},
	} {
		if err := ValidateTargetDevice(tc.value); err == nil {
			t.Errorf("%q was accepted (%s)", tc.value, tc.why)
		}
	}
}

// An ABSENT field is fine: most slugs take none. An EMPTY one is the documented
// "use the node's configured device", and several callers send it deliberately —
// refusing it would break install_os's own default.
func TestAnAbsentOrEmptyFieldIsAccepted(t *testing.T) {
	if err := ValidateTargetDevicePayload(map[string]interface{}{"slug": "reboot"}); err != nil {
		t.Errorf("a payload with no target_device was refused: %v", err)
	}
	if err := ValidateTargetDevicePayload(map[string]interface{}{"target_device": ""}); err != nil {
		t.Errorf("an empty target_device was refused: %v", err)
	}
}

// A non-string is refused rather than ignored: the dispatch sites cast it, and a
// silently-dropped value becomes "the node's configured device" — a different
// disk from the one the caller named.
func TestANonStringTargetDeviceIsRefused(t *testing.T) {
	if err := ValidateTargetDevicePayload(map[string]interface{}{"target_device": 42}); err == nil {
		t.Error("a numeric target_device was accepted")
	}
}
