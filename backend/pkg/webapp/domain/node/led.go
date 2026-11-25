package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// LED behaviour is a per-node desired state converged by the queueable
// apply_led_config command and persisted agent-side, so a reboot restores it
// with the platform unreachable. Three independent knobs (see the Compute
// Blade documentation's "Stealth mode" section for the hardware facts):
//
//   - StealthMode (blade only): GPIO 21 drives a transistor that cuts every
//     LED except the digital chain, so identify keeps working.
//   - ActLEDOff / PwrLEDOff (any Pi): sysfs trigger=none + brightness=0.
//   - DigitalSources (blade only): per-LED ambient indication rendered by the
//     agent; identify preempts it and it resumes when the blink ends.

// ErrLEDNotSupported marks a config the node's board cannot honor; handlers
// answer it 400 with the remedy in the message.
var ErrLEDNotSupported = errors.New("led configuration not supported by this board")

// ErrLEDInvalid marks a config that fails the domain bounds; handlers answer
// it 400. A sentinel rather than bare fmt.Errorf so the handler's error
// mapping can tell an operator-worded refusal from a platform failure.
var ErrLEDInvalid = errors.New("invalid led configuration")

// LEDSourceOff / valid digital LED indication sources. RAM and disk are LED
// v2 sources: agents advertise what they can render (ConnectionInit
// led_sources), and SetLED refuses a source the node's agent has not
// advertised — with the v1 pair as the assumption for agents that predate
// the advertisement.
const (
	LEDSourceOff         = "off"
	LEDSourceTemperature = "temperature"
	LEDSourceLoad        = "load"
	LEDSourceRAM         = "ram"
	LEDSourceDisk        = "disk"
)

// LEDV1Sources is what an agent that advertises nothing can render.
var LEDV1Sources = []string{LEDSourceTemperature, LEDSourceLoad}

// LED v2 per-source gradient defaults — the constants the agent has always
// used, now the fallbacks for absent spec fields (docs/DEVICE-PROFILES.md →
// LED indication v2).
const (
	LEDDefaultTempLow    = 45
	LEDDefaultTempHigh   = 80
	LEDDefaultPctLow     = 0
	LEDDefaultPctHigh    = 100
	LEDDefaultBrightness = 20
)

// LEDMaxDigitalSources bounds the per-LED list — a runaway guard; every known
// board carries two.
const LEDMaxDigitalSources = 8

// LEDConfig is the desired LED behaviour for one node.
type LEDConfig struct {
	StealthMode bool `json:"stealth_mode"`
	ActLEDOff   bool `json:"act_led_off"`
	PwrLEDOff   bool `json:"pwr_led_off"`
	// DigitalSources assigns an indication source per digital LED (index =
	// chain position). Absent entries render dark. With DigitalLEDs present
	// this is the DERIVED v1 projection (sources only), kept in the wire
	// payload so an older agent renders the same sources with its built-in
	// defaults.
	DigitalSources []string `json:"digital_sources,omitempty"`
	// DigitalLEDs is the LED v2 parameterized form: per-LED source plus
	// gradient bounds and brightness. When present it is the source of truth
	// and DigitalSources is derived from it.
	DigitalLEDs []DigitalLEDSpec `json:"digital_leds,omitempty"`
}

// DigitalLEDSpec parameterizes one digital LED (LED v2). Zero Low/High/
// Brightness mean "the per-source default" — 45..80 °C for temperature,
// 0..100 % for the percentage sources, 20 % brightness — so a config that
// names only sources behaves exactly as v1 did.
type DigitalLEDSpec struct {
	Source     string  `json:"source"`
	Low        float64 `json:"low,omitempty"`
	High       float64 `json:"high,omitempty"`
	Brightness int     `json:"brightness,omitempty"`
}

// EffectiveSources returns the per-LED source list whichever form the config
// carries — the v1 projection the wire payload and older agents consume.
func (c *LEDConfig) EffectiveSources() []string {
	if len(c.DigitalLEDs) == 0 {
		return c.DigitalSources
	}
	out := make([]string, len(c.DigitalLEDs))
	for i, spec := range c.DigitalLEDs {
		out[i] = spec.Source
	}
	return out
}

// Validate checks the config invariants that hold regardless of board.
// Whether the node's AGENT can render a source is SetLED's question (it holds
// the advertisement); this only bounds the vocabulary and the numbers.
func (c *LEDConfig) Validate() error {
	if len(c.DigitalSources) > LEDMaxDigitalSources || len(c.DigitalLEDs) > LEDMaxDigitalSources {
		return fmt.Errorf("%w: at most %d digital LED sources are supported", ErrLEDInvalid, LEDMaxDigitalSources)
	}
	if len(c.DigitalLEDs) > 0 && len(c.DigitalSources) > 0 {
		// Both forms in one submission invite a client to disagree with
		// itself; the platform derives the projection, never the caller.
		return fmt.Errorf("%w: send either digital_leds (v2) or digital_sources (v1), not both", ErrLEDInvalid)
	}
	validSource := func(s string) bool {
		switch s {
		case LEDSourceOff, LEDSourceTemperature, LEDSourceLoad, LEDSourceRAM, LEDSourceDisk:
			return true
		}
		return false
	}
	for i, s := range c.DigitalSources {
		if !validSource(s) {
			return fmt.Errorf("%w: digital LED %d: unknown source %q (want off, temperature, load, ram or disk)", ErrLEDInvalid, i+1, s)
		}
	}
	for i, spec := range c.DigitalLEDs {
		if !validSource(spec.Source) {
			return fmt.Errorf("%w: digital LED %d: unknown source %q (want off, temperature, load, ram or disk)", ErrLEDInvalid, i+1, spec.Source)
		}
		if spec.Low != 0 || spec.High != 0 {
			if spec.High <= spec.Low {
				return fmt.Errorf("%w: digital LED %d: high must be above low", ErrLEDInvalid, i+1)
			}
		}
		if spec.Brightness < 0 || spec.Brightness > 100 {
			return fmt.Errorf("%w: digital LED %d: brightness is a percentage (0 = default, 1..100)", ErrLEDInvalid, i+1)
		}
	}
	return nil
}

// WantsIndication reports whether any digital LED has an ambient source.
func (c *LEDConfig) WantsIndication() bool {
	for _, s := range c.EffectiveSources() {
		if s != "" && s != LEDSourceOff {
			return true
		}
	}
	return false
}

// IsZero reports an all-defaults config (nothing to converge).
func (c *LEDConfig) IsZero() bool {
	return !c.StealthMode && !c.ActLEDOff && !c.PwrLEDOff && !c.WantsIndication()
}

// Hash is the desired-state fingerprint, recorded at settle so the UI can
// tell "asked for" from "running".
func (c *LEDConfig) Hash() string {
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// LEDCapabilities is what the node's effective device profile makes possible
// — the UI renders only the controls the board can honor.
type LEDCapabilities struct {
	// StealthAvailable: the board declares a stealth transistor GPIO.
	StealthAvailable bool `json:"stealth_available"`
	// DigitalLEDs: how many digital LEDs the board carries (0 = none).
	DigitalLEDs int `json:"digital_leds"`
	// SupportedSources: the ambient sources the node's AGENT can render (its
	// advertisement, or the v1 pair for agents predating LED v2). The UI
	// offers exactly these.
	SupportedSources []string `json:"supported_sources,omitempty"`
}

// LEDCommandRef references the newest apply_led_config command.
type LEDCommandRef struct {
	ID      uuid.UUID `json:"id"`
	Status  string    `json:"status"`
	Details string    `json:"details,omitempty"`
}

// LEDState is the full per-node LED picture the API returns.
type LEDState struct {
	Desired *LEDConfig `json:"desired,omitempty"`
	// DesiredSource: which level supplied Desired (node|ou|root_ou|account).
	// Absent when nothing is declared anywhere.
	DesiredSource string          `json:"desired_source,omitempty"`
	DesiredHash   string          `json:"desired_hash,omitempty"`
	AppliedHash  string          `json:"applied_hash,omitempty"`
	InSync       bool            `json:"in_sync"`
	Queued       bool            `json:"queued"`
	LastCommand  *LEDCommandRef  `json:"last_command,omitempty"`
	Capabilities LEDCapabilities `json:"capabilities"`
	// CapabilitiesUnknown marks a read where the device-profile resolver
	// FAILED: Capabilities is then empty because nothing could be read, not
	// because the board declares nothing — the UI must say "could not
	// resolve", never "this board has no LED features".
	CapabilitiesUnknown bool `json:"capabilities_unknown,omitempty"`
}
