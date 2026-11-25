package node

import (
	"fmt"
	"strings"
)

// BootOption represents a boot option with metadata
type BootOption struct {
	Value       string `json:"value"`       // e.g., "sd", "nvme", "http", "usb_msd", "restart", "network", etc.
	Label       string `json:"label"`       // Human-readable label
	HexDigit    string `json:"hex_digit"`   // Hex digit for boot order (e.g., "1", "4", "6", "7", "f")
	IsDefault   bool   `json:"is_default"`  // Whether it's a default option (sd, nvme, http, restart)
	Description string `json:"description"` // Optional description
}

// GetAllBootOptions returns all available boot options for Raspberry Pi
// Based on Raspberry Pi boot order documentation: https://www.raspberrypi.com/documentation/computers/raspberry-pi.html#BOOT_ORDER
func GetAllBootOptions() []BootOption {
	return []BootOption{
		// Default options (always shown)
		{
			Value:       "sd",
			Label:       "SD Card",
			HexDigit:    "1",
			IsDefault:   true,
			Description: "SD card (or eMMC on Compute Module 4)",
		},
		{
			Value:       "nvme",
			Label:       "NVMe",
			HexDigit:    "6",
			IsDefault:   true,
			Description: "Boot from an NVMe SSD on the PCIe interface. Boards with PCIe only (CM4, CM5, Pi 5) — a Pi 4 B has none",
		},
		{
			Value:       "http",
			Label:       "HTTP",
			HexDigit:    "7",
			IsDefault:   true,
			Description: "HTTP boot over ethernet (different from PXE network boot)",
		},
		{
			Value:       "restart",
			Label:       "Restart",
			HexDigit:    "f",
			IsDefault:   true,
			Description: "Restart from the first boot-mode in the BOOT_ORDER field i.e. loop",
		},
		// Additional options
		{
			Value:       "sd_card_detect",
			Label:       "SD Card Detect",
			HexDigit:    "0",
			IsDefault:   false,
			Description: "Try SD then wait for card-detect to indicate that the card has changed. Deprecated now that 0xf (RESTART) is available",
		},
		{
			Value:       "network",
			Label:       "Network",
			HexDigit:    "2",
			IsDefault:   false,
			Description: "Network boot via PXE (different from HTTP boot)",
		},
		{
			Value:       "rpiboot",
			Label:       "RPIBOOT",
			HexDigit:    "3",
			IsDefault:   false,
			Description: "RPIBOOT - See usbboot",
		},
		{
			Value:       "usb_msd",
			Label:       "USB-MSD",
			HexDigit:    "4",
			IsDefault:   true,
			Description: "Boot from a USB disk — including a SATA SSD in a USB enclosure, which is how these boards boot SATA at all",
		},
		{
			Value:       "bcm_usb_msd",
			Label:       "BCM-USB-MSD",
			HexDigit:    "5",
			IsDefault:   false,
			Description: "USB 2.0 boot from USB Type C socket (CM4: USB type A socket on CM4IO board). Not available on Raspberry Pi 5",
		},
		{
			Value:       "stop",
			Label:       "Stop",
			HexDigit:    "e",
			IsDefault:   false,
			Description: "Stop and display error pattern. A power cycle is required to exit this state",
		},
	}
}

// GetDefaultBootOptions returns the list of default boot options
func GetDefaultBootOptions() []string {
	return []string{"sd", "nvme", "http", "restart"}
}

// GetBootOptionByValue returns a boot option by its value, or nil if not found
func GetBootOptionByValue(value string) *BootOption {
	options := GetAllBootOptions()
	for i := range options {
		if options[i].Value == value {
			return &options[i]
		}
	}
	return nil
}

// GetValidBootOptionValues returns a map of all valid boot option values for validation
func GetValidBootOptionValues() map[string]bool {
	options := GetAllBootOptions()
	valid := make(map[string]bool, len(options))
	for _, opt := range options {
		valid[opt.Value] = true
	}
	return valid
}

// GetHexDigitForBootOption returns the hex digit for a given boot option value
func GetHexDigitForBootOption(value string) (string, error) {
	option := GetBootOptionByValue(value)
	if option == nil {
		return "", fmt.Errorf("invalid boot option: %s", value)
	}
	return option.HexDigit, nil
}

// BootOrderToHex converts a boot order array to the EEPROM BOOT_ORDER hex
// string. The firmware reads BOOT_ORDER right-to-left, so the first array
// element becomes the lowest nibble: ["http","nvme","sd","restart"] -> "0xf167".
func BootOrderToHex(order []string) (string, error) {
	if len(order) == 0 {
		return "", fmt.Errorf("boot order cannot be empty")
	}
	var b strings.Builder
	b.WriteString("0x")
	for i := len(order) - 1; i >= 0; i-- {
		digit, err := GetHexDigitForBootOption(strings.ToLower(strings.TrimSpace(order[i])))
		if err != nil {
			return "", err
		}
		b.WriteString(digit)
	}
	return b.String(), nil
}

// GetBootOptionByHexDigit returns a boot option by its hex digit, or nil if not found
func GetBootOptionByHexDigit(hexDigit string) *BootOption {
	options := GetAllBootOptions()
	for i := range options {
		if options[i].HexDigit == strings.ToLower(hexDigit) {
			return &options[i]
		}
	}
	return nil
}
