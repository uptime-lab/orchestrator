package node_command

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	websockets2 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// A command frame is a third read path to a run's commands, after the run view
// and the node's command reads: a command those withhold from a member without
// secret.read_value may reach only holders of that action over the socket,
// decided by the same filter the REST reads apply.

type recordingWebsocket struct{ sent []websockets2.Message }

func (r *recordingWebsocket) Send(m websockets2.Message) error {
	r.sent = append(r.sent, m)
	return nil
}
func (r *recordingWebsocket) GetChannel() chan websockets2.Message { return nil }

// fakeRunFilter hides the commands in hidden (every command with hideAll), or
// fails.
type fakeRunFilter struct {
	hidden  map[uuid.UUID]bool
	hideAll bool
	err     error
	calls   int
}

func (f *fakeRunFilter) WithoutHiddenRunCommands(_ context.Context, _ uuid.UUID, cmds []node_command.NodeCommand) ([]node_command.NodeCommand, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []node_command.NodeCommand
	for _, c := range cmds {
		if !f.hideAll && !f.hidden[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

// onlySecretReaders reports whether the message reaches secret.read_value
// holders alone: everyone else is sent the redacted variant, which is nothing.
func onlySecretReaders(m websockets2.Message) bool {
	return m.FullPayloadAction == authz.ActionSecretReadValue && m.RedactedPayload == nil
}

func frameService(filter RunCommandFilter) (*Service, *recordingWebsocket) {
	ws := &recordingWebsocket{}
	recorder, _ := audittest.New()
	svc := NewService(&registerRepo{}, ws, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if filter != nil {
		svc.SetRunCommandFilter(filter)
	}
	return svc, ws
}

func TestCommandFramesFollowTheRunCommandFilter(t *testing.T) {
	stepID := uuid.New()
	cases := []struct {
		name       string
		runCommand bool
		filter     func(cmdID uuid.UUID) *fakeRunFilter // nil: unwired
		restricted bool
		lookups    int
	}{
		{"a direct command needs no lookup", false, func(uuid.UUID) *fakeRunFilter { return &fakeRunFilter{} }, false, 0},
		{"a visible run command", true, func(uuid.UUID) *fakeRunFilter { return &fakeRunFilter{} }, false, 1},
		{"a hidden run command", true, func(id uuid.UUID) *fakeRunFilter { return &fakeRunFilter{hidden: map[uuid.UUID]bool{id: true}} }, true, 1},
		{"the filter fails", true, func(uuid.UUID) *fakeRunFilter { return &fakeRunFilter{err: errors.New("db down")} }, true, 1},
		{"the filter is unwired", true, nil, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &node_command.NodeCommand{ID: uuid.New(), NodeID: uuid.New(), AccountID: uuid.New(), Slug: node_command.SlugReboot}
			if tc.runCommand {
				cmd.WorkflowExecutionNodeID = &stepID
			}
			var filter *fakeRunFilter
			var svc *Service
			var ws *recordingWebsocket
			if tc.filter != nil {
				filter = tc.filter(cmd.ID)
				svc, ws = frameService(filter)
			} else {
				svc, ws = frameService(nil)
			}

			svc.notifyNodeCommandUpdated(cmd)

			if len(ws.sent) != 1 {
				t.Fatalf("sent %d frames, want 1", len(ws.sent))
			}
			msg := ws.sent[0]
			if got := onlySecretReaders(msg); got != tc.restricted {
				t.Fatalf("frame restricted to secret.read_value holders = %v, want %v (full action %q, redacted %v)",
					got, tc.restricted, msg.FullPayloadAction, msg.RedactedPayload)
			}
			if !tc.restricted && msg.FullPayloadAction != "" {
				t.Fatalf("a visible command's frame is split by %q", msg.FullPayloadAction)
			}
			if filter != nil && filter.calls != tc.lookups {
				t.Fatalf("filter asked %d times, want %d", filter.calls, tc.lookups)
			}
		})
	}
}

// The created frame of a run's command is routed by the run link the dispatch
// passed in, before any read of the row.
func TestCreatedFrameOfAHiddenRunCommandReachesOnlySecretReaders(t *testing.T) {
	filter := &fakeRunFilter{hideAll: true}
	svc, ws := frameService(filter)
	stepID := uuid.New()

	if _, err := svc.RegisterNodeCommandWithWorkflowNode(context.Background(), uuid.New(), node_command.SlugReboot,
		map[string]interface{}{}, commands.NewAuthContext(uuid.New(), uuid.Nil), &stepID); err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(ws.sent) != 1 {
		t.Fatalf("sent %d frames, want 1", len(ws.sent))
	}
	if !onlySecretReaders(ws.sent[0]) {
		t.Fatal("the created frame of a hidden run command reaches members without secret.read_value")
	}
	if filter.calls != 1 {
		t.Fatalf("filter asked %d times, want 1", filter.calls)
	}
}
