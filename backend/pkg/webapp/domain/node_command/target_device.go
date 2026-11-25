package node_command

import (
	"fmt"
	"path/filepath"
	"strings"
)

// A `target_device` travels from an API caller or a playbook param to an agent
// that MOUNTS it, writes into its rootfs, or streams a disk image over it. The
// platform never validated the string at all: every dispatch site read it with a
// bare type assertion, and the only check in the system was
// validateTargetDevice on the AGENT — which asks whether the disk is safe to
// overwrite (mounted, partitioned) and is reached from install_os alone.
//
// apply_access_config_offline and patch_file_offline both mount whatever they
// are handed, and both were added after that check existed.
//
// So the shape is validated where every command is created, rather than at the
// seven places that read the field. A list of call sites is exactly what leaves
// the eighth one unguarded.

const (
	// targetDeviceKey is the payload field every device-targeting slug uses.
	targetDeviceKey = "target_device"
	// maxTargetDeviceLen bounds the string. Real names are short: /dev/nvme0n1
	// is 12, and the longest realistic form is a by-path link at well under a
	// hundred characters.
	maxTargetDeviceLen = 256
)

// ValidateTargetDevicePayload checks a command payload's target_device, when it
// carries one. An ABSENT field is fine — most slugs take none, and the ones that
// do treat empty as "the node's configured device".
func ValidateTargetDevicePayload(payload map[string]interface{}) error {
	raw, present := payload[targetDeviceKey]
	if !present {
		return nil
	}
	value, ok := raw.(string)
	if !ok {
		return fmt.Errorf("target_device must be a string")
	}
	if strings.TrimSpace(value) == "" {
		// Empty is the documented "use the node's configured device", and
		// several callers send it deliberately.
		return nil
	}
	return ValidateTargetDevice(value)
}

// ValidateTargetDevice checks that a string is shaped like a block device this
// platform may hand to an agent.
//
// It is a SHAPE check and says nothing about whether the disk exists or is safe
// to overwrite — that is the agent's job, on the device in front of it. What
// this stops is the class the agent cannot: a path that is not a device at all,
// a traversal out of /dev, or a value carrying whitespace, a null byte or shell
// metacharacters, any of which reaches a mount or a command line on the device.
func ValidateTargetDevice(value string) error {
	if len(value) > maxTargetDeviceLen {
		return fmt.Errorf("target_device is %d characters; a device path is short (max %d)", len(value), maxTargetDeviceLen)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("target_device contains a null byte")
	}
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, " \t\n\r") {
		return fmt.Errorf("target_device contains whitespace: %q", value)
	}
	if !strings.HasPrefix(value, "/dev/") {
		return fmt.Errorf("target_device must be a path under /dev (got %q)", value)
	}
	// Clean() collapses "..", "." and doubled separators. A value that changes
	// under it was trying to leave /dev, whatever it looked like.
	if filepath.Clean(value) != value {
		return fmt.Errorf("target_device is not a clean absolute path: %q", value)
	}
	if value == "/dev/" || value == "/dev" {
		return fmt.Errorf("target_device names no device")
	}
	for _, r := range value {
		if !isTargetDeviceRune(r) {
			return fmt.Errorf("target_device contains %q, which no device path uses", string(r))
		}
	}
	return nil
}

// isTargetDeviceRune allows exactly what real device paths contain.
//
// Letters, digits and '/' cover /dev/sda and /dev/nvme0n1; '-' and '_' cover
// by-id and by-label links; '.' and ':' cover by-path names such as
// /dev/disk/by-path/pci-0000:01:00.0-nvme-1, which are legitimate and would be
// refused by a stricter set. Everything else — quotes, semicolons, backticks,
// dollars, spaces — is refused, because a device path never needs them and a
// command line does.
func isTargetDeviceRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '/', r == '-', r == '_', r == '.', r == ':':
		return true
	}
	return false
}
