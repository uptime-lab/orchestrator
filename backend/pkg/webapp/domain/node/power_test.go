package node

import "testing"

func TestNewPowerInfoDecodesTheMeasuredMask(t *testing.T) {
	// 0x50000 is the reading taken on a boot where a USB disk wedged:
	// under-voltage HAS occurred (bit 16) and throttling HAS occurred
	// (bit 18), with nothing live. The live half read 0x0 at the time even
	// though the node had already browned out, which is why the sticky half is the
	// one that explains a failure.
	p := NewPowerInfo(0x50000)

	if p.ThrottledFlags != 0x50000 {
		t.Fatalf("raw mask = 0x%x, want 0x50000", p.ThrottledFlags)
	}
	if !p.UnderVoltageSinceBoot {
		t.Error("under-voltage since boot should be set")
	}
	if !p.ThrottledSinceBoot {
		t.Error("throttled since boot should be set")
	}
	if p.UnderVoltageNow || p.ThrottledNow || p.FreqCappedNow || p.SoftTempLimitNow {
		t.Error("no live bit is set in 0x50000")
	}
	if p.FreqCappedSinceBoot || p.SoftTempLimitSinceBoot {
		t.Error("0x50000 sets neither freq-capped nor soft-temp-limit since boot")
	}
	if !p.HasPowerProblem() {
		t.Error("HasPowerProblem must be true: this is exactly the case it exists for")
	}
}

func TestNewPowerInfoCleanReading(t *testing.T) {
	// A clean reading is a real reading, and must not read as "unknown".
	p := NewPowerInfo(0)
	if p == nil {
		t.Fatal("a zero mask must still produce facts; absence is a nil *PowerInfo at the call site")
	}
	if p.HasPowerProblem() || p.HasThermalProblem() {
		t.Error("0x0 means the SoC reports nothing wrong")
	}
}

func TestNewPowerInfoEachBit(t *testing.T) {
	cases := []struct {
		mask uint32
		get  func(*PowerInfo) bool
		name string
	}{
		{0x1, func(p *PowerInfo) bool { return p.UnderVoltageNow }, "under-voltage now"},
		{0x2, func(p *PowerInfo) bool { return p.FreqCappedNow }, "freq capped now"},
		{0x4, func(p *PowerInfo) bool { return p.ThrottledNow }, "throttled now"},
		{0x8, func(p *PowerInfo) bool { return p.SoftTempLimitNow }, "soft temp limit now"},
		{0x10000, func(p *PowerInfo) bool { return p.UnderVoltageSinceBoot }, "under-voltage since boot"},
		{0x20000, func(p *PowerInfo) bool { return p.FreqCappedSinceBoot }, "freq capped since boot"},
		{0x40000, func(p *PowerInfo) bool { return p.ThrottledSinceBoot }, "throttled since boot"},
		{0x80000, func(p *PowerInfo) bool { return p.SoftTempLimitSinceBoot }, "soft temp limit since boot"},
	}

	for _, tc := range cases {
		p := NewPowerInfo(tc.mask)
		if !tc.get(p) {
			t.Errorf("mask 0x%x should set %s", tc.mask, tc.name)
		}
		// Exactly one flag per bit: a mask must not light up a neighbour.
		set := 0
		for _, other := range cases {
			if other.get(p) {
				set++
			}
		}
		if set != 1 {
			t.Errorf("mask 0x%x set %d flags, want exactly 1", tc.mask, set)
		}
	}
}

func TestPowerAndThermalStaySeparate(t *testing.T) {
	// The split is the design: bits 0/16 mean the supply or the cable, bits 3/19
	// mean cooling, and one "throttled" badge would destroy the difference
	// between buying a power supply and buying a heatsink.
	supply := NewPowerInfo(0x10000)
	if !supply.HasPowerProblem() {
		t.Error("under-voltage since boot is a power problem")
	}
	if supply.HasThermalProblem() {
		t.Error("under-voltage alone must not read as a thermal problem")
	}

	heat := NewPowerInfo(0x80000)
	if heat.HasPowerProblem() {
		t.Error("a soft temperature limit is not a power problem")
	}
	if !heat.HasThermalProblem() {
		t.Error("a soft temperature limit is a thermal problem")
	}

	// The generic "currently throttled" bit is attributed to thermal, but it is
	// set alongside the specific bit in the voltage case, so this cannot hide a
	// power problem.
	both := NewPowerInfo(0x1 | 0x4)
	if !both.HasPowerProblem() {
		t.Error("the specific under-voltage bit must still surface")
	}
}

func TestPowerFactsIsNilSafe(t *testing.T) {
	var nilNode *Node
	if nilNode.PowerFacts() != nil {
		t.Error("a nil node has no power facts")
	}

	// A node that has never reported, and one whose report predates agent 0.5.4:
	// both must be nil rather than a fabricated clean reading, or the platform
	// claims the supply is fine on hardware that never said so.
	n := &Node{}
	if n.PowerFacts() != nil {
		t.Error("a node with no system info has no power facts")
	}
	n.SystemInfo = &SystemInfo{Hardware: &HardwareInfo{}}
	if n.PowerFacts() != nil {
		t.Error("hardware info without power must stay unknown, not clean")
	}

	n.SystemInfo.Hardware.Power = NewPowerInfo(0x10000)
	if !n.PowerFacts().HasPowerProblem() {
		t.Error("PowerFacts should return what the node reported")
	}

	// And the predicates on a nil receiver never panic — every UI/log caller
	// reaches them through PowerFacts(), which can be nil.
	var absent *PowerInfo
	if absent.HasPowerProblem() || absent.HasThermalProblem() {
		t.Error("absent facts assert nothing")
	}
}
