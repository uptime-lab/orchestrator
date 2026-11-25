package results

// BootOrderResult represents boot order in API responses
type BootOrderResult struct {
	BootOrder []string `json:"boot_order"` // Array of boot device enums: ["nvme", "sd", "usb", "restart"]
}

// NewBootOrderResult creates a new BootOrderResult from a boot order array
func NewBootOrderResult(bootOrder []string) *BootOrderResult {
	if bootOrder == nil {
		bootOrder = []string{}
	}
	return &BootOrderResult{
		BootOrder: bootOrder,
	}
}
