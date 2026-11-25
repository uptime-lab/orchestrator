package device_profile_registry

import (
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
)

// TestEmbeddedRegistryLoads is the CI gate for the registry files themselves:
// a YAML entry that fails schema validation, uses an unknown key, or declares
// a setup kind the agent does not implement breaks the build here — before it
// can reach a boot fail-fast, let alone a device.
func TestEmbeddedRegistryLoads(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("embedded registry must load: %v", err)
	}
	if len(c.Boards) == 0 || len(c.Modules) == 0 {
		t.Fatalf("registry unexpectedly empty: %d boards, %d modules", len(c.Boards), len(c.Modules))
	}
}

// TestComputeBladeShape pins the first real citizens: the decisions the
// registry encodes (TPM is a factory variant, DEV carries it too, the
// header shares uart0, the RTC fits both the blade and a generic 40-pin)
// asserted against the shipped files.
func TestComputeBladeShape(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	blade, ok := c.Board("compute-blade")
	if !ok {
		t.Fatal("compute-blade missing")
	}
	// The blade socket takes both compute-module generations — but never a
	// full-size Pi, which is its own carrier.
	for _, kind := range []string{"CM4", "CM5", "CM5 Lite"} {
		if !blade.FitsBoard(kind) {
			t.Fatalf("blade must fit %s: %v", kind, blade.Fits)
		}
	}
	if blade.FitsBoard("Pi 5") || blade.FitsBoard("Pi 4") {
		t.Fatalf("blade must not fit full-size boards: %v", blade.Fits)
	}

	tpm, ok := blade.Variant("tpm")
	if !ok || len(tpm.Peripherals) != 1 || tpm.Peripherals[0].Probe.IsZero() {
		t.Fatalf("tpm variant shape: %+v", tpm)
	}
	// DEV carries the TPM too (plus human-facing debug hardware in notes).
	dev, ok := blade.Variant("dev")
	if !ok || len(dev.Peripherals) != 1 || dev.Peripherals[0].ID != "tpm" || dev.Notes == "" {
		t.Fatalf("dev must carry the TPM and notes: %+v", dev)
	}

	header, ok := blade.Port("expansion-header")
	if !ok || header.PinFrom != 1 || header.PinTo != 10 {
		t.Fatalf("header: %+v", header)
	}

	rtc, ok := c.Module("ds3231-rtc")
	if !ok {
		t.Fatal("ds3231-rtc missing")
	}
	if missing, ok := device_profile.ModuleFitsPort(rtc, header); !ok {
		t.Fatalf("RTC must fit the blade header, missing %s", missing)
	}
	generic, _ := c.Board("generic-cm4")
	full, ok := generic.Port("gpio-header")
	if !ok {
		t.Fatal("generic-cm4 gpio-header missing")
	}
	if _, ok := device_profile.ModuleFitsPort(rtc, full); !ok {
		t.Fatal("RTC must fit the full header too — one definition, both ports")
	}

	// The full blade TPM+RTC equipage validates and renders every overlay.
	e := device_profile.Equipage{
		ProfileID: "compute-blade", VariantID: "tpm",
		Modules: []device_profile.EquippedModule{{ModuleID: "ds3231-rtc", PortID: "expansion-header"}},
	}
	if err := e.Validate(c); err != nil {
		t.Fatalf("blade tpm+rtc equipage: %v", err)
	}
	r := device_profile.Render(c, e)
	for _, want := range []string{"[all]", "dtoverlay=tpm-slb9670", "dtoverlay=i2c-rtc,ds3231", "dtoverlay=dwc2,dr_mode=host"} {
		if !strings.Contains(r.Content, want) {
			t.Fatalf("render missing %q:\n%s", want, r.Content)
		}
	}
	if len(r.Setup) != 1 || r.Setup[0].Kind != device_profile.SetupKindRTCHwclock {
		t.Fatalf("setup: %+v", r.Setup)
	}
}

// TestShippedRTCProbeMatchesRealDevice drives the SHIPPED registry against the
// sysfs name a DS3231 actually reports. The domain test builds its own catalog,
// so a probe token that matches nothing on real hardware passed everything and
// showed the operator "not confirmed" on a working module for as long as it
// took someone to look at a blade with an RTC in it.
func TestShippedRTCProbeMatchesRealDevice(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	e := device_profile.Equipage{
		ProfileID: "compute-blade", VariantID: "basic",
		Modules: []device_profile.EquippedModule{{ModuleID: "ds3231-rtc", PortID: "expansion-header"}},
	}
	applied := device_profile.Render(c, e).Hash

	// Linux binds the DS3231 with drivers/rtc/rtc-ds1307.c, so the chip's
	// own name never appears in /sys/class/rtc/rtc0/name.
	module := &device_profile.ScanResult{
		Paths:    []string{"/dev/rtc0", "/sys/class/rtc/rtc0"},
		RTCNames: []string{"rtc-ds1307 1-0068"},
	}
	if got := rtcStatus(t, c, e, applied, module); got != device_profile.StatusConfirmed {
		t.Fatalf("a real DS3231 must confirm the module, got %v", got)
	}

	// A board whose only RTC is the built-in one must still read as absent —
	// that is the whole reason this probe is on the driver name and not on
	// /sys/class/rtc/rtc0 existing.
	builtin := &device_profile.ScanResult{
		Paths:    []string{"/dev/rtc0", "/sys/class/rtc/rtc0"},
		RTCNames: []string{"rpi-rtc soc@107c000000:rpi_rtc"},
	}
	if got := rtcStatus(t, c, e, applied, builtin); got != device_profile.StatusNotConfirmed {
		t.Fatalf("the built-in RTC must not confirm the module, got %v", got)
	}
}

func rtcStatus(t *testing.T, c *device_profile.Catalog, e device_profile.Equipage,
	applied string, scan *device_profile.ScanResult) device_profile.OptionStatus {
	t.Helper()
	for _, o := range device_profile.ResolveOptions(c, e, applied, scan) {
		if o.ID == "ds3231-rtc" {
			return o.Status
		}
	}
	t.Fatal("ds3231-rtc option missing")
	return ""
}

// TestStrictDecodeRefusesUnknownKeys: a typo'd YAML key must fail, not
// silently drop intent.
func TestStrictDecodeRefusesUnknownKeys(t *testing.T) {
	var b boardYAML
	err := strictDecode([]byte("id: x\nname: X\noverlayz: []\n"), &b)
	if err == nil {
		t.Fatal("unknown key must be refused")
	}
}

// TestStrictDecodeRefusesSecondDocument: a single Decode reads only the first
// YAML document, so a `---`-separated second entry would be silently dropped
// and its board would read as "does not exist" instead of as a file mistake.
func TestStrictDecodeRefusesSecondDocument(t *testing.T) {
	var b boardYAML
	err := strictDecode([]byte("id: a\nname: A\n---\nid: b\nname: B\n"), &b)
	if err == nil || !strings.Contains(err.Error(), "more than one YAML document") {
		t.Fatalf("second document must be refused, got %v", err)
	}
}

// TestShippedRTCReachesTheWorkingRung: presence and USE are different facts,
// and the whole point of the working rung is that "confirmed" reports a clock
// the kernel ignores as a success. Driven against the SHIPPED registry, with
// the sysfs strings a real device produces.
func TestShippedRTCReachesTheWorkingRung(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	e := device_profile.Equipage{
		ProfileID: "compute-blade", VariantID: "basic",
		Modules: []device_profile.EquippedModule{{ModuleID: "ds3231-rtc", PortID: "expansion-header"}},
	}
	applied := device_profile.Render(c, e).Hash
	present := []string{"/dev/rtc0", "/sys/class/rtc/rtc0"}

	// hctosys=1 on the ds1307-driven device, i.e. the kernel took the system
	// clock from it.
	inUse := &device_profile.ScanResult{
		Paths: present, RTCNames: []string{"rtc-ds1307 1-0068"},
		ClockSourceRTC: "rtc-ds1307 1-0068",
	}
	if got := rtcStatus(t, c, e, applied, inUse); got != device_profile.StatusWorking {
		t.Fatalf("an RTC the kernel keeps time from must read working, got %v", got)
	}

	// Answering on I2C but NOT the clock source — a board whose built-in RTC
	// won hctosys. Confirmed, never working: the module is there and unused.
	idle := &device_profile.ScanResult{
		Paths: present, RTCNames: []string{"rtc-ds1307 1-0068", "rpi-rtc soc@107c000000:rpi_rtc"},
		ClockSourceRTC: "rpi-rtc soc@107c000000:rpi_rtc",
	}
	if got := rtcStatus(t, c, e, applied, idle); got != device_profile.StatusConfirmed {
		t.Fatalf("a module that is not the clock source must stop at confirmed, got %v", got)
	}

	// A scan taken before this fact existed carries no clock source. That is
	// "cannot tell", so it must fall back to confirmed rather than claim the
	// module is unused.
	old := &device_profile.ScanResult{Paths: present, RTCNames: []string{"rtc-ds1307 1-0068"}}
	if got := rtcStatus(t, c, e, applied, old); got != device_profile.StatusConfirmed {
		t.Fatalf("a pre-existing scan must stay confirmed, got %v", got)
	}
}
