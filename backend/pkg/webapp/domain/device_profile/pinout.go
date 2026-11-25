package device_profile

// The standard Raspberry Pi 40-pin header, as pin functions per PHYSICAL pin
// number. Ports describe themselves as contiguous subsets of this table, and a
// module states the functions it needs — compatibility is then computed, never
// enumerated: a module that fits in pins 1..10 automatically works on both the
// blade's 10-pin header and any full 40-pin board.
//
// Function tags, not a full pinmux model: "i2c1" appears on both of its pins,
// power rails are plain "3v3"/"5v"/"gnd", and every BCM GPIO carries its
// "gpioN" tag so a module can claim a single line. Alternate functions beyond
// what modules actually declare are deliberately absent — add a tag when a
// module needs it, not ahead of time.

// PinoutRPi40 is the only pinout defined today.
const PinoutRPi40 = "rpi-40pin"

// rpi40Pinout maps physical pin -> function tags.
var rpi40Pinout = map[int][]string{
	1:  {"3v3"},
	2:  {"5v"},
	3:  {"gpio2", "i2c1"},
	4:  {"5v"},
	5:  {"gpio3", "i2c1"},
	6:  {"gnd"},
	7:  {"gpio4"},
	8:  {"gpio14", "uart0"},
	9:  {"gnd"},
	10: {"gpio15", "uart0"},
	11: {"gpio17"},
	12: {"gpio18", "pwm0"},
	13: {"gpio27"},
	14: {"gnd"},
	15: {"gpio22"},
	16: {"gpio23"},
	17: {"3v3"},
	18: {"gpio24"},
	19: {"gpio10", "spi0"},
	20: {"gnd"},
	21: {"gpio9", "spi0"},
	22: {"gpio25"},
	23: {"gpio11", "spi0"},
	24: {"gpio8", "spi0"},
	25: {"gnd"},
	26: {"gpio7", "spi0"},
	27: {"gpio0", "i2c0"},
	28: {"gpio1", "i2c0"},
	29: {"gpio5"},
	30: {"gnd"},
	31: {"gpio6"},
	32: {"gpio12", "pwm0"},
	33: {"gpio13", "pwm1"},
	34: {"gnd"},
	35: {"gpio19"},
	36: {"gpio16"},
	37: {"gpio26"},
	38: {"gpio20"},
	39: {"gnd"},
	40: {"gpio21"},
}

// shareableFunctions are functions several modules may consume at once.
// I2C is a shared bus by design (conflict is per-address, checked separately);
// power and ground are rails. Everything else — a GPIO line, a UART, SPI chip
// selects — is exclusive to one consumer.
var shareableFunctions = map[string]bool{
	"i2c0": true,
	"i2c1": true,
	"3v3":  true,
	"5v":   true,
	"gnd":  true,
}

// portFunctions returns the set of functions available on a port. Unknown
// pinout names return an empty set (validation refuses them earlier).
func portFunctions(p Port) map[string]bool {
	if p.Pinout != PinoutRPi40 {
		return map[string]bool{}
	}
	fns := make(map[string]bool)
	for pin := p.PinFrom; pin <= p.PinTo; pin++ {
		for _, fn := range rpi40Pinout[pin] {
			fns[fn] = true
		}
	}
	return fns
}

// ModuleFitsPort reports whether the port carries every function the module
// needs, naming the first missing one.
func ModuleFitsPort(m ModuleDef, p Port) (missing string, ok bool) {
	fns := portFunctions(p)
	for _, need := range m.Needs {
		if !fns[need] {
			return need, false
		}
	}
	return "", true
}
