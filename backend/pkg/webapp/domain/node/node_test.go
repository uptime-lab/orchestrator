package node

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewNodeFromMasterToken_GivesUniqueNonEmptyToken(t *testing.T) {
	accountID := uuid.New()
	masterTokenID := uuid.New()
	fp := HardwareFingerprint{MacAddress: "2c:cf:67:12:34:56", HardwareSerial: "serial-1"}

	n1 := NewNodeFromMasterToken(accountID, masterTokenID, fp)
	n2 := NewNodeFromMasterToken(accountID, masterTokenID, fp)

	// The DB column unique_token is NOT NULL with a unique index. Empty strings
	// collide, so every master-token node must get a distinct non-empty token.
	if n1.UniqueToken == "" || n2.UniqueToken == "" {
		t.Fatalf("expected non-empty unique tokens, got %q and %q", n1.UniqueToken, n2.UniqueToken)
	}
	if n1.UniqueToken == n2.UniqueToken {
		t.Fatalf("expected distinct unique tokens for two nodes, both were %q", n1.UniqueToken)
	}
	if _, err := uuid.Parse(n1.UniqueToken); err != nil {
		t.Fatalf("unique token is not a valid UUID: %v", err)
	}
}

func TestNewNodeFromMasterToken_SetsMasterTokenFields(t *testing.T) {
	accountID := uuid.New()
	masterTokenID := uuid.New()
	fp := HardwareFingerprint{MacAddress: "aa:bb:cc:dd:ee:ff", HardwareSerial: "serial-2"}

	n := NewNodeFromMasterToken(accountID, masterTokenID, fp)

	if !n.UsesMasterToken() {
		t.Error("expected node to use master token auth")
	}
	if n.MasterTokenID == nil || *n.MasterTokenID != masterTokenID {
		t.Errorf("expected master token id %v, got %v", masterTokenID, n.MasterTokenID)
	}
	if n.ApprovalStatus != ApprovalStatusPendingApproval {
		t.Errorf("expected pending approval, got %s", n.ApprovalStatus)
	}
	if n.Fingerprint == nil || n.Fingerprint.HardwareSerial != fp.HardwareSerial {
		t.Errorf("expected fingerprint serial %q to be preserved", fp.HardwareSerial)
	}
	if n.GetHardwareSerial() != fp.HardwareSerial {
		t.Errorf("expected GetHardwareSerial %q, got %q", fp.HardwareSerial, n.GetHardwareSerial())
	}
}

func TestBootOrderToHex(t *testing.T) {
	tests := []struct {
		order   []string
		want    string
		wantErr bool
	}{
		{order: []string{"http", "nvme", "sd", "restart"}, want: "0xf167"},
		{order: []string{"http", "nvme", "usb_msd", "sd", "restart"}, want: "0xf1467"},
		{order: []string{"nvme", "http", "sd", "restart"}, want: "0xf176"},
		{order: []string{"sd"}, want: "0x1"},
		{order: []string{" HTTP ", "NVMe"}, want: "0x67"},
		{order: nil, wantErr: true},
		{order: []string{"floppy"}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := BootOrderToHex(tt.order)
		if tt.wantErr {
			if err == nil {
				t.Errorf("BootOrderToHex(%v) expected error, got %q", tt.order, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("BootOrderToHex(%v) unexpected error: %v", tt.order, err)
			continue
		}
		if got != tt.want {
			t.Errorf("BootOrderToHex(%v) = %q, want %q", tt.order, got, tt.want)
		}
	}
}

// TestNetworkFirstBootOrder pins the order this platform bakes into every
// per-device EEPROM. It is asserted rather than merely encoded because the
// consequence of a silent edit is a fleet that boots the wrong disk, visible
// only on hardware: USB must stay AFTER nvme (so a board booting from NVMe is
// unaffected) and BEFORE sd (so every other board reaches its USB/SATA disk
// instead of the provisioning card the operator was told to remove).
func TestNetworkFirstBootOrder(t *testing.T) {
	want := []string{"http", "nvme", "usb_msd", "sd", "restart"}
	if len(NetworkFirstBootOrder) != len(want) {
		t.Fatalf("NetworkFirstBootOrder = %v, want %v", NetworkFirstBootOrder, want)
	}
	for i := range want {
		if NetworkFirstBootOrder[i] != want[i] {
			t.Fatalf("NetworkFirstBootOrder = %v, want %v", NetworkFirstBootOrder, want)
		}
	}

	hex, err := BootOrderToHex(NetworkFirstBootOrder)
	if err != nil {
		t.Fatalf("BootOrderToHex(default) unexpected error: %v", err)
	}
	if hex != "0xf1467" {
		t.Errorf("BootOrderToHex(default) = %q, want %q", hex, "0xf1467")
	}
}
