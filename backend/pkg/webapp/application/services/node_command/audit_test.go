package node_command

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// "Who rebooted my node" was unanswerable before these writers: not one device
// command produced an audit row. RegisterNodeCommandWithWorkflowNode is the
// single choke point every dispatch passes through — HTTP handlers, the workflow
// engine, the cron scheduler and the platform's own self-heal — so the writer
// lives there and these tests pin it there.

// registerRepo accepts creates and remembers them.
type registerRepo struct {
	repository.NodeCommandRepository
	created []node_command.NodeCommand
}

func (r *registerRepo) CreateWithWorkflowNode(_ context.Context, cmd *node_command.NodeCommand, _ *uuid.UUID) error {
	r.created = append(r.created, *cmd)
	return nil
}

func (r *registerRepo) SupersedeRegisteredByKey(context.Context, uuid.UUID, string, uuid.UUID, time.Time) ([]uuid.UUID, error) {
	return nil, nil
}

func (r *registerRepo) HasActiveCommands(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}

// internalAuth mirrors the account-only auth context the platform's own
// dispatch paths pass (node.Service.internalAuth): it has no user, and that is
// exactly what must be recorded as a system actor rather than as somebody.
type internalAuth struct{ accountID uuid.UUID }

func (a internalAuth) GetAccountID() uuid.UUID { return a.accountID }

func registerService() (*Service, *audittest.Sink) {
	recorder, sink := audittest.New()
	return NewService(&registerRepo{}, &stubWebsocket{}, recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil))), sink
}

// The headline case: a reboot dispatched by a person, recorded with the slug in
// the action so the trail is searchable by the question an operator asks.
func TestUserDispatchIsAuditedWithTheSlugInTheAction(t *testing.T) {
	svc, sink := registerService()
	accountID, userID, nodeID := uuid.New(), uuid.New(), uuid.New()

	cmd, err := svc.RegisterNodeCommand(context.Background(), nodeID,
		node_command.SlugReboot, map[string]interface{}{"delay_seconds": 5.0},
		commands.NewAuthContext(accountID, userID))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	event, ok := sink.Find(audit_event.NodeCommandAction(node_command.SlugReboot))
	if !ok {
		t.Fatalf("no audit event for a reboot dispatch; recorded %v", sink.Actions())
	}
	if event.ActorKind != audit_event.ActorUser {
		t.Errorf("actor kind = %q, want a user", event.ActorKind)
	}
	if event.ActorUserID == nil || *event.ActorUserID != userID {
		t.Errorf("actor = %v, want %v", event.ActorUserID, userID)
	}
	if event.AccountID != accountID {
		t.Errorf("account = %v, want %v", event.AccountID, accountID)
	}
	if event.TargetType != audit_event.TargetNode || event.TargetID != nodeID.String() {
		t.Errorf("target = %s/%s, want node/%s", event.TargetType, event.TargetID, nodeID)
	}
	// The command id links the row to the payload and status history.
	if !strings.Contains(event.Detail, cmd.ID.String()) {
		t.Errorf("detail %q does not name the command", event.Detail)
	}
}

// A platform-initiated dispatch carries an auth context with no user. Recording
// it as a user would name whoever last touched the code path.
func TestPlatformDispatchIsRecordedAsTheSystem(t *testing.T) {
	svc, sink := registerService()
	accountID, nodeID := uuid.New(), uuid.New()

	if _, err := svc.RegisterNodeCommand(context.Background(), nodeID,
		node_command.SlugApplyAccessConfig, nil, internalAuth{accountID: accountID}); err != nil {
		t.Fatalf("register: %v", err)
	}

	event, ok := sink.Find(audit_event.NodeCommandAction(node_command.SlugApplyAccessConfig))
	if !ok {
		t.Fatalf("no audit event; recorded %v", sink.Actions())
	}
	if event.ActorKind != audit_event.ActorSystem {
		t.Errorf("actor kind = %q, want the system", event.ActorKind)
	}
	if event.ActorUserID != nil {
		t.Errorf("a system dispatch carries an actor user id: %v", event.ActorUserID)
	}
	if event.AccountID != accountID {
		t.Errorf("account = %v, want %v", event.AccountID, accountID)
	}
}

// The engine passes a real AuthContext with a NIL user id (it runs a pinned
// snapshot; the human who started the run is recorded on the execution). A nil
// user must fall through to the system actor rather than being stored as a
// zero-uuid "user" that resolves to nobody.
func TestEngineDispatchWithANilUserIsRecordedAsTheSystem(t *testing.T) {
	svc, sink := registerService()
	accountID, nodeID, execNodeID := uuid.New(), uuid.New(), uuid.New()

	if _, err := svc.RegisterNodeCommandWithWorkflowNode(context.Background(), nodeID,
		node_command.SlugRunScript, nil,
		commands.NewAuthContext(accountID, uuid.Nil), &execNodeID); err != nil {
		t.Fatalf("register: %v", err)
	}

	event, ok := sink.Find(audit_event.NodeCommandAction(node_command.SlugRunScript))
	if !ok {
		t.Fatalf("no audit event; recorded %v", sink.Actions())
	}
	if event.ActorKind != audit_event.ActorSystem || event.ActorUserID != nil {
		t.Errorf("recorded as %q/%v, want a system actor with no user", event.ActorKind, event.ActorUserID)
	}
	// The run is what links this dispatch back to a human, via the execution's
	// started_by. Without it a scheduled playbook's commands are unattributable.
	if !strings.Contains(event.Detail, execNodeID.String()) {
		t.Errorf("detail %q does not link back to the workflow execution node", event.Detail)
	}
}

// A queued command has been ACCEPTED, not applied, and may land hours later on
// reconnect. The trail has to say so or the row reads as "this happened".
func TestQueuedDispatchIsMarkedAsQueuedInTheDetail(t *testing.T) {
	svc, sink := registerService()
	accountID := uuid.New()

	// apply_access_config is queueable; reboot is not.
	if _, err := svc.RegisterNodeCommand(context.Background(), uuid.New(),
		node_command.SlugApplyAccessConfig, nil,
		commands.NewAuthContext(accountID, uuid.New())); err != nil {
		t.Fatalf("register queueable: %v", err)
	}
	if _, err := svc.RegisterNodeCommand(context.Background(), uuid.New(),
		node_command.SlugReboot, nil,
		commands.NewAuthContext(accountID, uuid.New())); err != nil {
		t.Fatalf("register imperative: %v", err)
	}

	queued, _ := sink.Find(audit_event.NodeCommandAction(node_command.SlugApplyAccessConfig))
	if !strings.Contains(queued.Detail, "queued") {
		t.Errorf("a queueable dispatch was not marked queued: %q", queued.Detail)
	}
	imperative, _ := sink.Find(audit_event.NodeCommandAction(node_command.SlugReboot))
	if strings.Contains(imperative.Detail, "queued") {
		t.Errorf("an immediate dispatch was marked queued: %q", imperative.Detail)
	}
}

// node_commands.payload has its own encv1: encryption because it can carry
// credential material. Copying it into audit_events would put that outside every
// rewrap list, in a table nobody treats as a secret store.
func TestTheCommandPayloadIsNeverCopiedIntoTheTrail(t *testing.T) {
	svc, sink := registerService()
	const password = "s3cret-provisioning-password"

	if _, err := svc.RegisterNodeCommand(context.Background(), uuid.New(),
		node_command.SlugInstallOS,
		map[string]interface{}{"password": password, "username": "orchestrator"},
		commands.NewAuthContext(uuid.New(), uuid.New())); err != nil {
		t.Fatalf("register: %v", err)
	}

	for _, event := range sink.Events() {
		if strings.Contains(event.Detail, password) {
			t.Fatalf("the command payload leaked into audit detail: %q", event.Detail)
		}
	}
}

// Every slug, not a curated "destructive" subset: run_script and write_file are
// as consequential as a reboot, and any subset drifts from its own definition
// as an earlier hand-written list in a doc comment did.
func TestEverySlugIsRecorded(t *testing.T) {
	svc, sink := registerService()
	accountID := uuid.New()
	slugs := []string{
		node_command.SlugReboot,
		node_command.SlugShutdown,
		node_command.SlugInstallOS,
		node_command.SlugRunScript,
		node_command.SlugWriteFile,
		node_command.SlugStatusCheck,
	}
	for _, slug := range slugs {
		if _, err := svc.RegisterNodeCommand(context.Background(), uuid.New(), slug, nil,
			commands.NewAuthContext(accountID, uuid.New())); err != nil {
			t.Fatalf("register %s: %v", slug, err)
		}
	}
	for _, slug := range slugs {
		if _, ok := sink.Find(audit_event.NodeCommandAction(slug)); !ok {
			t.Errorf("%s produced no audit row; recorded %v", slug, sink.Actions())
		}
	}
}

func TestNewServiceRefusesWithoutAnAuditRecorder(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit recorder")
		}
	}()
	NewService(&registerRepo{}, &stubWebsocket{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}
