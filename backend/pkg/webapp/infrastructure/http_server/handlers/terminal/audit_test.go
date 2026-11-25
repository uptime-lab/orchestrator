package terminal

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// "Every session open/close is audit-logged" must mean the audit trail, not a
// `logger.Info` line, which rotates out of the container log. An interactive
// root shell on managed hardware — the most privileged capability in the
// product — must not be the one action with no durable record.

func auditHandler() (*Handler, *audittest.Sink) {
	recorder, sink := audittest.New()
	return &Handler{
		audit:  recorder,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, sink
}

// Both rows, in order, joined by the session id.
func TestATerminalSessionRecordsBothOpenAndClose(t *testing.T) {
	h, sink := auditHandler()
	accountID, userID := uuid.New(), uuid.New()
	nodeID := uuid.NewString()
	const sessionID = "sess-7f3a"

	closeSession := h.auditSession(context.Background(),
		audit_service.UserActor(accountID, userID), nodeID, sessionID)

	// After the open, exactly one row — the close must not be written early.
	if actions := sink.Actions(); len(actions) != 1 || actions[0] != audit_event.ActionNodeTerminalOpen {
		t.Fatalf("after opening, recorded %v", actions)
	}

	closeSession()

	actions := sink.Actions()
	if len(actions) != 2 {
		t.Fatalf("recorded %d rows for one session: %v", len(actions), actions)
	}
	if actions[0] != audit_event.ActionNodeTerminalOpen || actions[1] != audit_event.ActionNodeTerminalClose {
		t.Errorf("rows out of order: %v", actions)
	}
	for _, event := range sink.Events() {
		if event.TargetType != audit_event.TargetNode || event.TargetID != nodeID {
			t.Errorf("%s target = %s/%s, want node/%s", event.Action, event.TargetType, event.TargetID, nodeID)
		}
		if event.ActorKind != audit_event.ActorUser {
			t.Errorf("%s recorded as %q, want a user", event.Action, event.ActorKind)
		}
		if event.ActorUserID == nil || *event.ActorUserID != userID {
			t.Errorf("%s actor = %v, want %v", event.Action, event.ActorUserID, userID)
		}
		if event.AccountID != accountID {
			t.Errorf("%s account = %v, want %v", event.Action, event.AccountID, accountID)
		}
		// Without a shared session id the two rows cannot be paired, and "how long
		// was the shell open" stops being answerable.
		if !strings.Contains(event.Detail, sessionID) {
			t.Errorf("%s detail %q does not carry the session id", event.Action, event.Detail)
		}
	}
}

// The trap: the close row runs from a defer as the WebSocket tears down, by
// which point the request context is already cancelled. Writing it on that
// context would fail every single time, leaving a trail of opens that never end
// — which reads as "somebody still has a root shell on this device".
func TestTheCloseRowSurvivesTheRequestContextBeingCancelled(t *testing.T) {
	h, sink := auditHandler()
	nodeID := uuid.NewString()

	requestCtx, cancel := context.WithCancel(context.Background())
	closeSession := h.auditSession(requestCtx,
		audit_service.UserActor(uuid.New(), uuid.New()), nodeID, "sess-1")
	cancel() // the handler has returned and the socket is gone
	closeSession()

	if _, ok := sink.Find(audit_event.ActionNodeTerminalClose); !ok {
		t.Fatalf("the close row was not written; recorded %v", sink.Actions())
	}
}

func TestNewHandlerRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewHandler(nil, nil, nil, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
