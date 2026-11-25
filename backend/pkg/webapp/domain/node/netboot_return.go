package node

import (
	"fmt"
	"strings"
)

// Installing the telemetry-only agent is a ONE-WAY DOOR unless the device can
// netboot again. A Watcher node executes no commands, so the only route back to
// managing it is a recovery boot: the operator flips `boot_to_os` to netboot and
// power-cycles, and the platform serves the recovery image when the ROM asks for
// it. If the ROM never asks — because its BOOT_ORDER reaches a local disk with
// an installed OS first — nothing on the platform side can make it ask, and the
// node is a telemetry feed with no way home. With
// `PROVISIONING_BOOT_FAILSAFE_THRESHOLD` at 0 this is sharper still: there is
// no boot-loop fallback to recovery either.
//
// The checkable form of "can it come back" is the device's own BOOT_ORDER: http
// must be reached before anything that can end the sequence.

// bootOrderPassThrough are the ONLY entries a walk may cross on its way to
// http. Everything else counts as reaching the end of the road:
//
//   - the local-media modes (sd, nvme, usb…) are the important ones — after a
//     Watcher install the disk holds a bootable OS, so any of them ahead of http
//     wins every time and the device never contacts us;
//   - `restart` loops back to the first entry and `stop` halts outright, so
//     either ahead of http makes http unreachable in the same way.
//
// This is an ALLOWLIST rather than a list of blockers on purpose. A denylist
// fails OPEN — an entry nobody has heard of is walked past — and the two ways
// that happens are real: firmware gains a boot mode (the ROM's BOOT_ORDER is a
// hex nibble sequence, and `hexToBootOrderArray` in the agent silently DROPS
// digits it cannot name, so an unknown local-media mode arrives as a SHORTER
// list with the blocker deleted), or the agent's own value set is extended.
// The action being gated cannot be undone from the platform, so an entry whose
// meaning we do not know is treated as one that stops the walk.
//
// PXE and rpiboot are the two crossings: neither boots the installed OS, so a
// device that tries them falls through to http. For PXE that is a statement
// about this product's deployments rather than about the protocol — where a
// PXE/TFTP server answers on the LAN, network boot CAN succeed and http would
// never be reached.
var bootOrderPassThrough = map[string]bool{
	"network": true, // PXE, a different server than our http boot
	"rpiboot": true, // waits for a USB host; boots nothing on its own
}

// bootOrderLabels names the entries an operator is likely to see, so the
// refusal says "NVMe disk" rather than echoing a token. An entry with no label
// is reported verbatim — it still stops the walk.
var bootOrderLabels = map[string]string{
	"sd":             "SD card",
	"sd_card_detect": "SD card",
	"nvme":           "NVMe disk",
	"usb_msd":        "USB disk",
	"bcm_usb_msd":    "USB disk",
	"restart":        "restart (loops back to the first entry)",
	"stop":           "stop (halts until a power cycle)",
}

// BootOrderCanReturnToNetboot reports whether a device carrying this BOOT_ORDER
// would still ask the platform for a boot image after an OS is installed on it,
// and — when it would not — why, in words an operator can act on.
//
// It is deliberately a property of the ORDER the device reported, not of what
// the platform once wrote: the EEPROM is the thing that decides, and it can be
// reflashed locally. An empty order is NOT treated as "fine": absence of a
// report is not evidence of capability, and the action this gates cannot be
// undone from the platform.
func BootOrderCanReturnToNetboot(order []string) (ok bool, reason string) {
	if len(order) == 0 {
		return false, "the device has not reported its boot order, so the platform cannot confirm it would netboot again"
	}

	for _, raw := range order {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if entry == "http" {
			return true, ""
		}
		if bootOrderPassThrough[entry] {
			continue
		}
		label := bootOrderLabels[entry]
		if label == "" {
			label = fmt.Sprintf("%q, which the platform does not recognise", raw)
		}
		return false, fmt.Sprintf(
			"the device's boot order reaches %s before HTTP boot (%s), so after an OS is installed it would boot that instead of asking the platform for the recovery image",
			label, strings.Join(order, " → "))
	}

	return false, fmt.Sprintf(
		"the device's boot order has no HTTP boot entry (%s), so it can never ask the platform for the recovery image",
		strings.Join(order, " → "))
}

// ReportedBootOrder is the boot order from the node's CURRENT system-info
// report.
//
// `Node.BootOrder` is not "what the platform asked for" — both are written from
// the same agent message. The difference is stickiness: the column is only
// written when the reported list is NON-EMPTY, while SystemInfo is replaced
// wholesale. So the column can outlive a report whose `vcgencmd` call timed out,
// and this accessor cannot.
//
// The gate uses this one anyway, and the stickiness is the reason: a stale
// column would answer "yes, it can netboot" for a device whose EEPROM was
// re-flashed locally since. For an action the platform cannot undo, "the device
// did not say so on this connection" is the honest input — the cost is a
// refusal an operator clears by reconnecting the node.
func (n *Node) ReportedBootOrder() []string {
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.Boot != nil {
		return n.SystemInfo.Hardware.Boot.BootDevicesOrder
	}
	return nil
}
