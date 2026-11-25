package node

import (
	"strings"
	"testing"
)

func TestBootOrderCanReturnToNetboot(t *testing.T) {
	cases := []struct {
		name    string
		order   []string
		wantOK  bool
		wantHas string // substring the reason must carry, so the operator learns what to do
	}{
		{
			name:   "network-first order — http is reached first",
			order:  NetworkFirstBootOrder,
			wantOK: true,
		},
		{
			name:   "http alone is enough",
			order:  []string{"http"},
			wantOK: true,
		},
		{
			name:   "case and whitespace are the device's, not ours",
			order:  []string{" HTTP ", "nvme"},
			wantOK: true,
		},
		{
			// The shape that makes a Watcher install a one-way door: the disk
			// holds a bootable OS after the install, so nvme wins every time.
			name:    "a local disk ahead of http",
			order:   []string{"nvme", "http", "restart"},
			wantOK:  false,
			wantHas: "NVMe disk",
		},
		{
			name:    "sd ahead of http",
			order:   []string{"sd", "http"},
			wantOK:  false,
			wantHas: "SD card",
		},
		{
			name:    "usb ahead of http",
			order:   []string{"usb_msd", "http"},
			wantOK:  false,
			wantHas: "USB disk",
		},
		{
			name:    "the CM4IO USB path ahead of http",
			order:   []string{"bcm_usb_msd", "http"},
			wantOK:  false,
			wantHas: "USB disk",
		},
		{
			name:    "sd-card-detect ahead of http — it tries the SD card",
			order:   []string{"sd_card_detect", "http"},
			wantOK:  false,
			wantHas: "SD card",
		},
		{
			name:    "stop ahead of http halts until a power cycle",
			order:   []string{"stop", "http"},
			wantOK:  false,
			wantHas: "stop",
		},
		{
			// An entry nobody has heard of stops the walk. A denylist would have
			// crossed it — and the ROM's hex order is decoded by an agent that
			// silently DROPS digits it cannot name, so an unknown local-media
			// mode reaches us as a shorter list with the blocker missing.
			name:    "an unrecognised entry is not walked past",
			order:   []string{"sata_direct", "http"},
			wantOK:  false,
			wantHas: "does not recognise",
		},
		{
			// First match decides: a blocker AFTER http never runs, because the
			// device has already asked us for an image by then.
			name:   "a local disk after http still passes",
			order:  []string{"http", "nvme", "sd", "restart"},
			wantOK: true,
		},
		{
			name:    "case and whitespace are normalised on the blocker side too",
			order:   []string{" NVMe ", "http"},
			wantOK:  false,
			wantHas: "NVMe disk",
		},
		{
			// restart loops back to the first entry, so nothing after it is ever
			// reached — http included.
			name:    "restart ahead of http",
			order:   []string{"restart", "http"},
			wantOK:  false,
			wantHas: "restart",
		},
		{
			name:    "no http entry at all",
			order:   []string{"nvme", "sd"},
			wantOK:  false,
			wantHas: "NVMe disk",
		},
		{
			name:    "only local media, no http anywhere",
			order:   []string{"network", "rpiboot"},
			wantOK:  false,
			wantHas: "no HTTP boot entry",
		},
		{
			// PXE and rpiboot cannot boot the installed OS, so a device that
			// tries them still falls through to http.
			name:   "pxe ahead of http does not block",
			order:  []string{"network", "http", "nvme"},
			wantOK: true,
		},
		{
			// Absence of a report is not evidence of capability, and the action
			// this gates cannot be undone from the platform.
			name:    "an unreported order is refused, not assumed fine",
			order:   nil,
			wantOK:  false,
			wantHas: "has not reported",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := BootOrderCanReturnToNetboot(tc.order)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v (reason %q), want %v", ok, reason, tc.wantOK)
			}
			if ok && reason != "" {
				t.Errorf("a passing order carried a reason: %q", reason)
			}
			if !ok && tc.wantHas != "" && !strings.Contains(reason, tc.wantHas) {
				t.Errorf("reason %q does not mention %q", reason, tc.wantHas)
			}
		})
	}
}

// The platform's own record of a boot order is what it ASKED for; the device's
// report is what its EEPROM will do. The gate must read the latter.
func TestReportedBootOrderComesFromTheDevice(t *testing.T) {
	n := &Node{BootOrder: []string{"http", "nvme"}}
	if got := n.ReportedBootOrder(); got != nil {
		t.Errorf("a node with no system info reported %v; the stored column must not stand in", got)
	}

	n.SystemInfo = &SystemInfo{Hardware: &HardwareInfo{Boot: &BootInfo{
		BootDevicesOrder: []string{"nvme", "http"},
	}}}
	got := n.ReportedBootOrder()
	if len(got) != 2 || got[0] != "nvme" {
		t.Fatalf("ReportedBootOrder = %v, want the device's [nvme http]", got)
	}
}
