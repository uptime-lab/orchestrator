package node

import "testing"

// An unknown class must render as its number, not as "Unknown": the number is
// something a support conversation can look up, a word is not.
func TestUSBClassName(t *testing.T) {
	known := map[uint32]string{
		0x03: "HID",
		0x02: "Communications",
		0xe0: "Wireless",
		0xff: "Vendor specific",
	}
	for code, want := range known {
		if got := USBClassName(code); got != want {
			t.Errorf("USBClassName(0x%02x) = %q, want %q", code, got, want)
		}
	}
	if got := USBClassName(0x42); got != "Class 0x42" {
		t.Errorf("unknown class = %q, want the raw code", got)
	}
}
