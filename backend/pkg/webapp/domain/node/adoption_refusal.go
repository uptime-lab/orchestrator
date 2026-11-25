package node

import "time"

// AdoptionRefusal is one device this account's install flow could not admit,
// because the hardware serial it presented is bound to a node in another
// account. It is derived at read time from the platform's own refusal record —
// nothing is stored for it beyond that record — so it can never disagree with
// what the guard actually did.
//
// It names a SERIAL binding, never a device movement or an owner: the serial can
// have been bound by an account that never held the hardware, and the owning
// account is the one fact the refusal deliberately withholds from the tenant.
type AdoptionRefusal struct {
	HardwareSerial string
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
	// Occurrences counts the device's retries folded into the record.
	Occurrences int64
	// Live says the device was refused within AdoptionRefusalLiveWindow of now:
	// it is powered on and still trying. A refused agent retries on a bounded
	// backoff (the agent's DefaultMaxInterval is 30 s), so a record older than
	// the window describes a device that has since been switched off, moved,
	// or admitted.
	Live bool
	// StillBoundElsewhere says the serial is, right now, held by another
	// account and not by this one — the condition the device keeps hitting.
	// False with Live means the other account has released it and the device
	// should be admitted on its next retry; the record is then history.
	StillBoundElsewhere bool
}

// AdoptionRefusalLiveWindow bounds how old a refusal may be and still describe
// a device that is currently retrying. Ten times the agent's maximum reconnect
// interval, so one slow retry does not flip a live refusal to stale.
const AdoptionRefusalLiveWindow = 5 * time.Minute

// AdoptionRefusalIsLive is the one place the window is applied.
func AdoptionRefusalIsLive(lastSeen, now time.Time) bool {
	return !lastSeen.IsZero() && now.Sub(lastSeen) < AdoptionRefusalLiveWindow
}
