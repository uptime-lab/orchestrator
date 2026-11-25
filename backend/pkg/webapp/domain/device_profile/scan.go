package device_profile

import (
	"strings"
	"time"
)

// The device-profile scan is ONE fixed, side-effect-free script for every node
// and every equipage. That is a security property, not a convenience: the GET
// poll authorizes reading a run_script result by requiring the stored command's
// script to EQUAL this constant (the os-users scan precedent) — a per-equipage
// script could not be verified that way, and re-rendering it at poll time would
// break when the equipage changed between dispatch and poll. Module probes
// therefore select facts FROM this superset dump; they never change what runs.
//
// The path list is a superset on purpose: it covers every probe any current or
// future registry entry is likely to declare (TPM, RTC, HAT). Extending it is
// an additive change — old scan results simply lack the new fact.
const ScanScript = `for p in /dev/tpm0 /dev/tpmrm0 /dev/rtc0 /sys/class/rtc/rtc0 /proc/device-tree/hat; do [ -e "$p" ] && echo "PATH $p"; done; for r in /sys/class/rtc/*/name; do [ -f "$r" ] && echo "RTC $(cat "$r")"; done; for d in /sys/class/rtc/*; do [ -f "$d/hctosys" ] && [ "$(cat "$d/hctosys")" = "1" ] && [ -f "$d/name" ] && echo "CLOCKSRC $(cat "$d/name")"; done; if [ -d /proc/device-tree/hat ]; then for f in vendor product product_id product_ver uuid; do [ -f "/proc/device-tree/hat/$f" ] && echo "HAT $f=$(tr -d '\0' < /proc/device-tree/hat/$f)"; done; fi; true`

// ParseScanOutput turns the script's stdout into a ScanResult. Unknown lines
// are ignored (the shell may emit noise); HAT values are truncated defensively
// — they come from an EEPROM any vendor programs, and this result is stored
// and rendered.
func ParseScanOutput(out string, at time.Time) ScanResult {
	const maxHATValue = 128
	res := ScanResult{At: at}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "PATH "):
			if p := strings.TrimSpace(strings.TrimPrefix(line, "PATH ")); p != "" {
				res.Paths = append(res.Paths, p)
			}
		case strings.HasPrefix(line, "RTC "):
			// The registered RTC driver's name ("ds3231", "rpi-rtc", ...).
			// Needed because Pi 5 / CM5 carry a BUILT-IN RTC: a bare
			// "/sys/class/rtc/rtc0 exists" probe would false-confirm an
			// external DS3231 module on every board of that family.
			if n := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "RTC "))); n != "" {
				res.RTCNames = append(res.RTCNames, n)
			}
		case strings.HasPrefix(line, "CLOCKSRC "):
			// The RTC whose sysfs hctosys flag is 1: the one the kernel took
			// the system clock FROM at boot. Presence on I2C and being the
			// clock source are different facts — a board can carry a second
			// RTC, an overlay can fail to bind, and CONFIG_RTC_HCTOSYS_DEVICE
			// names exactly one — so this is what "the OS is actually using
			// it" means, and it cannot be inferred from the RTC list above.
			if n := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "CLOCKSRC "))); n != "" {
				res.ClockSourceRTC = n
			}
		case strings.HasPrefix(line, "HAT "):
			kv := strings.SplitN(strings.TrimPrefix(line, "HAT "), "=", 2)
			if len(kv) != 2 {
				continue
			}
			key := sanitizeHATValue(kv[0], maxHATValue)
			val := sanitizeHATValue(kv[1], maxHATValue)
			if key != "" && val != "" {
				if res.HAT == nil {
					res.HAT = map[string]string{}
				}
				res.HAT[key] = val
			}
		}
	}
	return res
}

// sanitizeHATValue mirrors node.SanitizeHATField (which this package cannot
// import — domain/node already depends on this one): vendor-programmed EEPROM
// bytes are untrusted, so control characters and ANSI escapes must not reach
// the stored scan or the API. Rune-wise, so the cap cannot split UTF-8.
func sanitizeHATValue(v string, maxLen int) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	for _, r := range v {
		if r >= 32 && r != 127 {
			b.WriteRune(r)
		}
		if b.Len() >= maxLen {
			break
		}
	}
	return b.String()
}
