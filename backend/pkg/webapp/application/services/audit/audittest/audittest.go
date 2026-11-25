// Package audittest builds a real audit.Recorder over an in-memory sink, so a
// service test can assert what the audit trail recorded.
//
// A real Recorder rather than a fake: the mapping from an Actor to a user-vs-
// system event, the detail truncation and the account guard all live in the
// Recorder, and a stubbed one would let a writer pass its test while recording
// the wrong thing.
package audittest

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// Sink captures the events a Recorder wrote. Safe for concurrent writes because
// several writers dispatch off the request goroutine.
type Sink struct {
	repository.AuditEventRepository

	mu     sync.Mutex
	events []audit_event.AuditEvent
}

// Create records the event.
func (s *Sink) Create(_ context.Context, event *audit_event.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, *event)
	return nil
}

// Events returns a copy of everything recorded, in write order.
func (s *Sink) Events() []audit_event.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit_event.AuditEvent(nil), s.events...)
}

// Actions returns just the action strings, which is what most assertions need.
func (s *Sink) Actions() []string {
	events := s.Events()
	actions := make([]string, 0, len(events))
	for i := range events {
		actions = append(actions, events[i].Action)
	}
	return actions
}

// Find returns the first event with the given action, and whether one exists.
func (s *Sink) Find(action string) (audit_event.AuditEvent, bool) {
	for _, event := range s.Events() {
		if event.Action == action {
			return event, true
		}
	}
	return audit_event.AuditEvent{}, false
}

// New returns a Recorder writing into a fresh Sink.
func New() (*audit.Recorder, *Sink) {
	sink := &Sink{}
	return audit.NewRecorder(sink, slog.New(slog.NewTextHandler(io.Discard, nil))), sink
}
