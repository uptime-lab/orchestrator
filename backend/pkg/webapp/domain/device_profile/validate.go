package device_profile

import (
	"fmt"
	"regexp"
	"strings"
)

// Catalog validation. Runs twice with the same rules: in go test against the
// embedded registry (breaks CI before a bad entry merges) and at backend boot
// via the infrastructure loader (fail-fast in the composition root, so a bad
// YAML can never serve traffic). The rules are here, in domain, because they
// ARE the schema; the loader only feeds them.

// idPattern keeps registry ids URL- and log-safe; they appear in routes,
// audit details and rendered comments.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// knownBoardKinds mirrors domain/node's BoardKind values. FitsBoard compares
// fits entries against the detected kind with exact string equality, so an
// entry outside this set never matches anything and the profile silently
// becomes unofferable for exactly the board it was written for. This package
// cannot import domain/node (node imports it), so the mirror is asserted
// equal to the canon by boardkinds_test.go instead of by the compiler.
var knownBoardKinds = map[string]bool{
	"Pi 4":     true,
	"Pi 400":   true,
	"CM4":      true,
	"CM4S":     true,
	"Pi 5":     true,
	"CM5":      true,
	"Pi 500":   true,
	"CM5 Lite": true,
}

// KnownBoardKinds returns the fits vocabulary, for the canon-sync test.
func KnownBoardKinds() []string {
	out := make([]string, 0, len(knownBoardKinds))
	for k := range knownBoardKinds {
		out = append(out, k)
	}
	return out
}

// maxBCMLine bounds GPIO declarations: the 40-pin header exposes BCM lines
// 0-27, and every GPIO the registry can name (a button, an identify LED)
// lives on that header.
const maxBCMLine = 27

// Validate checks the whole catalog. Returns the first violation with enough
// context to fix the YAML file it came from.
func (c *Catalog) Validate() error {
	boardIDs := map[string]bool{}
	for _, b := range c.Boards {
		if err := validateBoard(b); err != nil {
			return fmt.Errorf("board %q: %w", b.ID, err)
		}
		if boardIDs[b.ID] {
			return fmt.Errorf("board %q: duplicate id", b.ID)
		}
		boardIDs[b.ID] = true
	}
	moduleIDs := map[string]bool{}
	for _, m := range c.Modules {
		if err := validateModule(m); err != nil {
			return fmt.Errorf("module %q: %w", m.ID, err)
		}
		if moduleIDs[m.ID] {
			return fmt.Errorf("module %q: duplicate id", m.ID)
		}
		moduleIDs[m.ID] = true
	}
	return nil
}

func validateBoard(b BoardProfile) error {
	if !idPattern.MatchString(b.ID) {
		return fmt.Errorf("id must match %s", idPattern)
	}
	if strings.TrimSpace(b.Name) == "" {
		return fmt.Errorf("name is required")
	}
	for _, f := range b.Fits {
		if !knownBoardKinds[f] {
			return fmt.Errorf("fits %q is not a known board kind (see domain/node BoardKind values)", f)
		}
	}
	variantIDs := map[string]bool{}
	for _, v := range b.Variants {
		if !idPattern.MatchString(v.ID) {
			return fmt.Errorf("variant id %q must match %s", v.ID, idPattern)
		}
		if variantIDs[v.ID] {
			return fmt.Errorf("variant %q: duplicate id", v.ID)
		}
		variantIDs[v.ID] = true
		for _, p := range v.Peripherals {
			if err := validatePeripheral(p); err != nil {
				return fmt.Errorf("variant %q peripheral %q: %w", v.ID, p.ID, err)
			}
		}
	}
	peripheralIDs := map[string]bool{}
	for _, p := range b.Fixed {
		if err := validatePeripheral(p); err != nil {
			return fmt.Errorf("fixed peripheral %q: %w", p.ID, err)
		}
		if peripheralIDs[p.ID] {
			return fmt.Errorf("fixed peripheral %q: duplicate id", p.ID)
		}
		peripheralIDs[p.ID] = true
	}
	portIDs := map[string]bool{}
	for _, p := range b.Ports {
		if !idPattern.MatchString(p.ID) {
			return fmt.Errorf("port id %q must match %s", p.ID, idPattern)
		}
		if portIDs[p.ID] {
			return fmt.Errorf("port %q: duplicate id", p.ID)
		}
		portIDs[p.ID] = true
		if p.Pinout != PinoutRPi40 {
			return fmt.Errorf("port %q: unknown pinout %q", p.ID, p.Pinout)
		}
		if p.PinFrom < 1 || p.PinTo > 40 || p.PinFrom > p.PinTo {
			return fmt.Errorf("port %q: pin range %d..%d outside %s", p.ID, p.PinFrom, p.PinTo, p.Pinout)
		}
	}
	switch b.Identify.Backend {
	case "", "sys-led", "neopixel":
	default:
		return fmt.Errorf("identify backend %q is not known", b.Identify.Backend)
	}
	if b.Identify.GPIO < 0 || b.Identify.GPIO > maxBCMLine {
		return fmt.Errorf("identify gpio %d outside BCM lines 0..%d", b.Identify.GPIO, maxBCMLine)
	}
	if b.Identify.StealthGPIO < 0 || b.Identify.StealthGPIO > maxBCMLine {
		return fmt.Errorf("stealth gpio %d outside BCM lines 0..%d", b.Identify.StealthGPIO, maxBCMLine)
	}
	if b.Identify.DigitalLEDs < 0 || b.Identify.DigitalLEDs > 16 {
		return fmt.Errorf("digital_leds %d outside 0..16", b.Identify.DigitalLEDs)
	}
	// At most one button per board — the schema promises it and Render keeps
	// only the first ButtonGPIO it meets, so a second one would be silently
	// dropped rather than refused. The active set is Fixed plus ONE variant,
	// so each such combination is checked, not the flat union (two variants
	// each carrying the board's button is a legal shape).
	fixedButtons := countButtons(b.Fixed)
	if fixedButtons > 1 {
		return fmt.Errorf("at most one button per board (fixed peripherals declare %d)", fixedButtons)
	}
	for _, v := range b.Variants {
		if n := fixedButtons + countButtons(v.Peripherals); n > 1 {
			return fmt.Errorf("variant %q: at most one button per board (fixed + variant declare %d)", v.ID, n)
		}
	}
	return nil
}

func countButtons(ps []Peripheral) int {
	n := 0
	for _, p := range ps {
		if p.ButtonGPIO > 0 {
			n++
		}
	}
	return n
}

func validateModule(m ModuleDef) error {
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id must match %s", idPattern)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(m.Needs) == 0 {
		return fmt.Errorf("needs is required — a module that consumes nothing fits nowhere meaningfully")
	}
	for _, need := range m.Needs {
		if !functionKnown(need) {
			return fmt.Errorf("needs %q is not a known pin function", need)
		}
	}
	if m.I2CAddress != "" && !regexp.MustCompile(`^0x[0-9a-fA-F]{2}$`).MatchString(m.I2CAddress) {
		return fmt.Errorf("i2c address %q must look like 0x68", m.I2CAddress)
	}
	return validateApplyParts(m.Overlays, m.Setup, m.Probe)
}

func validatePeripheral(p Peripheral) error {
	if !idPattern.MatchString(p.ID) {
		return fmt.Errorf("id must match %s", idPattern)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name is required")
	}
	// 0 is the "no button" sentinel (BCM 0 is I2C0 SDA, never a button line).
	if p.ButtonGPIO < 0 || p.ButtonGPIO > maxBCMLine {
		return fmt.Errorf("button gpio %d outside BCM lines 0..%d", p.ButtonGPIO, maxBCMLine)
	}
	return validateApplyParts(p.Overlays, p.Setup, p.Probe)
}

// validateApplyParts guards what ends up on a device. Overlay lines are the
// narrow config.txt vocabulary — a directive or a comment, one per line, no
// section filters (the render owns sectioning via its [all] opener; a filter
// smuggled in an overlay would scope every LATER line in the block).
func validateApplyParts(overlays []string, setup []SetupStep, _ Probe) error {
	for _, o := range overlays {
		line := strings.TrimSpace(o)
		if line == "" {
			return fmt.Errorf("overlay lines must be non-empty")
		}
		if strings.ContainsAny(o, "\n\r") {
			return fmt.Errorf("overlay %q: one directive per entry", o)
		}
		if strings.HasPrefix(line, "[") {
			return fmt.Errorf("overlay %q: section filters are owned by the renderer", o)
		}
	}
	for _, s := range setup {
		if !setupKinds[s.Kind] {
			return fmt.Errorf("setup kind %q is not implemented by the agent", s.Kind)
		}
	}
	return nil
}

// functionKnown reports whether any pin of any pinout carries the tag.
func functionKnown(fn string) bool {
	for _, fns := range rpi40Pinout {
		for _, f := range fns {
			if f == fn {
				return true
			}
		}
	}
	return false
}
