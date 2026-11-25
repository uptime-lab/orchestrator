# Supported hardware

Uptime Orchestrator supports the Raspberry Pi 4, the Raspberry Pi 5, the
Compute Module 4 and the Compute Module 5, with eMMC or Lite. All are arm64
and join over **Ethernet**: the recovery image and the network boot use the
wired interface, so Wi-Fi is not used to adopt or install a device.

| Board | Status |
|---|---|
| Raspberry Pi 5 | Supported |
| Raspberry Pi 4 | Supported |
| Compute Module 5 (eMMC or Lite) | Supported |
| Compute Module 4 (eMMC or Lite) | Supported |

**Device profiles** (declared carrier-board hardware, `docs/DEVICE-PROFILES.md`)
describe carrier boards, so they apply to the Compute Modules and the Pi 5. A
Raspberry Pi 4 is managed like any other node and takes no device profile.

Two facts worth knowing before buying hardware:

- **Board identity survives netboot only through the OTP revision code.** The
  recovery bootstrap's forced `device_tree=` makes every BCM2712 board call
  itself a CM5 in `/proc/cpuinfo`; the platform decodes the raw revision code
  instead, so the Overview's model field is trustworthy on every listed board.
- **A leftover `SIGNED_BOOT=1` in the EEPROM refuses unsigned SD bundles and
  stock OSes alike**, and the symptom reads as "the payload is broken". Scan
  the EEPROM config before concluding anything about a board that will not
  boot.
