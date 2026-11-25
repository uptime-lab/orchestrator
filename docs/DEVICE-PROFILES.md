# Device Profiles

Declared hardware model for carrier boards and attachable modules — Compute
Blade support without making anything blade-specific. Phase 1 has shipped; the
reasoning below is the part that is expensive to re-derive.

## The model

**The compute module is detected; the carrier is declared; a scan only
confirms.** The board revision code identifies the CM4/CM5/Pi reliably
(`domain/node/board_revision.go`), but nothing identifies the carrier board it
sits on — and a peripheral like the blade's TPM (SLB9670 on SPI) or a DS3231
RTC is invisible to the kernel until its device-tree overlay is applied, so
even "probe for the chip" cannot bootstrap the declaration. Detection is
therefore demoted permanently: the operator declares, the platform applies,
the scan confirms. A wrong declaration is safe and visible — the overlay loads
inert and the option sticks at `applied, not confirmed`.

Three registry concepts (`domain/device_profile`, pure; YAML loaded by
`infrastructure/device_profile_registry` via go:embed):

- **Board profile** — fixed peripherals (the blade: button, digital LEDs,
  USB-A), **factory variants** (Basic / TPM / DEV — soldered differences,
  chosen at purchase; DEV carries the TPM too, its HDMI debug port and USB-C
  bootloader access staying inventory-only notes), and
  **ports** described as contiguous subsets of the standard Pi 40-pin pinout.
  The blade's 10-pin header is literally pins 1–10, which is the whole trick:
- **Module** — declares the pin functions it needs (`i2c1`, `3v3`, `gpio4`,
  ...), its overlays, typed setup steps, a probe, and the capabilities it
  grants. Module↔port compatibility is **computed** from the pinout table,
  never enumerated — one DS3231 definition fits the blade header and any full
  40-pin board. Conflicts (exclusive GPIO/UART, same I2C address, a bus the
  board shares with a built-in function like the blade's front UART on uart0)
  are refused at SAVE time.
- **Equipage** — what one node declares: profile + variant + which module in
  which port. Inheritable node → OU → root → account **as one block**
  (`organizational_unit.ResolveDeviceProfile`); half a hardware declaration is
  not a meaningful state.

Registry entries are data in git (`registry/boards/*.yaml`,
`registry/modules/*.yaml`), validated identically in `go test` and at boot
(fail-fast in main.go). Adding a board = YAML + PR + backend deploy; the
catalog API (`GET /api/v1/device-profiles`) drives the picker and forms, so no
frontend change either. The YAML schema is the future third-party format if
profiles ever open up.

## The ladder: declared → applied → confirmed → working

Per-option status, computed — never stored — from three columns on `nodes`
with ONE writer each (the watchdog precedent): `device_profile` (assignment
endpoint), `device_profile_applied_hash` (the gRPC settle hook), and
`device_profile_scan` (the scan poll).

Alongside the ladder, one more computed answer: **`reboot_pending`** — the
applied config is written but the boot that activates it has not happened.
`device_profile_applied_at` rides the SAME write as the applied hash (two
writes is how they drift apart) and is compared against `nodes.last_boot_at`.

That boot moment is derived from the agent's **uptime**, never from the
device's `BootInfo.BootedAt`: a blank hardware RTC puts the device's wall clock
decades in the past (a node with a blank RTC boots into 2000-01-01), which would
place every boot before every apply and pin `reboot_pending` true forever. It is
stamped when the report ARRIVES, because a stale uptime paired with a fresh
clock walks the computed moment forward until any node looks freshly rebooted.

`reboot_pending` is **tri-state**: absent = the platform cannot tell (nothing
applied, an edit outstanding, or an agent that reports no uptime), and a caller
must then keep offering the reboot. Only an explicit `false` may hide it —
withholding an action the operator needs is worse than offering a spare one.
Before this existed the panel offered Reboot unconditionally after any apply,
which is how a node that had rebooted repeatedly kept a red primary button
implying a reboot would fix a server-side probe bug.

- The **applied hash is computed from the RENDERED config.txt content at
  dispatch and travels in the command payload**; the settle hook records that
  value verbatim. Never recomputed at settle: the equipage may have been
  edited while the command sat queued, and marking the newer desired state
  applied on the strength of the older dispatch is exactly the confusion the
  ladder exists to expose. A registry edit in a release therefore shows as
  `update_pending` ("re-apply") instead of silently rewriting the fleet.
- The render opens with `[all]` (config.txt section scoping — the overclock
  lesson) and un-assignment renders a **comment-only block** whose hash still
  participates: a managed block cannot be removed, only emptied, and without
  a renderable "no profile" state stale overlays would load forever.
- `not_confirmed` is deliberately NOT an error state: it is the honest
  "declared, but the board does not answer" signal (wrong variant, unseated
  module, or no reboot yet).
- **`working` is a rung ABOVE `confirmed`**, and the distinction is the point:
  confirmed means the hardware ANSWERED, working means the OS is actually
  USING it. An RTC can answer on I2C while the kernel takes its time from a
  different clock entirely, and reporting that as a success is what the badge
  did before. The scan emits the name of the RTC carrying sysfs `hctosys=1` —
  the one the kernel read the system clock from at boot — and an option reaches
  `working` when that name matches the SAME driver tokens its presence probe
  already declares, so the registry keeps one list and cannot drift from
  itself. Reachable only where a usage fact exists (today: the RTC family);
  everything else settles at `confirmed`, which is not a lesser outcome, just a
  smaller claim. An old scan with no clock source recorded means "cannot tell"
  and stays `confirmed` — never "unused".

## Why apply is its own slug

`apply_device_profile` (queueable, managed-OS-only, floor **0.6.0**, one
command carrying the config patch AND typed setup steps) exists because the
obvious alternative — `patch_file` + `run_script` — fails three ways:

1. The queue's supersede key is `slug:nodeID`; sharing `patch_file` with the
   overclock feature means a queued profile apply and a queued overclock apply
   for one offline node silently retire each other as `superseded`.
2. `patch_file` is queueable but `run_script` is not, so an offline node would
   get half an apply: config patched on reconnect, fake-hwclock never
   disabled — and "the command's terminal status" is ambiguous between two
   commands.
3. `patch_file` is not in `managedOSOnlySlugs`, so a netbooted node would take
   the patch into its initramfs, report success, and the ladder would record
   an applied hash for a config.txt that evaporates on reboot.

Setup steps are **typed kinds** (`rtc_hwclock`: disable fake-hwclock where
present, switch `/lib/udev/hwclock-set` from `--systz` to `--hctosys`, then
**seed the module from the system clock**), implemented natively by the agent,
which FAILS on a kind it does not know — a registry/agent version mismatch must
surface, not skip.

The seed is the half that decides whether the clock is any use, and it is easy
to leave out because the other two steps look like the work. **On Ubuntu both
are no-ops** — neither fake-hwclock nor `/lib/udev/hwclock-set` is installed —
so without the seed an apply leaves a brand-new module holding its power-on
default and the next boot sets the SYSTEM clock from it: observed as
`2000-01-01T00:01:03` on a CM4 blade, 26 years backwards, with every TLS dial
failing "certificate is not yet valid" until chrony steps it. The kernel's own
`CONFIG_RTC_SYSTOHC` write-back repairs the chip within ~11 minutes of the clock
being disciplined, which is why the second boot looks fine and the bug hides —
but a device powered off inside that window boots into 2000 again.

Two things about the seed:

- The apply itself usually CANNOT do it. The overlay it just wrote to config.txt
  takes effect on the reboot it asks for, so on a first apply there is no
  `/dev/rtc*` yet. The agent's **`StartRTCKeeper`** catches that boot: it waits
  (bounded) for the kernel to report the clock disciplined — `STA_UNSYNC` via
  `adjtimex`, exactly what `timedatectl` calls "System clock synchronized" —
  and only then writes. Writing an undisciplined clock would replace a
  possibly-good hardware time with a definitely-bad one. The apply-time seed is
  what makes a RE-apply (swapped module, replaced battery) take effect without
  a reboot.
- The write is **read back**, because a clone module that ACKs a write it does
  not apply otherwise looks seeded forever and only shows up at the next power
  cut.

Only an I2C-attached RTC is touched. A board's built-in clock (a Pi 5's
`rpi-rtc soc@…`, a platform device) is left alone — this exists for the modules
a profile declares.

**A probe matches the KERNEL DRIVER's name, not the chip's.** Linux has no
ds3231 driver: `drivers/rtc/rtc-ds1307.c` serves the family, so a DS3231 reports
`rtc-ds1307 1-0068` and the obvious `rtc_name_any: ["ds3231"]` never matched
anything, showing a working module as "not confirmed". Also note the chip
identity in sysfs (`/sys/bus/i2c/devices/1-0068/name` = `ds3231`) is DECLARED by
the overlay, not detected — it is not evidence about what is plugged in.
The DS3231 oscillator-stop flag (status bit 7) is **not** a usable
battery indicator here: it was measured still set after a write that
demonstrably landed, so the driver's `SET TIME!` on every boot is cosmetic. The
only test of battery retention is a real power-off. No free-form shell
reaches the dispatch payload (the `system.apply_watchdog` reasoning), which is
also why the slug is pinned UNMAPPED for generic command dispatch
(`authz/mapping.go` + contract test).

## Scan

One **fixed** read-only superset script (`device_profile.ScanScript`) riding
`run_script`, exactly like the os-users scan — the GET poll authorizes reading
the result by requiring the stored script to EQUAL the constant, and a
per-equipage script would break that check. Module probes select facts FROM
the dump (`paths_any`); they never change what runs. The dump also carries the
HAT ID EEPROM fields. Scan is refused while netbooted (the recovery bootstrap
never applied the overlays, so every probe would answer a false "absent") and
the parsed result is persisted at poll time.

## Identify

`identify` (floor 0.6.0, mapped to `node.status_check`, NOT queueable): the
agent blinks a sysfs LED (ACT-first candidate list), reports terminal SUCCESS
**immediately after starting** — a command held in_progress would pin the node
in `maintenance` for the whole blink, exactly while an operator is hunting for
it — and stops on a local timer; a repeat command replaces the blink. The
profile may declare `identify: {backend: neopixel, gpio: 18, color: blue}`
(the blade's digital LEDs), and since 0.6.6/0.6.7 the agent drives them
natively — `OpenNeoPixel` picks RP1 PIO on CM5/Pi 5 and the BCM2711 PWM
serializer on CM4/Pi 4, falling back to sys-led on any SoC with neither, and on
any node whose profile declares no digital LEDs. Every node gets the feature;
the blade just gets it prettier.

## UI

Zero new UI for a node that neither has a profile nor could take one — the
compatible-profile list is computed on the platform (`fits` vs detected board
kind; the frontend never re-derives board identity, the four-drifting-copies
lesson). Overview shows a read-only resolved `Platform` row + detected HAT;
Properties carries the picker (Boot Behavior tri-state language, Inherit +
compatible boards), variant, modules, ladder badges, Scan, and Apply behind a
confirm dialog that shows the **exact config.txt block** — the content is the
consent. A reboot is required after apply and the platform never reboots on
its own.

## Button events (device→web identify — shipped with phase 2)

The blade's edge button lights the node up in the UI: `NodeEvent` is the
protocol's first agent-INITIATED message (`NodeToPlatform` oneof field 13,
with `press_kind` so long-press semantics are an addition, not a change), and
like MetricsUpdate it is dropped when disconnected — a replayed press claims a
human touched the machine at a moment nobody did. Which line to watch comes
from the applied profile (`ApplyDeviceProfileCommand.button_gpio`, rendered
from the peripheral's `button_gpio` — deliberately OUTSIDE the content hash,
or every equipage would flip to update_pending when a profile gains a button);
the agent persists it in `/etc/orchestrator/device-profile.json` so the
watcher survives restarts, and watches via the GPIO character device
(go-gpiocdev, pull-up/falling-edge/kernel debounce; the chip is found by the
`pinctrl` label — the header moved chips across Pi generations). Rate limits
live on BOTH ends: the agent's 1/sec is a courtesy, the platform's 1/sec/node
is the guarantee, because the event fans out to every browser of the account.
A press blinks the local LED first — the human at the rack gets feedback even
when the platform is unreachable. The UI toasts and pulses the node row
(`entities/node/buttonPulse.ts`, fed by the user socket).

## Phase 2 remainder (open work)

NeoPixel identify and the LED behaviour/stealth state both SHIPPED — the first
in 0.6.6/0.6.7 (`rp1_ws2812_linux.go` for RP1 PIO on CM5/Pi 5,
`bcm2711_ws2812_linux.go` for the PWM serializer on CM4/Pi 4, chosen by
`OpenNeoPixel`; no cgo, no rpi-ws281x), the second in 0.6.8 as
`apply_led_config` with the tri-state OU/account inheritance this entry asked
for (`organizational_unit.ResolveLEDConfig`). What is genuinely open:

- **Long/double-press button semantics.** Harder than when this was written,
  not easier: `press_kind` was reserved for exactly this, and the LED work
  OCCUPIED it with blink-outcome values (`short`, `blink_started`,
  `blink_stopped`). The field now answers *what the press did*, not *how it was
  pressed*, so press duration has to arrive as a second dimension — a new
  field, or a namespaced value scheme — rather than as one more `press_kind`.
- **Both fan types** — smart-uart5 (RP2040, autonomous regulation, platform
  pushes policy) and pwm (GPIO12/13, agent-local curve from desired state,
  never a platform control loop) — with the **controller/watcher role split**,
  because the physical fan unit is shared between two adjacent blades and
  exactly one of them may own the PWM.
- **The ZYMKEY 4i module** — no registry entry and no agent support today
  (`registry/modules/` holds the DS3231 alone).
- **Apply on save.** An equipage set on an OU RESOLVES for every node under
  it. The unit page's *Apply hardware profile to N nodes*
  (`POST /api/v1/organizational-units/{id}/device-profile/apply`,
  `ApplyDeviceProfileToUnit`) runs the per-node apply over every node
  DIRECTLY in the unit — nested units are not included — queueing it for an
  offline node and listing each node it skips with the refusal. Nothing
  dispatches when the equipage is EDITED, on purpose: hardware setup is the
  last thing to run on a fleet as a side effect of a form save. A nested
  unit's nodes take the apply from their own unit's page or one node at a
  time.

## Validation notes

A failed post-apply boot is attributed from the console FIRST (no default
route + networkd down = the known ~50% netplan race, not the overlay);
"confirmed" on real hardware is a rate over N≥5 reboot–scan cycles, not one
boot; and the blade TPM variant is tested on real hardware before end-to-end
support for it is claimed.


## LED control and the monolith decision

The LED feature (stealth mode, ACT/PWR disable, ambient per-LED indication —
see `agent/pkg/application/handlers/led_manager_linux.go`)
raised the architecture question: on a Pi 4/5 with no digital LEDs, is this
dead code, and should the agent become modular — downloading platform-specific
code when a platform is selected?

**Decision: the agent stays ONE static, signed binary.** The reasoning, so it
is not re-litigated from scratch:

- The whole LED feature is <1% of the binary, and every entry point is lazy:
  `OpenNeoPixel` runs only for boards whose profile declares digital LEDs, the
  indication loop only after an apply the backend already board-gates. On a
  Pi 4/5 the cost is cold, never-paged text — there is nothing to optimize.
- Downloadable modules would reopen exactly the surface the YubiKey signing
  chain exists to close ("fetch and execute code" as a feature), multiply the
  per-release signing cost by the module count, and add a fleet-state axis
  (which nodes hold which module at which version) with its own skipped/floor
  semantics. Go's `plugin` also requires exact toolchain matches and fights
  static linking, so real modularity means separate binaries + IPC.
- The modularity we DO use is build-time and artifact-level: the watcher is
  a separate binary from the same tree (privilege separation, not size), and
  heavy platform-specific payloads belong in **utilities** (containers/debs
  the agent only orchestrates — the kiosk is the template). Thin hardware
  facts (GPIOs, LED counts, stealth transistor) are **registry declarations**;
  agent code stays generic and inert without them.

**Recovery interaction, stated because it is a visible behaviour**: the
`apply_led_config` slug is managed-OS-only, so a netbooted node cannot be
(re)configured — and the recovery initramfs has no state file, so **a blade in
stealth mode LIGHTS UP while netbooted** and goes dark again when its OS
returns and the agent re-applies the persisted config. Identify still works
in recovery (ACT always; digital LEDs where the recovery kernel exposes the
device), which is the desired half: a netbooted node is the one an operator
most often needs to find.

## LED indication v2 (agreed direction, not scheduled)

Two different costs, one plan (0.7.0 material):

1. **Thresholds, brightness, colors, period are parameters, not code.** Each
   digital LED's entry becomes `{source, low, high, brightness}` (platform
   validates, agent renders generically; absent fields fall to today's
   defaults — 45..80°C, 0..100% load, 20% brightness). ONE agent release,
   after which different heatsinks are a UI edit, never a rebuild.
2. **New metric sources need agent code by nature** (the agent must read the
   metric), so v2 ships a small reader registry at once — temperature, load,
   **ram**, **disk** — and the agent advertises its supported sources so the
   UI offers exactly what the node can render and the platform side never
   hand-tracks agent capabilities again.

The wire color order is RGB (the blade's chips, NOT the classic WS2812 GRB —
green rendered red on real blades before 0.6.9; blue and white are
identical in both orders, which is why blue-identify validation missed it).
If a future board carries GRB chips, the order belongs in `IdentifySpec`.
