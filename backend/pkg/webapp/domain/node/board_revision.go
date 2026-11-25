package node

import (
	"strconv"
	"strings"
)

// Board identity decoded from the Raspberry Pi board revision code.
//
// The revision code is written by the firmware from OTP, which is what makes it
// usable where the device-tree model is not: the recovery bootstrap forces one
// device_tree= for all of BCM2712, so a Pi 5 running it reports itself as a
// Compute Module 5 in /proc/cpuinfo's "Model" line. Everything board-specific
// should therefore prefer this and keep the model string only as a fallback for
// agents that predate the field.
//
// The table lives here, on the platform, rather than in the agent: recognising
// a newly released board then costs a backend deploy instead of rolling a new
// agent across a fleet whose agents may be several versions behind.

// BoardKind is a specific Raspberry Pi board.
type BoardKind string

const (
	BoardPi4B    BoardKind = "Pi 4"
	BoardPi400   BoardKind = "Pi 400"
	BoardCM4     BoardKind = "CM4"
	BoardCM4S    BoardKind = "CM4S"
	BoardPi5     BoardKind = "Pi 5"
	BoardCM5     BoardKind = "CM5"
	BoardPi500   BoardKind = "Pi 500"
	BoardCM5Lite BoardKind = "CM5 Lite"
)

// boardTypes maps the revision code's type field (bits 4-11) to a board.
// Types absent from this map are not an error — they decode to an unknown
// board, and callers fall back rather than guess.
var boardTypes = map[uint8]BoardKind{
	0x11: BoardPi4B,
	0x13: BoardPi400,
	0x14: BoardCM4,
	0x15: BoardCM4S,
	0x17: BoardPi5,
	0x18: BoardCM5,
	0x19: BoardPi500,
	0x1a: BoardCM5Lite,
}

// BoardRevision is a decoded new-style Raspberry Pi revision code.
type BoardRevision struct {
	Raw  string
	Kind BoardKind // "" when the type is not in the table
	// SoC is "cm4" for BCM2711 and "cm5" for BCM2712 — the EEPROM firmware
	// family as the signer names it, i.e. the silicon and not the form factor.
	// Empty for any other processor.
	SoC string
}

// ParseBoardRevision decodes a new-style revision code such as "c04170".
//
// The layout NOQuuuWuFMMMCCCCPPPPTTTTTTTTRRRR is valid ONLY when bit 23 (the
// "new flag") is set; an old-style sequential code decodes to nonsense under
// it. Returns ok=false for empty, unparseable or old-style input so callers
// fall back instead of acting on a fabricated board.
func ParseBoardRevision(rev string) (BoardRevision, bool) {
	s := strings.TrimSpace(rev)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return BoardRevision{}, false
	}

	code, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return BoardRevision{}, false
	}
	if code&(1<<23) == 0 {
		return BoardRevision{}, false
	}

	soc := ""
	switch (code >> 12) & 0xf {
	case 3:
		soc = "cm4" // BCM2711
	case 4:
		soc = "cm5" // BCM2712
	}

	return BoardRevision{
		Raw:  rev,
		Kind: boardTypes[uint8((code>>4)&0xff)],
		SoC:  soc,
	}, true
}

// BoardRevision returns this node's decoded board revision, false when the
// agent reported none or it could not be decoded.
func (n *Node) BoardRevision() (BoardRevision, bool) {
	if n.SystemInfo == nil || n.SystemInfo.Hardware == nil || n.SystemInfo.Hardware.CPU == nil {
		return BoardRevision{}, false
	}
	return ParseBoardRevision(n.SystemInfo.Hardware.CPU.BoardRevision)
}

// BoardLabel returns the short board name for display ("Pi 5", "CM5 Lite", …),
// preferring the revision code and falling back to the reported model string
// for agents that do not send one. Empty when neither identifies a board.
func (n *Node) BoardLabel() string {
	if rev, ok := n.BoardRevision(); ok && rev.Kind != "" {
		return string(rev.Kind)
	}
	model := ""
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.CPU != nil {
		model = n.SystemInfo.Hardware.CPU.Model
	}
	return string(BoardKindFromModel(model))
}

// BoardKindFromModel is the pre-revision-code path: classify from the
// device-tree model string. Order matters — "Compute Module 4S" and
// "Compute Module 5 Lite" must be tested before their shorter prefixes.
func BoardKindFromModel(model string) BoardKind {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "compute module 5 lite"):
		return BoardCM5Lite
	case strings.Contains(m, "compute module 5"):
		return BoardCM5
	case strings.Contains(m, "compute module 4s"):
		return BoardCM4S
	case strings.Contains(m, "compute module 4"):
		return BoardCM4
	case strings.Contains(m, "raspberry pi 500"):
		return BoardPi500
	case strings.Contains(m, "raspberry pi 5"):
		return BoardPi5
	case strings.Contains(m, "raspberry pi 400"):
		return BoardPi400
	case strings.Contains(m, "raspberry pi 4"):
		return BoardPi4B
	default:
		return ""
	}
}
