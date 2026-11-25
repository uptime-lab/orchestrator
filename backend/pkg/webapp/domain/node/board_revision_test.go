package node

import "testing"

// Pinned against codes read off the ROM's own output on the devices this
// support was written for, so the decoder is checked against hardware and not
// only against the documentation it came from.
func TestParseBoardRevisionRealDevices(t *testing.T) {
	tests := []struct {
		name     string
		rev      string
		wantKind BoardKind
		wantSoC  string
	}{
		{"Pi 4 Model B 8GB", "d03115", BoardPi4B, "cm4"},
		{"CM4 8GB rev 1.1", "d03141", BoardCM4, "cm4"},
		{"Pi 5 4GB", "c04170", BoardPi5, "cm5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseBoardRevision(tt.rev)
			if !ok {
				t.Fatalf("ParseBoardRevision(%q) = not ok", tt.rev)
			}
			if got.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.SoC != tt.wantSoC {
				t.Errorf("SoC = %q, want %q", got.SoC, tt.wantSoC)
			}
		})
	}
}

func TestParseBoardRevisionRejects(t *testing.T) {
	// Each must fail rather than decode to a plausible board. The SoC value
	// feeds the EEPROM signer, so a confident wrong answer means flashing the
	// wrong-silicon bootloader and recovering over USB.
	for _, rev := range []string{"", "  ", "000e", "0002", "nothex", "c04170ff"} {
		if got, ok := ParseBoardRevision(rev); ok {
			t.Errorf("ParseBoardRevision(%q) = %+v, ok; want not ok", rev, got)
		}
	}
}

// A type we have never seen must still yield the right SoC — the processor
// nibble is read directly and needs no table entry. This is what keeps a board
// released after this code was written from breaking EEPROM provisioning.
func TestParseBoardRevisionUnknownTypeKeepsSoC(t *testing.T) {
	// Type 0xfe, processor 4 (BCM2712), new-style flag set.
	got, ok := ParseBoardRevision("c04fe0")
	if !ok {
		t.Fatal("ParseBoardRevision(c04fe0) = not ok")
	}
	if got.Kind != "" {
		t.Errorf("Kind = %q, want empty for an unknown type", got.Kind)
	}
	if got.SoC != "cm5" {
		t.Errorf("SoC = %q, want cm5", got.SoC)
	}
}

func TestBoardLabelPrefersRevisionOverModel(t *testing.T) {
	// The case this exists for: in the recovery bootstrap a forced device_tree=
	// makes a Pi 5 report itself as a Compute Module 5. The revision code is
	// written by the firmware from OTP and stays correct.
	n := &Node{SystemInfo: &SystemInfo{Hardware: &HardwareInfo{CPU: &CPUInfo{
		Model:         "Raspberry Pi Compute Module 5 Rev 1.0",
		BoardRevision: "c04170",
	}}}}
	if got := n.BoardLabel(); got != string(BoardPi5) {
		t.Errorf("BoardLabel = %q, want %q", got, BoardPi5)
	}

	// Without a revision — every agent below 0.4.5 — the model string is used.
	n.SystemInfo.Hardware.CPU.BoardRevision = ""
	if got := n.BoardLabel(); got != string(BoardCM5) {
		t.Errorf("BoardLabel (no revision) = %q, want %q", got, BoardCM5)
	}
}

func TestBoardKindFromModelOrdering(t *testing.T) {
	// The longer names must be tested before their own prefixes, or a CM5 Lite
	// reads as a CM5 and a CM4S as a CM4.
	tests := map[string]BoardKind{
		"Raspberry Pi Compute Module 5 Lite Rev 1.0": BoardCM5Lite,
		"Raspberry Pi Compute Module 5 Rev 1.0":      BoardCM5,
		"Raspberry Pi Compute Module 4S Rev 1.0":     BoardCM4S,
		"Raspberry Pi Compute Module 4 Rev 1.1":      BoardCM4,
		"Raspberry Pi 500":                           BoardPi500,
		"Raspberry Pi 5 Model B Rev 1.0":             BoardPi5,
		"Raspberry Pi 400 Rev 1.0":                   BoardPi400,
		"Raspberry Pi 4 Model B Rev 1.4":             BoardPi4B,
		"Some x86 box":                               "",
	}
	for model, want := range tests {
		if got := BoardKindFromModel(model); got != want {
			t.Errorf("BoardKindFromModel(%q) = %q, want %q", model, got, want)
		}
	}
}

// The regression this predicate exists for: an SD-provisioned recovery
// bootstrap reports boot device "sd", so gating on the medium made the platform
// treat an initramfs with no groupadd and no systemd as a managed OS.
func TestIsRecoveryBootstrap(t *testing.T) {
	tests := []struct {
		name       string
		bootDevice string
		flag       bool
		want       bool
	}{
		{"SD-booted recovery, agent reports the flag", "sd", true, true},
		{"netbooted recovery, agent reports the flag", "http", true, true},
		{"netbooted recovery, agent too old for the flag", "http", false, true},
		{"installed OS on NVMe", "nvme", false, false},
		{"installed OS on SD", "sd", false, false},
		{"installed OS on USB", "usb", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &Node{SystemInfo: &SystemInfo{Hardware: &HardwareInfo{Boot: &BootInfo{
				CurrentBootDevice: tt.bootDevice,
				RecoveryBootstrap: tt.flag,
			}}}}
			if got := n.IsRecoveryBootstrap(); got != tt.want {
				t.Errorf("IsRecoveryBootstrap = %v, want %v", got, tt.want)
			}
			if got := BootInfoIsRecoveryBootstrap(n.SystemInfo.Hardware.Boot); got != tt.want {
				t.Errorf("BootInfoIsRecoveryBootstrap = %v, want %v", got, tt.want)
			}
		})
	}

	// No system info at all must not read as "in recovery": that would hold
	// every queueable command on a node the platform has simply not heard from.
	if (&Node{}).IsRecoveryBootstrap() {
		t.Error("a node with no system info must not report as recovery")
	}
	if BootInfoIsRecoveryBootstrap(nil) {
		t.Error("nil BootInfo must not report as recovery")
	}
}

func TestOverclockBoardPrefersRevision(t *testing.T) {
	// Same forced-device-tree case: without the revision this node would be
	// offered CM5 presets while being a Pi 5.
	n := &Node{SystemInfo: &SystemInfo{Hardware: &HardwareInfo{CPU: &CPUInfo{
		Model:         "Raspberry Pi Compute Module 5 Rev 1.0",
		BoardRevision: "c04170",
	}}}}
	got, ok := n.OverclockBoard()
	if !ok || got != OverclockBoardPi5 {
		t.Errorf("OverclockBoard = %q/%v, want %q/true", got, ok, OverclockBoardPi5)
	}

	// A CM5 Lite has no catalog of its own and must land on the CM5 one rather
	// than being refused.
	n.SystemInfo.Hardware.CPU.BoardRevision = "d04160" // type 0x16 is not CM5 Lite
	n.SystemInfo.Hardware.CPU.Model = "Raspberry Pi Compute Module 5 Lite Rev 1.0"
	got, ok = n.OverclockBoard()
	if !ok || got != OverclockBoardCM5 {
		t.Errorf("OverclockBoard (unknown type, CM5L model) = %q/%v, want %q/true", got, ok, OverclockBoardCM5)
	}
}
