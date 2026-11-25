package node

// Raspberry Pi `vcgencmd get_throttled` bit assignments. Bits 0-3 report the
// state RIGHT NOW; bits 16-19 report that the same condition HAS OCCURRED since
// boot and are sticky until the next reboot.
const (
	throttledBitUnderVoltageNow  = 1 << 0
	throttledBitFreqCappedNow    = 1 << 1
	throttledBitThrottledNow     = 1 << 2
	throttledBitSoftTempLimitNow = 1 << 3

	throttledBitUnderVoltageSinceBoot  = 1 << 16
	throttledBitFreqCappedSinceBoot    = 1 << 17
	throttledBitThrottledSinceBoot     = 1 << 18
	throttledBitSoftTempLimitSinceBoot = 1 << 19
)

// PowerInfo is the decoded `vcgencmd get_throttled` bitmask: what the SoC is
// doing about power and heat right now, and what it has done since boot.
//
// The agent sends the raw mask and this decodes it, the same division as
// CPUInfo.BoardRevision — the bit assignments belong to Raspberry Pi's firmware,
// so a new bit becomes readable with a backend deploy rather than a fleet-wide
// agent roll.
//
// Two collapses are deliberately NOT made, because each destroys the operator
// action the facts exist to inform:
//
//   - Live and sticky stay separate. A node that browned out once under a heavy
//     install would otherwise carry a red badge until it reboots, and a signal
//     that is always lit stops being a signal.
//   - Power and thermal stay separate. Bits 0/16 mean the supply or the cable;
//     bits 3/19 mean cooling. One "throttled" badge destroys the difference
//     between buying a power supply and buying a heatsink.
//
// What it never reports is WHY the voltage sagged — PSU, cable, or a USB device's
// draw. The value is that suspicion leaves the disk immediately: a node fed
// from a computer's USB port can brown out until its USB disk stops answering,
// and without this the only thing the platform shows is a wipefs error naming a
// disk that is not faulty.
type PowerInfo struct {
	// ThrottledFlags is the raw mask, kept so a bit this build does not decode is
	// still visible to an operator and to support.
	ThrottledFlags uint32 `json:"throttled_flags"`

	// Right now.
	UnderVoltageNow  bool `json:"under_voltage_now,omitempty"`
	FreqCappedNow    bool `json:"freq_capped_now,omitempty"`
	ThrottledNow     bool `json:"throttled_now,omitempty"`
	SoftTempLimitNow bool `json:"soft_temp_limit_now,omitempty"`

	// Since boot — sticky until reboot, and the more valuable half: it explains a
	// failure after the fact, which is when somebody is actually looking.
	UnderVoltageSinceBoot  bool `json:"under_voltage_since_boot,omitempty"`
	FreqCappedSinceBoot    bool `json:"freq_capped_since_boot,omitempty"`
	ThrottledSinceBoot     bool `json:"throttled_since_boot,omitempty"`
	SoftTempLimitSinceBoot bool `json:"soft_temp_limit_since_boot,omitempty"`
}

// NewPowerInfo decodes a raw `vcgencmd get_throttled` mask.
//
// Note a zero mask is a real, useful reading ("the SoC reports nothing wrong"),
// which is why absence is expressed by a nil *PowerInfo at the call site and
// never by this returning nil.
func NewPowerInfo(flags uint32) *PowerInfo {
	return &PowerInfo{
		ThrottledFlags: flags,

		UnderVoltageNow:  flags&throttledBitUnderVoltageNow != 0,
		FreqCappedNow:    flags&throttledBitFreqCappedNow != 0,
		ThrottledNow:     flags&throttledBitThrottledNow != 0,
		SoftTempLimitNow: flags&throttledBitSoftTempLimitNow != 0,

		UnderVoltageSinceBoot:  flags&throttledBitUnderVoltageSinceBoot != 0,
		FreqCappedSinceBoot:    flags&throttledBitFreqCappedSinceBoot != 0,
		ThrottledSinceBoot:     flags&throttledBitThrottledSinceBoot != 0,
		SoftTempLimitSinceBoot: flags&throttledBitSoftTempLimitSinceBoot != 0,
	}
}

// PowerFacts returns the node's last reported power/throttling facts, or nil
// when the device has never reported any. Nil-safe at every level so callers do
// not repeat the three-deep SystemInfo walk.
func (n *Node) PowerFacts() *PowerInfo {
	if n == nil || n.SystemInfo == nil || n.SystemInfo.Hardware == nil {
		return nil
	}
	return n.SystemInfo.Hardware.Power
}

// HasPowerProblem reports whether the supply is implicated, now or since boot.
// Kept separate from HasThermalProblem on purpose — see the type comment.
func (p *PowerInfo) HasPowerProblem() bool {
	if p == nil {
		return false
	}
	return p.UnderVoltageNow || p.UnderVoltageSinceBoot
}

// HasThermalProblem reports whether cooling is implicated, now or since boot.
// The soft temperature limit and the generic "currently throttled" bit both land
// here: the generic bit is set by heat as well as by voltage, and it is set
// alongside the specific bit in the voltage case, so attributing it to thermal
// cannot hide a power problem.
func (p *PowerInfo) HasThermalProblem() bool {
	if p == nil {
		return false
	}
	return p.SoftTempLimitNow || p.SoftTempLimitSinceBoot ||
		p.ThrottledNow || p.ThrottledSinceBoot
}
