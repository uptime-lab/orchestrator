// Package device_profile_registry loads the embedded YAML registry of carrier
// boards and hardware modules into the pure domain catalog.
//
// The registry is DATA shipped inside the binary (go:embed, like the signing
// keys), not a database table and not runtime-loadable: its content becomes
// config.txt lines on live devices, so a new board goes through git review and
// a backend deploy. The YAML decode lives here — not in domain/device_profile
// — because the domain layer carries no external dependencies; domain owns the
// typed schema and every validation rule, this package only feeds it.
//
// Load is called twice with the same rules: from go test (a bad entry breaks
// CI before merging) and from cmd/server/main.go at boot, where an error is
// fatal in the composition root — a registry the binary cannot parse must not
// serve traffic.
package device_profile_registry

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
)

//go:embed registry/boards/*.yaml registry/modules/*.yaml
var registryFS embed.FS

// YAML DTOs. Kept separate from the domain structs so the file format can
// stay stable while the domain evolves, and so decoding is strict:
// KnownFields refuses a typo'd key instead of silently dropping the entry's
// intent.

type boardYAML struct {
	ID          string           `yaml:"id"`
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Fits        []string         `yaml:"fits"`
	Identify    identifyYAML     `yaml:"identify"`
	Fixed       []peripheralYAML `yaml:"fixed"`
	Variants    []variantYAML    `yaml:"variants"`
	Ports       []portYAML       `yaml:"ports"`
}

type variantYAML struct {
	ID          string           `yaml:"id"`
	Name        string           `yaml:"name"`
	Notes       string           `yaml:"notes"`
	Peripherals []peripheralYAML `yaml:"peripherals"`
}

type peripheralYAML struct {
	ID           string      `yaml:"id"`
	Name         string      `yaml:"name"`
	Overlays     []string    `yaml:"overlays"`
	Setup        []setupYAML `yaml:"setup"`
	Probe        probeYAML   `yaml:"probe"`
	Capabilities []string    `yaml:"capabilities"`
	ButtonGPIO   int         `yaml:"button_gpio"`
}

type setupYAML struct {
	Kind string `yaml:"kind"`
}

type probeYAML struct {
	PathsAny   []string `yaml:"paths_any"`
	RTCNameAny []string `yaml:"rtc_name_any"`
}

type portYAML struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Pinout      string   `yaml:"pinout"`
	PinFrom     int      `yaml:"pin_from"`
	PinTo       int      `yaml:"pin_to"`
	SharedBuses []string `yaml:"shared_buses"`
}

type identifyYAML struct {
	Backend     string `yaml:"backend"`
	GPIO        int    `yaml:"gpio"`
	Color       string `yaml:"color"`
	StealthGPIO int    `yaml:"stealth_gpio"`
	DigitalLEDs int    `yaml:"digital_leds"`
}

type moduleYAML struct {
	ID           string      `yaml:"id"`
	Name         string      `yaml:"name"`
	Description  string      `yaml:"description"`
	Needs        []string    `yaml:"needs"`
	I2CAddress   string      `yaml:"i2c_address"`
	Overlays     []string    `yaml:"overlays"`
	Setup        []setupYAML `yaml:"setup"`
	Probe        probeYAML   `yaml:"probe"`
	Capabilities []string    `yaml:"capabilities"`
}

// Load parses and validates the embedded registry. The returned catalog is
// the process-wide read-only source of truth.
func Load() (*device_profile.Catalog, error) {
	catalog := &device_profile.Catalog{}

	if err := eachFile("registry/boards", func(name string, data []byte) error {
		var b boardYAML
		if err := strictDecode(data, &b); err != nil {
			return err
		}
		catalog.Boards = append(catalog.Boards, toBoard(b))
		return nil
	}); err != nil {
		return nil, err
	}

	if err := eachFile("registry/modules", func(name string, data []byte) error {
		var m moduleYAML
		if err := strictDecode(data, &m); err != nil {
			return err
		}
		catalog.Modules = append(catalog.Modules, toModule(m))
		return nil
	}); err != nil {
		return nil, err
	}

	if err := catalog.Validate(); err != nil {
		return nil, fmt.Errorf("device profile registry: %w", err)
	}
	return catalog, nil
}

// eachFile walks one embedded directory in a deterministic (sorted) order so
// the catalog — and anything derived from it — is stable across builds.
func eachFile(dir string, fn func(name string, data []byte) error) error {
	entries, err := fs.ReadDir(registryFS, dir)
	if err != nil {
		return fmt.Errorf("device profile registry: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := registryFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("device profile registry: %s: %w", name, err)
		}
		if err := fn(name, data); err != nil {
			return fmt.Errorf("device profile registry: %s: %w", name, err)
		}
	}
	return nil
}

// strictDecode refuses unknown keys — a typo in a registry file must fail the
// build, not silently drop a field whose absence reads as "no overlays".
func strictDecode(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return err
	}
	// One document per file: a single Decode reads only the first, so a
	// `---`-separated second entry would be dropped without an error and its
	// board would read as "does not exist" instead of as a file mistake.
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("more than one YAML document in file — one entry per file")
		}
		return err
	}
	return nil
}

func toBoard(b boardYAML) device_profile.BoardProfile {
	board := device_profile.BoardProfile{
		ID:          b.ID,
		Name:        b.Name,
		Description: b.Description,
		Fits:        b.Fits,
		Identify: device_profile.IdentifySpec{
			Backend:     b.Identify.Backend,
			GPIO:        b.Identify.GPIO,
			Color:       b.Identify.Color,
			StealthGPIO: b.Identify.StealthGPIO,
			DigitalLEDs: b.Identify.DigitalLEDs,
		},
	}
	for _, p := range b.Fixed {
		board.Fixed = append(board.Fixed, toPeripheral(p))
	}
	for _, v := range b.Variants {
		variant := device_profile.Variant{ID: v.ID, Name: v.Name, Notes: v.Notes}
		for _, p := range v.Peripherals {
			variant.Peripherals = append(variant.Peripherals, toPeripheral(p))
		}
		board.Variants = append(board.Variants, variant)
	}
	for _, p := range b.Ports {
		board.Ports = append(board.Ports, device_profile.Port{
			ID:          p.ID,
			Name:        p.Name,
			Pinout:      p.Pinout,
			PinFrom:     p.PinFrom,
			PinTo:       p.PinTo,
			SharedBuses: p.SharedBuses,
		})
	}
	return board
}

func toPeripheral(p peripheralYAML) device_profile.Peripheral {
	return device_profile.Peripheral{
		ID:           p.ID,
		Name:         p.Name,
		Overlays:     p.Overlays,
		Setup:        toSetup(p.Setup),
		Probe:        device_profile.Probe{PathsAny: p.Probe.PathsAny, RTCNameAny: p.Probe.RTCNameAny},
		Capabilities: p.Capabilities,
		ButtonGPIO:   p.ButtonGPIO,
	}
}

func toModule(m moduleYAML) device_profile.ModuleDef {
	return device_profile.ModuleDef{
		ID:           m.ID,
		Name:         m.Name,
		Description:  m.Description,
		Needs:        m.Needs,
		I2CAddress:   m.I2CAddress,
		Overlays:     m.Overlays,
		Setup:        toSetup(m.Setup),
		Probe:        device_profile.Probe{PathsAny: m.Probe.PathsAny, RTCNameAny: m.Probe.RTCNameAny},
		Capabilities: m.Capabilities,
	}
}

func toSetup(in []setupYAML) []device_profile.SetupStep {
	var out []device_profile.SetupStep
	for _, s := range in {
		out = append(out, device_profile.SetupStep{Kind: s.Kind})
	}
	return out
}
