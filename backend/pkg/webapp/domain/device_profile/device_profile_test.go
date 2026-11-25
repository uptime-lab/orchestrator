package device_profile

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// testCatalog mirrors the real registry's shape without depending on it: the
// blade with a TPM variant and a 10-pin port sharing uart0, plus a full-header
// generic board and two I2C modules.
func testCatalog() *Catalog {
	return &Catalog{
		Boards: []BoardProfile{
			{
				ID: "blade", Name: "Blade", Fits: []string{"CM4"},
				Variants: []Variant{
					{ID: "basic", Name: "Basic"},
					{ID: "tpm", Name: "TPM", Peripherals: []Peripheral{{
						ID: "tpm", Name: "TPM",
						Overlays:     []string{"dtparam=spi=on", "dtoverlay=tpm-slb9670"},
						Probe:        Probe{PathsAny: []string{"/dev/tpm0", "/dev/tpmrm0"}},
						Capabilities: []string{"tpm"},
					}}},
					{ID: "dev", Name: "DEV", Notes: "HDMI debug"},
				},
				Fixed: []Peripheral{{ID: "leds", Name: "LEDs", Capabilities: []string{"leds"}}},
				Ports: []Port{{
					ID: "header", Name: "10-pin", Pinout: PinoutRPi40,
					PinFrom: 1, PinTo: 10, SharedBuses: []string{"uart0"},
				}},
			},
			{
				ID: "generic", Name: "Generic",
				Ports: []Port{{ID: "gpio", Name: "40-pin", Pinout: PinoutRPi40, PinFrom: 1, PinTo: 40}},
			},
		},
		Modules: []ModuleDef{
			{
				ID: "rtc", Name: "DS3231", Needs: []string{"i2c1", "3v3"}, I2CAddress: "0x68",
				Overlays:     []string{"dtparam=i2c_arm=on", "dtoverlay=i2c-rtc,ds3231"},
				Setup:        []SetupStep{{Kind: SetupKindRTCHwclock}},
				Probe:        Probe{PathsAny: []string{"/sys/class/rtc/rtc0", "/dev/rtc0"}},
				Capabilities: []string{"rtc"},
			},
			{ID: "rtc2", Name: "Second RTC", Needs: []string{"i2c1"}, I2CAddress: "0x68"},
			{ID: "uart-thing", Name: "UART device", Needs: []string{"uart0"}},
			{ID: "spi-hat", Name: "SPI HAT", Needs: []string{"spi0", "gpio25"}},
		},
	}
}

func TestCatalogValidates(t *testing.T) {
	if err := testCatalog().Validate(); err != nil {
		t.Fatalf("test catalog should validate: %v", err)
	}
}

func TestValidateRejectsBadEntries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Catalog)
		want   string
	}{
		{"duplicate board id", func(c *Catalog) { c.Boards = append(c.Boards, BoardProfile{ID: "blade", Name: "X"}) }, "duplicate id"},
		{"bad overlay section filter", func(c *Catalog) { c.Modules[0].Overlays = []string{"[cm4]"} }, "section filters"},
		{"unknown setup kind", func(c *Catalog) { c.Modules[0].Setup = []SetupStep{{Kind: "rm_rf"}} }, "not implemented"},
		{"unknown need", func(c *Catalog) { c.Modules[0].Needs = []string{"warp-core"} }, "not a known pin function"},
		{"port outside pinout", func(c *Catalog) { c.Boards[0].Ports[0].PinTo = 99 }, "outside"},
		{"needless module", func(c *Catalog) { c.Modules[0].Needs = nil }, "needs is required"},
		// A fits value outside the BoardKind canon never matches a detected
		// board — the profile would be unofferable for exactly the hardware it
		// was written for, silently.
		{"unknown fits kind", func(c *Catalog) { c.Boards[0].Fits = []string{"CM-4"} }, "not a known board kind"},
		{"button gpio past the header", func(c *Catalog) {
			c.Boards[0].Fixed[0].ButtonGPIO = 28
		}, "outside BCM lines"},
		{"negative button gpio", func(c *Catalog) {
			c.Boards[0].Fixed[0].ButtonGPIO = -1
		}, "outside BCM lines"},
		{"identify gpio past the header", func(c *Catalog) {
			c.Boards[0].Identify.GPIO = 54
		}, "outside BCM lines"},
		{"two fixed buttons", func(c *Catalog) {
			c.Boards[0].Fixed = append(c.Boards[0].Fixed,
				Peripheral{ID: "btn1", Name: "B1", ButtonGPIO: 20},
				Peripheral{ID: "btn2", Name: "B2", ButtonGPIO: 21})
		}, "at most one button"},
		{"fixed plus variant button", func(c *Catalog) {
			c.Boards[0].Fixed = append(c.Boards[0].Fixed,
				Peripheral{ID: "btn1", Name: "B1", ButtonGPIO: 20})
			c.Boards[0].Variants[0].Peripherals = append(c.Boards[0].Variants[0].Peripherals,
				Peripheral{ID: "btn2", Name: "B2", ButtonGPIO: 21})
		}, "at most one button"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testCatalog()
			tc.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestEquipageValidate(t *testing.T) {
	c := testCatalog()
	ok := Equipage{ProfileID: "blade", VariantID: "tpm",
		Modules: []EquippedModule{{ModuleID: "rtc", PortID: "header"}}}
	if err := ok.Validate(c); err != nil {
		t.Fatalf("valid equipage refused: %v", err)
	}

	cases := []struct {
		name string
		e    Equipage
		want error
	}{
		{"zero is fine", Equipage{}, nil},
		{"modules without profile", Equipage{Modules: []EquippedModule{{ModuleID: "rtc", PortID: "x"}}}, ErrUnknownProfile},
		{"unknown profile", Equipage{ProfileID: "nope"}, ErrUnknownProfile},
		{"unknown variant", Equipage{ProfileID: "blade", VariantID: "ultra"}, ErrUnknownVariant},
		{"variant required when defined", Equipage{ProfileID: "blade"}, ErrUnknownVariant},
		{"no variants accepts empty", Equipage{ProfileID: "generic"}, nil},
		{"unknown module", Equipage{ProfileID: "blade", VariantID: "basic",
			Modules: []EquippedModule{{ModuleID: "nope", PortID: "header"}}}, ErrUnknownModule},
		{"unknown port", Equipage{ProfileID: "blade", VariantID: "basic",
			Modules: []EquippedModule{{ModuleID: "rtc", PortID: "nope"}}}, ErrUnknownPort},
		// The SPI HAT needs pins past 10 — the blade header cannot carry it,
		// the generic 40-pin can. Computed, not enumerated.
		{"module does not fit blade header", Equipage{ProfileID: "blade", VariantID: "basic",
			Modules: []EquippedModule{{ModuleID: "spi-hat", PortID: "header"}}}, ErrModuleDoesNotFit},
		{"same module fits full header", Equipage{ProfileID: "generic",
			Modules: []EquippedModule{{ModuleID: "spi-hat", PortID: "gpio"}}}, nil},
		{"shared bus refused", Equipage{ProfileID: "blade", VariantID: "basic",
			Modules: []EquippedModule{{ModuleID: "uart-thing", PortID: "header"}}}, ErrBusShared},
		{"i2c address collision", Equipage{ProfileID: "blade", VariantID: "basic",
			Modules: []EquippedModule{
				{ModuleID: "rtc", PortID: "header"},
				{ModuleID: "rtc2", PortID: "header"}}}, ErrModuleConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.e.Validate(c)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestRender(t *testing.T) {
	c := testCatalog()
	e := Equipage{ProfileID: "blade", VariantID: "tpm",
		Modules: []EquippedModule{{ModuleID: "rtc", PortID: "header"}}}
	r := Render(c, e)

	// Golden: [all] opener before directives (config.txt section scoping),
	// variant + module overlays present, setup deduplicated.
	want := "# Device profile: blade/tpm (managed by orchestrator)\n" +
		"[all]\n" +
		"dtparam=spi=on\n" +
		"dtoverlay=tpm-slb9670\n" +
		"dtparam=i2c_arm=on\n" +
		"dtoverlay=i2c-rtc,ds3231"
	if r.Content != want {
		t.Fatalf("render mismatch:\n got: %q\nwant: %q", r.Content, want)
	}
	if len(r.Hash) != 64 {
		t.Fatalf("hash must be full sha256 hex, got %d chars", len(r.Hash))
	}
	if len(r.Setup) != 1 || r.Setup[0].Kind != SetupKindRTCHwclock {
		t.Fatalf("setup steps: %+v", r.Setup)
	}

	// Same equipage renders the same hash; a different one differs.
	if Render(c, e).Hash != r.Hash {
		t.Fatal("render is not deterministic")
	}
	basic := Render(c, Equipage{ProfileID: "blade", VariantID: "basic"})
	if basic.Hash == r.Hash {
		t.Fatal("different equipages must hash differently")
	}
}

func TestRenderUnassignedIsCommentOnly(t *testing.T) {
	r := Render(testCatalog(), Equipage{})
	if strings.Contains(r.Content, "[all]") || strings.Contains(r.Content, "dtoverlay") {
		t.Fatalf("unassigned render must carry no directives: %q", r.Content)
	}
	if !strings.HasPrefix(r.Content, "#") {
		t.Fatalf("unassigned render must be comment-only: %q", r.Content)
	}
	if r.Hash == "" {
		t.Fatal("unassigned render still needs a hash — it participates in the ladder")
	}
}

func TestRenderNoOverlayEquipage(t *testing.T) {
	// basic variant + no modules: nothing to configure, but the block still
	// renders (and hashes) so un-assigning overlays later has a target state.
	r := Render(testCatalog(), Equipage{ProfileID: "blade", VariantID: "basic"})
	if strings.Contains(r.Content, "[all]") {
		t.Fatalf("no-overlay equipage should not emit [all]: %q", r.Content)
	}
}

func TestResolveOptionsLadder(t *testing.T) {
	c := testCatalog()
	e := Equipage{ProfileID: "blade", VariantID: "tpm",
		Modules: []EquippedModule{{ModuleID: "rtc", PortID: "header"}}}
	hash := Render(c, e).Hash

	find := func(opts []Option, id string) Option {
		t.Helper()
		for _, o := range opts {
			if o.ID == id {
				return o
			}
		}
		t.Fatalf("option %q missing in %+v", id, opts)
		return Option{}
	}

	// Never applied: everything declared.
	opts := ResolveOptions(c, e, "", nil)
	if got := find(opts, "tpm").Status; got != StatusDeclared {
		t.Fatalf("unapplied tpm: %v", got)
	}

	// Applied, no scan yet.
	opts = ResolveOptions(c, e, hash, nil)
	if got := find(opts, "tpm").Status; got != StatusApplied {
		t.Fatalf("applied tpm before scan: %v", got)
	}
	// leds has no probe -> applied is final.
	if got := find(opts, "leds").Status; got != StatusApplied {
		t.Fatalf("leds: %v", got)
	}

	// Scan saw the TPM but not the RTC.
	scan := &ScanResult{Paths: []string{"/dev/tpm0"}, At: time.Now()}
	opts = ResolveOptions(c, e, hash, scan)
	if got := find(opts, "tpm").Status; got != StatusConfirmed {
		t.Fatalf("tpm after scan: %v", got)
	}
	if got := find(opts, "rtc").Status; got != StatusNotConfirmed {
		t.Fatalf("rtc after scan: %v", got)
	}

	// Stale applied hash (equipage edited since): update_pending, scan ignored.
	opts = ResolveOptions(c, e, "0000", scan)
	if got := find(opts, "tpm").Status; got != StatusUpdatePending {
		t.Fatalf("stale hash tpm: %v", got)
	}

	// Capabilities are the union over options.
	caps := Capabilities(c, e)
	want := map[string]bool{"tpm": true, "rtc": true, "leds": true}
	if len(caps) != len(want) {
		t.Fatalf("capabilities: %v", caps)
	}
	for _, cap := range caps {
		if !want[cap] {
			t.Fatalf("unexpected capability %q", cap)
		}
	}
}

func TestParseScanOutput(t *testing.T) {
	out := "PATH /dev/tpm0\nPATH /sys/class/rtc/rtc0\nRTC rpi-rtc\nRTC DS3231\nnoise line\nHAT vendor=Acme\nHAT product=Sense Thing\nHAT broken\n"
	res := ParseScanOutput(out, time.Unix(1, 0))
	if len(res.Paths) != 2 || res.Paths[0] != "/dev/tpm0" {
		t.Fatalf("paths: %v", res.Paths)
	}
	// RTC driver names are lowercased for probe matching.
	if len(res.RTCNames) != 2 || res.RTCNames[1] != "ds3231" {
		t.Fatalf("rtc names: %v", res.RTCNames)
	}
	// The built-in Pi 5 RTC must NOT satisfy a ds3231 name probe. Real sysfs
	// format is "<driver> <device>" — measured on a CM5.
	builtinOnly := &ScanResult{
		RTCNames: []string{"rpi-rtc soc@107c000000:rpi_rtc"},
		Paths:    []string{"/sys/class/rtc/rtc0"},
	}
	if builtinOnly.hasAny(Probe{RTCNameAny: []string{"ds3231", "ds1307"}}) {
		t.Fatal("built-in RTC must not confirm an external DS3231 module")
	}
	// What a real DS3231 reports: Linux has no ds3231 driver, rtc-ds1307 serves
	// the family — measured on a CM4 carrying the module.
	ds3231 := &ScanResult{RTCNames: []string{"rtc-ds1307 1-0068"}}
	if !ds3231.hasAny(Probe{RTCNameAny: []string{"ds3231", "ds1307"}}) {
		t.Fatal("the ds1307 driver token must confirm the DS3231 module (substring match)")
	}
	if res.HAT["vendor"] != "Acme" || res.HAT["product"] != "Sense Thing" {
		t.Fatalf("hat: %v", res.HAT)
	}
	if _, ok := res.HAT["broken"]; ok {
		t.Fatal("malformed HAT line must be ignored")
	}
	// Oversized HAT values are truncated: EEPROM content is untrusted input.
	long := ParseScanOutput("HAT product="+strings.Repeat("x", 500), time.Unix(1, 0))
	if len(long.HAT["product"]) != 128 {
		t.Fatalf("truncation: %d", len(long.HAT["product"]))
	}
}

func TestFitsBoard(t *testing.T) {
	b := BoardProfile{Fits: []string{"CM4"}}
	if !b.FitsBoard("CM4") || b.FitsBoard("Pi 5") {
		t.Fatal("fits filter broken")
	}
	// Unknown detected kind is accepted: declaration-first.
	if !b.FitsBoard("") {
		t.Fatal("empty detected kind must be accepted")
	}
	if !(BoardProfile{}).FitsBoard("Pi 5") {
		t.Fatal("empty fits list must accept anything")
	}
}

// The scan script must emit the clock source as its own fact, and the parser
// must lowercase it like the RTC names so probe matching is case-insensitive.
func TestParseScanOutputClockSource(t *testing.T) {
	res := ParseScanOutput("RTC rpi-rtc\nRTC RTC-DS1307 1-0068\nCLOCKSRC RTC-DS1307 1-0068\n", time.Unix(1, 0))
	if res.ClockSourceRTC != "rtc-ds1307 1-0068" {
		t.Fatalf("clock source: %q", res.ClockSourceRTC)
	}
	if !res.inUse(Probe{RTCNameAny: []string{"ds3231", "ds1307"}}) {
		t.Fatal("the clock source must satisfy the module's own driver tokens")
	}
	// A path probe has no usage fact; it must never claim one.
	if res.inUse(Probe{PathsAny: []string{"/dev/rtc0"}}) {
		t.Fatal("a path probe cannot report usage")
	}
	// No clock source recorded = cannot tell = not a usage claim.
	none := &ScanResult{RTCNames: []string{"rtc-ds1307 1-0068"}}
	if none.inUse(Probe{RTCNameAny: []string{"ds1307"}}) {
		t.Fatal("an absent clock source must not read as in use")
	}
}
