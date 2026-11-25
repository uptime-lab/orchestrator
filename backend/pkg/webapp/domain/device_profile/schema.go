// Package device_profile models carrier boards and attachable hardware modules.
//
// The compute module (CM4, Pi 4, ...) is reliably DETECTED from the board
// revision code (domain/node.ParseBoardRevision). The carrier board it sits on
// is not detectable at all — and a peripheral like a TPM or an I2C RTC is
// invisible to the kernel until its device-tree overlay is applied — so the
// model here is declaration-first: the operator declares a profile and an
// equipage, the platform applies the required config.txt overlays, and a scan
// afterwards only CONFIRMS what was declared. Detection never chooses.
//
// The registry content (which boards and modules exist) is data, not code: it
// is loaded from embedded YAML by infrastructure/device_profile_registry and
// validated against the rules in this package. This package holds the typed
// schema, the pinout model, compatibility computation, rendering and the
// declared → applied → confirmed ladder — all pure.
package device_profile

import "errors"

// Sentinel errors let the HTTP layer map failures to 4xx (checked with errors.Is).
var (
	// ErrUnknownProfile: the equipage references a profile id not in the catalog.
	ErrUnknownProfile = errors.New("unknown device profile")
	// ErrUnknownVariant: the equipage references a variant the profile does not have.
	ErrUnknownVariant = errors.New("unknown profile variant")
	// ErrUnknownModule: the equipage references a module id not in the catalog.
	ErrUnknownModule = errors.New("unknown hardware module")
	// ErrUnknownPort: the equipage plugs a module into a port the profile does not have.
	ErrUnknownPort = errors.New("unknown expansion port")
	// ErrModuleDoesNotFit: the port does not carry a bus/pin the module needs.
	ErrModuleDoesNotFit = errors.New("module does not fit this port")
	// ErrModuleConflict: two equipped modules claim the same exclusive resource
	// (a GPIO, a UART, or the same I2C address).
	ErrModuleConflict = errors.New("module conflicts with another equipped module")
	// ErrBusShared: the module needs a bus the profile declares as shared with a
	// fixed function of the board (e.g. the blade's front UART on uart0), so
	// equipping it would break that function.
	ErrBusShared = errors.New("module needs a bus the board shares with a built-in function")
	// ErrProfileDoesNotFit: the profile's fits list does not include this node's
	// detected board kind.
	ErrProfileDoesNotFit = errors.New("profile does not fit this board")
)

// Catalog is the full loaded registry: every known board profile and module.
// Built once at boot by the infrastructure loader, validated, then read-only.
type Catalog struct {
	Boards  []BoardProfile
	Modules []ModuleDef
}

// BoardProfile describes one carrier board (or a bare board like a Pi 4).
type BoardProfile struct {
	// ID is the stable API identifier ("compute-blade").
	ID string
	// Name is the human-readable name ("Compute Blade").
	Name string
	// Description explains what the board is, shown in the picker.
	Description string
	// Fits lists the board kinds (domain/node.BoardKind string values, e.g.
	// "CM4") this carrier accepts. The comparison happens in the application
	// layer against the node's DETECTED kind; an empty list means any board.
	Fits []string
	// Variants are factory configurations of the board (soldered differences —
	// chosen at purchase, not pluggable). A profile with no variants is a
	// single-configuration board.
	Variants []Variant
	// Fixed peripherals are present on every variant.
	Fixed []Peripheral
	// Ports are the physical expansion connectors modules can plug into.
	Ports []Port
	// Identify names how the locate blink is rendered on this board. Zero
	// value means the universal sys-led fallback.
	Identify IdentifySpec
}

// Variant is one factory configuration of a board.
type Variant struct {
	// ID is stable within the profile ("basic", "tpm", "dev").
	ID string
	// Name is the short label ("TPM").
	Name string
	// Notes is operator-facing context for inventory-only variants (the DEV
	// blade's HDMI debug port), shown in the picker. May be empty.
	Notes string
	// Peripherals soldered on this variant beyond the profile's Fixed set.
	Peripherals []Peripheral
}

// Peripheral is a piece of hardware that is part of the board itself —
// either fixed or variant-specific. It carries the same apply/probe shape as
// a module; the difference is only that nobody plugs it in.
type Peripheral struct {
	// ID is stable within the profile ("tpm", "leds", "button").
	ID string
	// Name is the display label ("TPM · SLB9670").
	Name string
	// Overlays are the raw config.txt lines required for the kernel to see the
	// hardware. Empty for peripherals that need no boot config (a GPIO button).
	Overlays []string
	// Setup lists typed post-overlay steps the agent performs. See SetupStep.
	Setup []SetupStep
	// Probe declares how a scan confirms the hardware is really there.
	// Zero value: nothing to confirm — "applied" is this option's final state.
	Probe Probe
	// Capabilities this hardware grants the node once equipped (gates UI and
	// feature dispatch).
	Capabilities []string
	// ButtonGPIO names the BCM line of a physical button (the blade's edge
	// button: 20). Non-zero makes the apply tell the agent to watch the line
	// and report presses as NodeEvents. At most one button per board.
	ButtonGPIO int
}

// ModuleDef describes attachable hardware (an RTC module, a HAT). Modules are
// defined once and fit ANY port that carries the functions they need — the
// same DS3231 definition serves the blade's 10-pin header and a Pi's 40-pin.
type ModuleDef struct {
	// ID is the stable API identifier ("ds3231-rtc").
	ID string
	// Name is the display label ("DS3231 RTC module").
	Name string
	// Description explains what the module does, shown in the picker.
	Description string
	// Needs lists the pin functions the module consumes (see PinFunction
	// values in pinout.go): "i2c1", "3v3", "gpio4", "uart0", ...
	Needs []string
	// I2CAddress is the module's bus address ("0x68") when it sits on I2C.
	// Used for conflict checks only — two I2C modules coexist unless their
	// addresses collide.
	I2CAddress string
	// Overlays, Setup, Probe, Capabilities: same semantics as Peripheral.
	Overlays     []string
	Setup        []SetupStep
	Probe        Probe
	Capabilities []string
}

// SetupStep is a typed post-overlay action the agent performs during apply.
// Typed on purpose: the alternative — a free-form shell script in the registry
// — would hand the dispatch path arbitrary root commands and bypass every
// domain bound (the same reasoning that keeps system.apply_watchdog
// server-executed). The agent implements each kind natively and refuses
// kinds it does not know.
type SetupStep struct {
	// Kind is one of the SetupKind* constants.
	Kind string
}

// Known setup-step kinds. The agent ships a native implementation per kind;
// validation refuses a registry entry with an unknown kind so a typo cannot
// reach a device as a silently-skipped step.
const (
	// SetupKindRTCHwclock makes the OS actually use a hardware RTC: disables
	// the fake-hwclock service and switches /lib/udev/hwclock-set from --systz
	// to --hctosys. Idempotent.
	SetupKindRTCHwclock = "rtc_hwclock"
)

// setupKinds is the closed set validation checks against.
var setupKinds = map[string]bool{
	SetupKindRTCHwclock: true,
}

// Probe declares what a scan must find for an option to be CONFIRMED.
// The scan itself is one fixed superset script (see scan.go); a probe only
// selects which of its facts prove this option, so per-option probes never
// change what runs on the device.
type Probe struct {
	// PathsAny: confirmed when any one of these paths exists on the device.
	PathsAny []string
	// RTCNameAny: confirmed when a registered RTC driver carries one of these
	// names ("ds3231"). Use this — not a path — for external RTC modules:
	// Pi 5 / CM5 boards have a BUILT-IN rtc0, so path existence would
	// false-confirm the module on that whole family.
	RTCNameAny []string
}

// IsZero reports whether this option declares nothing to confirm.
func (p Probe) IsZero() bool { return len(p.PathsAny) == 0 && len(p.RTCNameAny) == 0 }

// Port is a physical expansion connector, described as a contiguous subset of
// a standard pinout. The blade's 10-pin header is literally pins 1-10 of the
// Pi 40-pin header, which is what makes module definitions portable.
type Port struct {
	// ID is stable within the profile ("expansion-header").
	ID string
	// Name is the display label ("Expansion header (10-pin)").
	Name string
	// Pinout names the pin table (pinout.go). Only "rpi-40pin" exists today.
	Pinout string
	// PinFrom/PinTo bound the physical pins present on this connector,
	// inclusive. 1..40 for a full header, 1..10 for the blade.
	PinFrom, PinTo int
	// SharedBuses lists pin functions that are wired to a built-in function of
	// the board as well (the blade routes uart0 to its front connector), so a
	// module consuming one is refused with the conflict named.
	SharedBuses []string
}

// IdentifySpec names how the locate blink renders on a board.
type IdentifySpec struct {
	// Backend: "" or "sys-led" (blink a /sys/class/leds entry — works on any
	// Pi via the ACT LED) or "neopixel" (declared for boards with digital
	// LEDs; falls back to sys-led until the agent ships a driver).
	Backend string
	// GPIO for the neopixel backend (the blade's digital LEDs sit on 18).
	GPIO int
	// Color for the neopixel backend ("blue").
	Color string
	// StealthGPIO: the line that drives the board's stealth transistor (cuts
	// every LED except the digital chain; blade: 21). 0 = no stealth circuit.
	StealthGPIO int `yaml:"stealth_gpio"`
	// DigitalLEDs: how many digital LEDs the chain carries (blade: 2).
	DigitalLEDs int `yaml:"digital_leds"`
}

// Board returns the profile by id.
func (c *Catalog) Board(id string) (BoardProfile, bool) {
	for _, b := range c.Boards {
		if b.ID == id {
			return b, true
		}
	}
	return BoardProfile{}, false
}

// Module returns the module by id.
func (c *Catalog) Module(id string) (ModuleDef, bool) {
	for _, m := range c.Modules {
		if m.ID == id {
			return m, true
		}
	}
	return ModuleDef{}, false
}

// Variant returns the variant by id; a profile with no variants accepts only "".
func (b BoardProfile) Variant(id string) (Variant, bool) {
	if id == "" && len(b.Variants) == 0 {
		return Variant{}, true
	}
	for _, v := range b.Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

// Port returns the port by id.
func (b BoardProfile) Port(id string) (Port, bool) {
	for _, p := range b.Ports {
		if p.ID == id {
			return p, true
		}
	}
	return Port{}, false
}

// FitsBoard reports whether this profile accepts the given detected board
// kind (domain/node.BoardKind string). An empty fits list accepts anything —
// that is what makes a "generic" profile generic. An EMPTY kind (agent never
// reported a revision) is accepted too: declaration-first means the operator
// may know the hardware better than a fleet still on old agents.
func (b BoardProfile) FitsBoard(kind string) bool {
	if len(b.Fits) == 0 || kind == "" {
		return true
	}
	for _, f := range b.Fits {
		if f == kind {
			return true
		}
	}
	return false
}
