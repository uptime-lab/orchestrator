package device_profile

import (
	"fmt"
	"strings"
	"time"
)

// Equipage is what one node (or an OU, inherited) declares about its hardware:
// which carrier profile, which factory variant, and which module sits in which
// port. Stored as the nodes.device_profile jsonb; a nil/zero equipage means
// "no profile assigned" — the default for every node, rendering no UI.
type Equipage struct {
	ProfileID string           `json:"profile_id"`
	VariantID string           `json:"variant_id,omitempty"`
	Modules   []EquippedModule `json:"modules,omitempty"`
}

// EquippedModule plugs one module into one port.
type EquippedModule struct {
	ModuleID string `json:"module_id"`
	PortID   string `json:"port_id"`
}

// IsZero reports "no profile assigned".
func (e Equipage) IsZero() bool { return e.ProfileID == "" }

// Validate checks the equipage against the catalog: every reference resolves,
// every module fits its port, no two modules collide on an exclusive function
// or an I2C address, and no module consumes a bus the board shares with a
// built-in function. Returns the first violation — this runs at SAVE time so
// a broken config.txt can never be rendered from a stored equipage.
func (e Equipage) Validate(c *Catalog) error {
	if e.IsZero() {
		if e.VariantID != "" || len(e.Modules) > 0 {
			return fmt.Errorf("%w: variant or modules without a profile", ErrUnknownProfile)
		}
		return nil
	}
	board, ok := c.Board(e.ProfileID)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownProfile, e.ProfileID)
	}
	if _, ok := board.Variant(e.VariantID); !ok {
		return fmt.Errorf("%w: %q on profile %q", ErrUnknownVariant, e.VariantID, e.ProfileID)
	}

	// claimed tracks exclusive functions and I2C addresses across modules.
	claimedFn := map[string]string{}   // function -> module id that holds it
	claimedAddr := map[string]string{} // i2c address -> module id
	for _, em := range e.Modules {
		mod, ok := c.Module(em.ModuleID)
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownModule, em.ModuleID)
		}
		port, ok := board.Port(em.PortID)
		if !ok {
			return fmt.Errorf("%w: %q on profile %q", ErrUnknownPort, em.PortID, e.ProfileID)
		}
		if missing, ok := ModuleFitsPort(mod, port); !ok {
			return fmt.Errorf("%w: %q needs %s, port %q does not carry it",
				ErrModuleDoesNotFit, mod.ID, missing, port.ID)
		}
		for _, shared := range port.SharedBuses {
			for _, need := range mod.Needs {
				if need == shared {
					return fmt.Errorf("%w: %q needs %s", ErrBusShared, mod.ID, shared)
				}
			}
		}
		for _, need := range mod.Needs {
			if shareableFunctions[need] {
				continue
			}
			if holder, taken := claimedFn[need]; taken {
				return fmt.Errorf("%w: %q and %q both need %s",
					ErrModuleConflict, holder, mod.ID, need)
			}
			claimedFn[need] = mod.ID
		}
		if addr := strings.ToLower(strings.TrimSpace(mod.I2CAddress)); addr != "" {
			if holder, taken := claimedAddr[addr]; taken {
				return fmt.Errorf("%w: %q and %q share I2C address %s",
					ErrModuleConflict, holder, mod.ID, addr)
			}
			claimedAddr[addr] = mod.ID
		}
	}
	return nil
}

// OptionKind classifies where an equipped option comes from.
type OptionKind string

const (
	OptionFixed   OptionKind = "fixed"   // soldered on every variant
	OptionVariant OptionKind = "variant" // soldered on the chosen variant
	OptionModule  OptionKind = "module"  // plugged into a port
)

// OptionStatus is the declared → applied → confirmed ladder, per option.
type OptionStatus string

const (
	// StatusDeclared: in the equipage, config not yet on the device.
	StatusDeclared OptionStatus = "declared"
	// StatusUpdatePending: applied once, but the desired render has changed
	// since (equipage edit or a registry update in a release) — a re-apply is
	// an explicit operator action, never automatic.
	StatusUpdatePending OptionStatus = "update_pending"
	// StatusApplied: the device confirmed the current render is written.
	// Final state for options with nothing to probe.
	StatusApplied OptionStatus = "applied"
	// StatusConfirmed: a scan after apply saw the hardware.
	StatusConfirmed OptionStatus = "confirmed"
	// StatusNotConfirmed: a scan after apply did NOT see the hardware — the
	// honest "declared but the board does not answer" signal. Deliberately not
	// an error: an inert overlay is harmless, and this status pointing at a
	// misdeclared variant is the feature working.
	StatusNotConfirmed OptionStatus = "not_confirmed"
	// StatusWorking: the scan saw the hardware AND found the OS actually USING
	// it for its purpose. A rung above confirmed on purpose, because the two
	// answer different questions and only the second is what an operator
	// declared the module for: an RTC can answer on I2C while the kernel takes
	// its time from somewhere else entirely, and "confirmed" would report that
	// as a success.
	//
	// Reachable only for options whose registry entry can express a usage
	// fact — today that is the RTC family. Everything else settles at
	// confirmed, which is not a lesser outcome, just a smaller claim.
	StatusWorking OptionStatus = "working"
)

// Option is one resolved row of the equipage for display and gating: a fixed
// peripheral, a variant peripheral, or an equipped module, with its ladder
// status computed from what the device has acknowledged.
type Option struct {
	Kind         OptionKind
	ID           string // peripheral or module id
	Name         string
	PortID       string // module options only
	Status       OptionStatus
	Capabilities []string
}

// ScanResult is the server-parsed outcome of the fixed scan script, stored in
// nodes.device_profile_scan. Paths is the existence dump; RTCNames are the
// registered RTC drivers (a Pi 5 / CM5 has a built-in one, so bare path
// existence cannot prove an external module); HAT carries the ID EEPROM
// fields when a spec-compliant HAT is present.
type ScanResult struct {
	Paths    []string `json:"paths,omitempty"`
	RTCNames []string `json:"rtc_names,omitempty"`
	// ClockSourceRTC is the registered name of the RTC the kernel took the
	// system clock from (sysfs hctosys=1), i.e. the one the OS actually USES.
	// Empty on a scan taken before this fact existed, and on a board where no
	// RTC is the clock source — both mean "cannot tell", never "not in use".
	ClockSourceRTC string            `json:"clock_source_rtc,omitempty"`
	HAT            map[string]string `json:"hat,omitempty"`
	At             time.Time         `json:"at"`
}

// hasAny reports whether the scan satisfies the probe: an RTCNameAny probe
// matches a registered RTC driver name, a PathsAny probe matches path
// existence. A probe declaring both is satisfied by either.
func (s *ScanResult) hasAny(p Probe) bool {
	if s == nil {
		return false
	}
	// Substring match: the sysfs name attribute is "<driver> <device>"
	// ("rtc-ds1307 1-0068", "rpi-rtc soc@107c000000:rpi_rtc" — both measured
	// on real boards), so equality would never match a bare driver token. The driver
	// half is the KERNEL's name, not the chip's: rtc-ds1307 serves the whole
	// family including the DS3231, so a probe listing only the chip it declares
	// silently never confirms.
	for _, want := range p.RTCNameAny {
		for _, got := range s.RTCNames {
			if strings.Contains(got, want) {
				return true
			}
		}
	}
	for _, want := range p.PathsAny {
		for _, got := range s.Paths {
			if got == want {
				return true
			}
		}
	}
	return false
}

// inUse reports whether the scan found the OS actually USING this option's
// hardware, not merely answering.
//
// Only an RTC-name probe can say so today: the same driver tokens that confirm
// the module are matched against the clock source the kernel chose, so the
// registry needs no second list to maintain and cannot drift from the first. A
// path probe has no equivalent fact — a device node existing says nothing about
// anyone using it — so those options never reach the working rung, which is the
// honest answer rather than a missing feature.
func (s *ScanResult) inUse(p Probe) bool {
	if s == nil || s.ClockSourceRTC == "" || len(p.RTCNameAny) == 0 {
		return false
	}
	for _, want := range p.RTCNameAny {
		if strings.Contains(s.ClockSourceRTC, want) {
			return true
		}
	}
	return false
}

// ResolveOptions flattens a VALIDATED equipage into display/gating rows.
// appliedHash is the render hash the device last acknowledged (empty = never
// applied); scan is the last parsed scan (nil = never scanned). The ladder is
// computed per option but applied/update_pending is a property of the whole
// render — one marker block carries every overlay — so all options share it.
func ResolveOptions(c *Catalog, e Equipage, appliedHash string, scan *ScanResult) []Option {
	if e.IsZero() {
		return nil
	}
	board, ok := c.Board(e.ProfileID)
	if !ok {
		return nil
	}
	base := StatusDeclared
	switch appliedHash {
	case "":
		base = StatusDeclared
	case Render(c, e).Hash:
		base = StatusApplied
	default:
		base = StatusUpdatePending
	}

	statusFor := func(p Probe) OptionStatus {
		if base != StatusApplied || p.IsZero() {
			return base
		}
		if scan == nil {
			return StatusApplied
		}
		if scan.hasAny(p) {
			if scan.inUse(p) {
				return StatusWorking
			}
			return StatusConfirmed
		}
		return StatusNotConfirmed
	}

	var opts []Option
	for _, per := range board.Fixed {
		opts = append(opts, Option{
			Kind: OptionFixed, ID: per.ID, Name: per.Name,
			Status: statusFor(per.Probe), Capabilities: per.Capabilities,
		})
	}
	if variant, ok := board.Variant(e.VariantID); ok {
		for _, per := range variant.Peripherals {
			opts = append(opts, Option{
				Kind: OptionVariant, ID: per.ID, Name: per.Name,
				Status: statusFor(per.Probe), Capabilities: per.Capabilities,
			})
		}
	}
	for _, em := range e.Modules {
		mod, ok := c.Module(em.ModuleID)
		if !ok {
			continue
		}
		opts = append(opts, Option{
			Kind: OptionModule, ID: mod.ID, Name: mod.Name, PortID: em.PortID,
			Status: statusFor(mod.Probe), Capabilities: mod.Capabilities,
		})
	}
	return opts
}

// Capabilities returns the union of capabilities the equipage grants. Gating
// reads this, so an unassigned node has none and shows none of the gated UI.
func Capabilities(c *Catalog, e Equipage) []string {
	seen := map[string]bool{}
	var caps []string
	for _, opt := range ResolveOptions(c, e, "", nil) {
		for _, cap := range opt.Capabilities {
			if !seen[cap] {
				seen[cap] = true
				caps = append(caps, cap)
			}
		}
	}
	return caps
}
