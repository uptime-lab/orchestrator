package workflow_execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

type fakeAnomalyRepo struct {
	recorded []*instance_anomaly.Anomaly
	resolved []string
}

func (f *fakeAnomalyRepo) Record(_ context.Context, a *instance_anomaly.Anomaly) error {
	f.recorded = append(f.recorded, a)
	return nil
}

func (f *fakeAnomalyRepo) ResolveCondition(_ context.Context, key string) error {
	f.resolved = append(f.resolved, key)
	return nil
}

func skipTestService(repo *fakeAnomalyRepo) *Service {
	return &Service{
		anomalyRepo: repo,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestScheduleSkipIsRecordedAsAResolvableCondition(t *testing.T) {
	repo := &fakeAnomalyRepo{}
	svc := skipTestService(repo)
	tpl := workflow_template.WorkflowTemplate{
		ID: uuid.New(), AccountID: uuid.New(), Name: "nightly reboot",
	}

	svc.recordScheduleSkip(context.Background(), tpl.ID, tpl.AccountID, tpl.Name,
		skipReasonCronCapExceeded, instance_anomaly.SeverityWarning, "over the cap",
		map[string]interface{}{"cap": 10})

	if len(repo.recorded) != 1 {
		t.Fatalf("recorded %d anomalies, want 1", len(repo.recorded))
	}
	got := repo.recorded[0]
	if got.Kind != instance_anomaly.KindScheduledPlaybookSkipped {
		t.Errorf("kind = %q", got.Kind)
	}
	// A condition, not an event: it re-detects every minute while it holds, and
	// as an event that is 1440 rows a day for one misconfigured playbook.
	if got.Class != instance_anomaly.ClassCondition {
		t.Errorf("class = %q, want a condition", got.Class)
	}
	if got.AccountID == nil || *got.AccountID != tpl.AccountID {
		t.Errorf("account = %v, want %v", got.AccountID, tpl.AccountID)
	}
	// The operator's first question is which playbook, and the id alone does not
	// answer it in a console listing.
	if got.Details["template_name"] != "nightly reboot" {
		t.Errorf("details lost the playbook name: %v", got.Details)
	}
	if got.Details["cap"] != 10 {
		t.Errorf("details lost the cause specifics: %v", got.Details)
	}
}

// Two different causes must not collapse into one row: a playbook whose author
// was demoted AND which then lost its last node has had two things go wrong,
// and merging them lets the second hide behind the first's acknowledgement.
func TestSkipKeyDistinguishesCauses(t *testing.T) {
	id := uuid.New()
	capKey := instance_anomaly.ScheduledPlaybookSkipKey(id, skipReasonCronCapExceeded)
	nodesKey := instance_anomaly.ScheduledPlaybookSkipKey(id, skipReasonNoTargetNodes)

	if capKey == nodesKey {
		t.Fatal("two causes share a dedupe key; one will mask the other")
	}
	if !strings.Contains(capKey, id.String()) {
		t.Error("the key does not identify the playbook, so two playbooks would collapse into one row")
	}
}

// A condition nothing resolves is a condition operators learn to ignore. The
// skip is defined by an absence — the run that did not happen — so the only
// moment anything can observe that it ended is the run that finally does.
func TestASuccessfulFireResolvesEverySkipCause(t *testing.T) {
	repo := &fakeAnomalyRepo{}
	svc := skipTestService(repo)
	id := uuid.New()

	svc.resolveScheduleSkips(context.Background(), id)

	for _, reason := range []string{
		skipReasonCronCapExceeded, skipReasonInvalidCron, skipReasonNoAuthor,
		skipReasonNoAuthzChecker, skipReasonAuthorDemoted, skipReasonNoTargetNodes,
		skipReasonStartRefused,
	} {
		want := instance_anomaly.ScheduledPlaybookSkipKey(id, reason)
		found := false
		for _, got := range repo.resolved {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("a fire did not clear the %q condition; it stays firing forever", reason)
		}
	}
}

// The cap skip is the one cause a fire can never resolve — a playbook held back
// by the cap is by definition one that does not run — so it gets its own path,
// driven by the next tick observing that it is no longer past the cap.
func TestComingBackUnderTheCapResolvesTheSkip(t *testing.T) {
	repo := &fakeAnomalyRepo{}
	svc := skipTestService(repo)
	id := uuid.New()

	// Tick 1: over the cap.
	svc.resolveOverCapSkips(context.Background(), map[uuid.UUID]bool{id: true})
	if len(repo.resolved) != 0 {
		t.Fatalf("resolved %v while the playbook was still over the cap", repo.resolved)
	}
	// Tick 2: still over it — nothing changed, so nothing is written.
	svc.resolveOverCapSkips(context.Background(), map[uuid.UUID]bool{id: true})
	if len(repo.resolved) != 0 {
		t.Fatalf("a steady-state tick wrote %v; the diff exists so it writes nothing", repo.resolved)
	}
	// Tick 3: the cap was raised.
	svc.resolveOverCapSkips(context.Background(), map[uuid.UUID]bool{})

	want := instance_anomaly.ScheduledPlaybookSkipKey(id, skipReasonCronCapExceeded)
	if len(repo.resolved) != 1 || repo.resolved[0] != want {
		t.Fatalf("resolved %v, want exactly the cap condition for the playbook that came back", repo.resolved)
	}
}

// A resolve that fails must be retried, not forgotten: dropping it leaves the
// row firing forever, which is the outcome this whole path exists to prevent.
func TestAFailedResolveIsRetriedOnTheNextTick(t *testing.T) {
	repo := &failingResolveRepo{}
	svc := &Service{anomalyRepo: repo, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	id := uuid.New()

	svc.resolveOverCapSkips(context.Background(), map[uuid.UUID]bool{id: true})
	svc.resolveOverCapSkips(context.Background(), map[uuid.UUID]bool{}) // fails
	if repo.attempts != 1 {
		t.Fatalf("attempts = %d, want 1", repo.attempts)
	}
	svc.resolveOverCapSkips(context.Background(), map[uuid.UUID]bool{})
	if repo.attempts != 2 {
		t.Fatalf("attempts = %d, want the failed resolve retried on the next tick", repo.attempts)
	}
	svc.resolveOverCapSkips(context.Background(), map[uuid.UUID]bool{})
	if repo.attempts != 2 {
		t.Errorf("attempts = %d, want no further writes once it succeeded", repo.attempts)
	}
}

// failingResolveRepo fails the first resolve and then succeeds.
func (f *fakeAnomalyRepo) ListSerialBindingRefusals(context.Context, uuid.UUID, int) ([]repository.SerialBindingRefusal, error) {
	return nil, nil
}

type failingResolveRepo struct {
	fakeAnomalyRepo
	attempts int
}

func (f *failingResolveRepo) ResolveCondition(_ context.Context, _ string) error {
	f.attempts++
	if f.attempts == 1 {
		return errors.New("database is down")
	}
	return nil
}

// Nothing wired must not panic: the scheduler runs on installs with no console.
func TestSkipRecordingIsOptional(t *testing.T) {
	svc := &Service{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	svc.recordScheduleSkip(context.Background(), uuid.New(), uuid.New(), "",
		skipReasonInvalidCron, instance_anomaly.SeverityWarning, "x", nil)
	svc.resolveScheduleSkips(context.Background(), uuid.New())
}
