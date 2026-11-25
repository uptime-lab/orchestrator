package results

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// AdoptionRefusalResult is one device this account could not admit because its
// hardware serial is bound to a node in another account.
//
// It carries the serial — the caller is holding that hardware — and nothing
// about the owning account, for the same reason the gRPC refusal the device
// receives is deliberately vague. No field is omitempty: false and zero are
// real answers here, and an absent key is indistinguishable from an older
// backend.
type AdoptionRefusalResult struct {
	HardwareSerial string    `json:"hardware_serial"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	Occurrences    int64     `json:"occurrences"`
	// Live: the device was refused within the last few minutes, so it is
	// powered on and still retrying.
	Live bool `json:"live"`
	// StillBoundElsewhere: the serial is held by another account right now.
	// False means the other side has released it and the device will be
	// admitted on its next retry.
	StillBoundElsewhere bool `json:"still_bound_elsewhere"`
}

// AdoptionRefusalListResult wraps the list.
type AdoptionRefusalListResult struct {
	AdoptionRefusals []AdoptionRefusalResult `json:"adoption_refusals"`
	// LiveWindowSeconds tells the UI how "live" was decided, so the copy can
	// say "in the last N minutes" without hardcoding the server's rule.
	LiveWindowSeconds int `json:"live_window_seconds"`
}

// NewAdoptionRefusalResult projects one refusal.
func NewAdoptionRefusalResult(r node.AdoptionRefusal) AdoptionRefusalResult {
	return AdoptionRefusalResult{
		HardwareSerial:      r.HardwareSerial,
		FirstSeenAt:         r.FirstSeenAt,
		LastSeenAt:          r.LastSeenAt,
		Occurrences:         r.Occurrences,
		Live:                r.Live,
		StillBoundElsewhere: r.StillBoundElsewhere,
	}
}

// NewAdoptionRefusalListResult projects the list; never nil, so an account
// with no refusals serialises as an empty array rather than null.
func NewAdoptionRefusalListResult(refusals []node.AdoptionRefusal) AdoptionRefusalListResult {
	out := make([]AdoptionRefusalResult, 0, len(refusals))
	for _, r := range refusals {
		out = append(out, NewAdoptionRefusalResult(r))
	}
	return AdoptionRefusalListResult{
		AdoptionRefusals:  out,
		LiveWindowSeconds: int(node.AdoptionRefusalLiveWindow.Seconds()),
	}
}
