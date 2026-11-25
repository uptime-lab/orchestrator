package websockets

import (
	"log/slog"
	"testing"
	"time"

	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
)

// Send must never block. It is called serially from the hub's single fan-out
// goroutine, so one client that stops reading used to stall delivery for every
// other client — and then back-pressure into the shared channel whose senders
// include HTTP handlers, the gRPC metrics path and the workflow engine.

func TestSendDoesNotBlockWhenTheClientStopsReading(t *testing.T) {
	c := &Client{
		ClientID: websockets3.NewClientID(),
		// Deliberately tiny: the real buffer is 1000, and filling it in a test
		// would only prove the same thing more slowly.
		toClient: make(chan websockets3.MessagePayload, 2),
		logger:   slog.Default(),
	}

	// Nothing drains toClient — this is the stalled-browser case.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			c.Send(websockets3.NewMessageError("x"))
		}
	}()

	select {
	case <-done:
		// Returned without a reader: correct.
	case <-time.After(2 * time.Second):
		t.Fatal("Send blocked on a client that is not reading — one slow browser stalls the whole hub")
	}
}

func TestOverflowIsReportedOnceNotPerMessage(t *testing.T) {
	c := &Client{
		ClientID: websockets3.NewClientID(),
		toClient: make(chan websockets3.MessagePayload, 1),
		logger:   slog.Default(),
	}

	for i := 0; i < 20; i++ {
		c.Send(websockets3.NewMessageError("x"))
	}

	if !c.dropped.Load() {
		t.Fatal("an overflowing client must be marked dropped")
	}
	// The guard exists so a burst does not log and close twice; CompareAndSwap
	// having already flipped is what proves it.
	if c.dropped.CompareAndSwap(false, true) {
		t.Fatal("dropped was reset, so the close path could run more than once")
	}
}

func TestBufferedMessagesStillArriveWhileThereIsRoom(t *testing.T) {
	c := &Client{
		ClientID: websockets3.NewClientID(),
		toClient: make(chan websockets3.MessagePayload, 4),
		logger:   slog.Default(),
	}

	for i := 0; i < 3; i++ {
		c.Send(websockets3.NewMessageError("x"))
	}
	if got := len(c.toClient); got != 3 {
		t.Fatalf("queued %d of 3 — dropping must only happen when the buffer is full", got)
	}
	if c.dropped.Load() {
		t.Fatal("a client inside its buffer must not be dropped")
	}
}

func TestAuthorizedFlagStartsFalseAndLatches(t *testing.T) {
	c := &Client{ClientID: websockets3.NewClientID(), logger: slog.Default()}

	// The pre-auth reaper closes a socket only while this is false; if it
	// started true, an unauthenticated peer would live forever.
	if c.Authorized() {
		t.Fatal("a fresh client must not report itself authorized")
	}
	c.MarkAuthorized()
	if !c.Authorized() {
		t.Fatal("MarkAuthorized must latch, or the reaper closes a working connection")
	}
}
