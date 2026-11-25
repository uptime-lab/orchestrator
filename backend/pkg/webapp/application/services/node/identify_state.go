package node

import (
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The identify/button registry: the platform-side answer to "which node is
// blinking, and whose button was pressed recently" — the state a browser
// opened AFTER the fact could never rebuild from events (the walk-to-the-PC
// case: press the button at the rack, sit down, open the site, find the
// node). Deliberately in-memory: a blink lives seconds-to-minutes and a
// press mark minutes, so a backend restart loses nothing worth persisting.
// identifyPressMarkTTL is how long a press stays findable in the UI after
// the LEDs stop — the whole point of the walk-to-the-PC case. It bounds the
// press MARK, which only a legacy/failed press sets: an agent that reports
// its outcome is found through the blink itself, which carries the account's
// identify duration and its own end time.
const identifyPressMarkTTL = 10 * time.Minute

type identifyEntry struct {
	accountID  uuid.UUID
	blinkUntil time.Time
	pressedAt  time.Time
}

func (s *Service) identifyEntry(accountID, nodeID uuid.UUID) *identifyEntry {
	if s.identifyStates == nil {
		s.identifyStates = map[uuid.UUID]*identifyEntry{}
	}
	e := s.identifyStates[nodeID]
	if e == nil || e.accountID != accountID {
		e = &identifyEntry{accountID: accountID}
		s.identifyStates[nodeID] = e
	}
	return e
}

// recordIdentifyStart notes a dispatched blink; called after the dispatch
// succeeded, so the registry never claims a blink the device refused.
func (s *Service) recordIdentifyStart(accountID, nodeID uuid.UUID, duration time.Duration) {
	s.identifyStateMu.Lock()
	defer s.identifyStateMu.Unlock()
	s.identifyEntry(accountID, nodeID).blinkUntil = time.Now().Add(duration)
}

// recordIdentifyStop clears BOTH halves, because the operator's off-click
// clears both in the browser (nodeCard's handleIdentify calls
// identifyBlinkStopped and buttonMarkCleared together) — the ID lamp is one
// control and "off" means off. Leaving the press mark behind here made the
// lamp come back on the next socket reconnect, which reads as an identify
// that refuses to stop.
func (s *Service) recordIdentifyStop(accountID, nodeID uuid.UUID) {
	s.identifyStateMu.Lock()
	defer s.identifyStateMu.Unlock()
	e := s.identifyEntry(accountID, nodeID)
	e.blinkUntil = time.Time{}
	e.pressedAt = time.Time{}
}

// RecordIdentifyFailed clears a blink the registry claimed at dispatch but
// the device then refused or failed — without it a failed identify leaves a
// phantom lamp in every freshly opened browser for up to the full duration.
// It clears the BLINK only: a failed dispatch says nothing about whether
// somebody pressed the button at the rack, and wiping that mark would erase
// the one signal the presser is walking to a screen to find.
func (s *Service) RecordIdentifyFailed(accountID, nodeID uuid.UUID) {
	s.identifyStateMu.Lock()
	defer s.identifyStateMu.Unlock()
	s.identifyEntry(accountID, nodeID).blinkUntil = time.Time{}
}

// recordButtonOutcome mirrors what the press actually DID on the device (the
// agent reports the outcome kind). The registry must end up holding exactly
// what the browser's own stores hold for the same event, because hydration
// replays it into those stores on every socket connect — a registry that
// records MORE than the live path does not add information, it resurrects
// state the operator already dismissed:
//
//   - blink_started / blink_stopped carry their own state, so the press mark
//     is cleared, never set (buttonPulse.ts does the same: for these kinds it
//     deletes the mark and keeps the blink store authoritative). Recording a
//     mark for the press that STOPPED the blink is what made a stopped
//     identify light up again on the next reconnect.
//   - legacy "short" (an agent that reports no outcome, or a blink that could
//     not start) is the only kind whose whole content is "somebody pressed
//     it", so it is also the only kind that sets the mark — and it TOGGLES,
//     the way the browser toggles it, so a second press clears it for a page
//     opened later too. A short press while a blink is running is the
//     acknowledgement rather than a mark request: it stops the blink and
//     leaves the mark alone.
// It returns the blink end when the press started one, and the mark timestamp
// when it SET a mark. The mark timestamp rides the event so a browser stores
// the registry's own value rather than its local clock: the two then compare
// equal, and dismissing a mark can be keyed BY VALUE instead of by two clocks
// agreeing. Without that, the ID button's local off-click — which deliberately
// dispatches nothing when nothing is blinking — cleared the mark in this
// browser while the registry kept it, and the next socket reconnect hydrated it
// straight back.
func (s *Service) recordButtonOutcome(accountID, nodeID uuid.UUID, kind string, durationSec int) (blinkUntil *time.Time, markedAt *time.Time) {
	s.identifyStateMu.Lock()
	defer s.identifyStateMu.Unlock()
	now := time.Now()
	e := s.identifyEntry(accountID, nodeID)
	switch kind {
	case "blink_started":
		e.pressedAt = time.Time{}
		e.blinkUntil = now.Add(time.Duration(durationSec) * time.Second)
		until := e.blinkUntil
		return &until, nil
	case "blink_stopped":
		e.pressedAt = time.Time{}
		e.blinkUntil = time.Time{}
	case "long", "double":
		// Gesture reports (edge-pair timing, agents past 0.7.0): purely
		// informational. The falling edge of the SAME press already reported
		// its blink outcome, so letting these fall into the legacy default
		// would kill the blink that press just started.
	default:
		if e.blinkUntil.After(now) {
			e.blinkUntil = time.Time{}
			break
		}
		if !e.pressedAt.IsZero() && now.Sub(e.pressedAt) < identifyPressMarkTTL {
			e.pressedAt = time.Time{}
			break
		}
		e.pressedAt = now
		at := e.pressedAt
		return nil, &at
	}
	return nil, nil
}

// IdentifyStates returns the account's live entries, sweeping the dead ones
// while it is here (the map tracks only recently-touched nodes).
func (s *Service) IdentifyStates(accountID uuid.UUID) []node.IdentifyState {
	s.identifyStateMu.Lock()
	defer s.identifyStateMu.Unlock()
	now := time.Now()
	var out []node.IdentifyState
	for nodeID, e := range s.identifyStates {
		blinkLive := e.blinkUntil.After(now)
		pressLive := !e.pressedAt.IsZero() && now.Sub(e.pressedAt) < identifyPressMarkTTL
		if !blinkLive && !pressLive {
			delete(s.identifyStates, nodeID)
			continue
		}
		if e.accountID != accountID {
			continue
		}
		st := node.IdentifyState{NodeID: nodeID}
		if blinkLive {
			until := e.blinkUntil
			st.BlinkUntil = &until
		}
		if pressLive {
			at := e.pressedAt
			st.PressedAt = &at
		}
		out = append(out, st)
	}
	return out
}
