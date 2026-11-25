package node

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// The registry exists to be REPLAYED into the browser's stores on every socket
// connect, so anything it records that the live WebSocket path does not is a
// resurrection of state the operator already dismissed. These tests pin the
// two halves to the same table (frontend/src/entities/node/buttonPulse.ts):
//
//	kind          | blink            | press mark
//	blink_started | until now+d      | cleared
//	blink_stopped | cleared          | cleared
//	short (blink) | cleared          | untouched
//	short (idle)  | untouched        | toggled

func state(t *testing.T, s *Service, accountID, nodeID uuid.UUID) (blinking, marked bool) {
	t.Helper()
	for _, st := range s.IdentifyStates(accountID) {
		if st.NodeID == nodeID {
			return st.BlinkUntil != nil, st.PressedAt != nil
		}
	}
	return false, false
}

func TestButtonStopLeavesNothingToHydrate(t *testing.T) {
	s := &Service{}
	account, node := uuid.New(), uuid.New()

	if until, _ := s.recordButtonOutcome(account, node, "blink_started", 300); until == nil {
		t.Fatal("a started blink must report when it ends")
	}
	if blinking, marked := state(t, s, account, node); !blinking || marked {
		t.Fatalf("after the starting press: blinking=%v marked=%v, want true/false", blinking, marked)
	}

	// The press that stopped the blink used to leave a 10-minute press mark
	// behind, which the next reconnect hydrated into a pulsing ID lamp with
	// nothing running on the device — an identify that would not stop.
	if until, _ := s.recordButtonOutcome(account, node, "blink_stopped", 300); until != nil {
		t.Fatal("a stopping press must not report a blink end")
	}
	if blinking, marked := state(t, s, account, node); blinking || marked {
		t.Fatalf("after the stopping press: blinking=%v marked=%v, want false/false", blinking, marked)
	}
}

func TestIdentifyStopClearsTheMarkToo(t *testing.T) {
	s := &Service{}
	account, node := uuid.New(), uuid.New()

	// A legacy press marks the node, then the operator clicks the ID lamp off.
	s.recordButtonOutcome(account, node, "short", 300)
	s.recordIdentifyStart(account, node, 300*time.Second)
	s.recordIdentifyStop(account, node)

	if blinking, marked := state(t, s, account, node); blinking || marked {
		t.Fatalf("after the off-click: blinking=%v marked=%v, want false/false", blinking, marked)
	}
}

func TestFailedIdentifyKeepsThePressMark(t *testing.T) {
	s := &Service{}
	account, node := uuid.New(), uuid.New()

	s.recordButtonOutcome(account, node, "short", 300)
	s.recordIdentifyStart(account, node, 300*time.Second)
	// The device refused the dispatched blink; that says nothing about the
	// human who pressed the button and is walking to a screen to find it.
	s.RecordIdentifyFailed(account, node)

	blinking, marked := state(t, s, account, node)
	if blinking {
		t.Fatal("a failed identify must not leave a phantom blink")
	}
	if !marked {
		t.Fatal("a failed identify must not erase the press mark")
	}
}

func TestLegacyPressTogglesTheMark(t *testing.T) {
	s := &Service{}
	account, node := uuid.New(), uuid.New()

	s.recordButtonOutcome(account, node, "short", 300)
	if _, marked := state(t, s, account, node); !marked {
		t.Fatal("the first legacy press must mark the node")
	}
	// The browser toggles the mark off on the second press; a page opened
	// later must not hydrate it back on.
	s.recordButtonOutcome(account, node, "short", 300)
	if _, marked := state(t, s, account, node); marked {
		t.Fatal("the second legacy press must clear the mark")
	}
}

func TestLegacyPressDuringABlinkIsAnAcknowledgement(t *testing.T) {
	s := &Service{}
	account, node := uuid.New(), uuid.New()

	s.recordIdentifyStart(account, node, 300*time.Second)
	s.recordButtonOutcome(account, node, "short", 300)

	blinking, marked := state(t, s, account, node)
	if blinking {
		t.Fatal("a press on the blinking machine ends the blink")
	}
	if marked {
		t.Fatal("an acknowledgement is not a mark request")
	}
}

func TestIdentifyStatesAreAccountScoped(t *testing.T) {
	s := &Service{}
	mine, theirs, node := uuid.New(), uuid.New(), uuid.New()

	s.recordIdentifyStart(mine, node, 300*time.Second)
	if got := len(s.IdentifyStates(theirs)); got != 0 {
		t.Fatalf("another account sees %d entries, want 0", got)
	}
	if got := len(s.IdentifyStates(mine)); got != 1 {
		t.Fatalf("the owning account sees %d entries, want 1", got)
	}
}
