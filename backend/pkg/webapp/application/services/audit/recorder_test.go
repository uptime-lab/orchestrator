package audit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// fakeRepo captures writes and can be made to fail.
type fakeRepo struct {
	repository.AuditEventRepository
	events []audit_event.AuditEvent
	err    error
}

func (f *fakeRepo) Create(_ context.Context, event *audit_event.AuditEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, *event)
	return nil
}

func newTestRecorder() (*Recorder, *fakeRepo) {
	repo := &fakeRepo{}
	return NewRecorder(repo, slog.New(slog.NewTextHandler(io.Discard, nil))), repo
}

func TestRecordStoresUserActor(t *testing.T) {
	rec, repo := newTestRecorder()
	accountID, userID, targetID := uuid.New(), uuid.New(), uuid.New()

	rec.Record(context.Background(), UserActor(accountID, userID),
		audit_event.ActionAccessEntryDelete, audit_event.TargetAccessEntry, targetID.String())

	if len(repo.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(repo.events))
	}
	got := repo.events[0]
	if got.ActorKind != audit_event.ActorUser {
		t.Errorf("actor kind = %q, want %q", got.ActorKind, audit_event.ActorUser)
	}
	if got.ActorUserID == nil || *got.ActorUserID != userID {
		t.Errorf("actor user id = %v, want %v", got.ActorUserID, userID)
	}
	if got.AccountID != accountID {
		t.Errorf("account id = %v, want %v", got.AccountID, accountID)
	}
	if got.Action != audit_event.ActionAccessEntryDelete {
		t.Errorf("action = %q", got.Action)
	}
	if got.TargetID != targetID.String() {
		t.Errorf("target id = %q", got.TargetID)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at not stamped")
	}
}

// A platform-initiated action must be recorded as the system, not silently
// attributed to whoever happened to be in scope.
func TestRecordStoresSystemActorWithoutUser(t *testing.T) {
	rec, repo := newTestRecorder()
	accountID := uuid.New()

	rec.Record(context.Background(), SystemActor(accountID),
		audit_event.NodeCommandAction("apply_access_config"), audit_event.TargetNode, uuid.New().String())

	if len(repo.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(repo.events))
	}
	got := repo.events[0]
	if got.ActorKind != audit_event.ActorSystem {
		t.Errorf("actor kind = %q, want %q", got.ActorKind, audit_event.ActorSystem)
	}
	if got.ActorUserID != nil {
		t.Errorf("system event carries an actor user id: %v", got.ActorUserID)
	}
	if got.Action != "node_command.apply_access_config" {
		t.Errorf("action = %q", got.Action)
	}
}

// An event with no account is invisible to every read (they all filter on
// account_id), so storing one would inflate a row count while answering nothing.
func TestRecordDropsEventWithNoAccount(t *testing.T) {
	rec, repo := newTestRecorder()

	rec.Record(context.Background(), Actor{UserID: uuid.New()},
		audit_event.ActionSecretDelete, audit_event.TargetSecret, uuid.New().String())

	if len(repo.events) != 0 {
		t.Fatalf("expected the event to be dropped, got %d", len(repo.events))
	}
}

// The action being audited has already happened; a failed audit write must not
// turn a bookkeeping fault into a failed operation.
func TestRecordSwallowsRepositoryFailure(t *testing.T) {
	repo := &fakeRepo{err: errors.New("database is down")}
	rec := NewRecorder(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// The absence of a panic and of a return value is the assertion.
	rec.Record(context.Background(), UserActor(uuid.New(), uuid.New()),
		audit_event.ActionNodeDelete, audit_event.TargetNode, uuid.New().String())
}

func TestRecordDetailTruncatesAndOmitsEmpty(t *testing.T) {
	rec, repo := newTestRecorder()
	actor := UserActor(uuid.New(), uuid.New())

	rec.RecordDetail(context.Background(), actor,
		audit_event.ActionNodeAttributeSet, audit_event.TargetNode, uuid.New().String(), "")
	rec.RecordDetail(context.Background(), actor,
		audit_event.ActionNodeAttributeSet, audit_event.TargetNode, uuid.New().String(),
		strings.Repeat("x", audit_event.DetailMaxLen+50))

	if len(repo.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(repo.events))
	}
	if repo.events[0].Detail != "" {
		t.Errorf("empty detail stored as %q", repo.events[0].Detail)
	}
	if got := len([]rune(repo.events[1].Detail)); got != audit_event.DetailMaxLen+1 {
		t.Errorf("detail length = %d runes, want %d (cap plus the ellipsis)",
			got, audit_event.DetailMaxLen+1)
	}
}

// A Recorder that discards is the state this package exists to end, so a missing
// repository must fail at composition rather than at the first privileged action.
func TestNewRecorderPanicsWithoutRepository(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic without an audit event repository")
		}
	}()
	NewRecorder(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
