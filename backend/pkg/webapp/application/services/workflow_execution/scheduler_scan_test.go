package workflow_execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// The per-minute scan is the one loop that runs for every account on the
// instance whether or not anything is due, so what it reads — and how much of
// it — is the property under test here, not just which playbook fires.

// scanTemplateRepo records what the scheduler asked for.
type scanTemplateRepo struct {
	repository.WorkflowTemplateRepository
	candidates []workflow_template.ScheduledCandidate
	templates  map[uuid.UUID]workflow_template.WorkflowTemplate

	loadErr error

	askedLimit  int
	loadedByIDs [][]uuid.UUID
}

func (r *scanTemplateRepo) ListScheduledCandidates(_ context.Context, perAccountLimit int) ([]workflow_template.ScheduledCandidate, error) {
	r.askedLimit = perAccountLimit
	return r.candidates, nil
}

func (r *scanTemplateRepo) GetByIDs(_ context.Context, ids []uuid.UUID) ([]workflow_template.WorkflowTemplate, error) {
	r.loadedByIDs = append(r.loadedByIDs, ids)
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	var out []workflow_template.WorkflowTemplate
	for _, id := range ids {
		if tpl, ok := r.templates[id]; ok {
			out = append(out, tpl)
		}
	}
	return out, nil
}

// scanNodeRepo counts fire attempts: reaching it means a playbook got as far as
// resolving its targets. It returns none, so nothing is actually started.
type scanNodeRepo struct {
	repository.NodeRepository
	asked int
}

func (r *scanNodeRepo) GetAllByAccountID(_ context.Context, _ uuid.UUID) ([]node.Node, error) {
	r.asked++
	return nil, nil
}

// everyMinute fires on every tick; neverFires is February 30th.
const (
	everyMinute = "* * * * *"
	neverFires  = "0 0 30 2 *"
)

func scheduledTemplate(id, accountID uuid.UUID, cron string) workflow_template.WorkflowTemplate {
	return workflow_template.WorkflowTemplate{
		ID:        id,
		AccountID: accountID,
		Name:      "scheduled",
		Status:    workflow_template.StatusActive,
		Graph: &workflow_template.WorkflowGraph{
			Nodes: []workflow_template.GraphNode{{
				ID:     uuid.New().String(),
				Type:   "trigger.schedule",
				Params: map[string]interface{}{"cron": cron},
			}},
		},
	}
}

func scanService(repo *scanTemplateRepo, nodes *scanNodeRepo, maxCron int) *Service {
	return &Service{
		templateRepo: repo,
		nodeRepo:     nodes,
		cfg:          config.WorkflowConfig{MaxCronPlaybooksPerAccount: maxCron},
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// The point of the two-phase scan: on a tick where nothing is due, no playbook
// document is read at all. This is what stops a per-minute loop from streaming
// every tenant's graph jsonb 1440 times a day.
func TestNothingDueLoadsNoPlaybookContent(t *testing.T) {
	accountID := uuid.New()
	repo := &scanTemplateRepo{candidates: []workflow_template.ScheduledCandidate{
		{ID: uuid.New(), AccountID: accountID, Name: "nightly", Cron: neverFires},
	}}
	nodes := &scanNodeRepo{}

	scanService(repo, nodes, 0).tickScheduler(context.Background(), map[string]string{})

	if len(repo.loadedByIDs) != 0 {
		t.Fatalf("loaded playbook content for nothing due: %v", repo.loadedByIDs)
	}
	if nodes.asked != 0 {
		t.Errorf("resolved targets %d times for a playbook that is not due", nodes.asked)
	}
}

// And only the due ones are loaded — not the whole scheduled set.
func TestOnlyDuePlaybooksAreLoaded(t *testing.T) {
	accountID := uuid.New()
	dueID, quietID := uuid.New(), uuid.New()
	repo := &scanTemplateRepo{
		candidates: []workflow_template.ScheduledCandidate{
			{ID: dueID, AccountID: accountID, Name: "due", Cron: everyMinute},
			{ID: quietID, AccountID: accountID, Name: "quiet", Cron: neverFires},
		},
		templates: map[uuid.UUID]workflow_template.WorkflowTemplate{
			dueID:   scheduledTemplate(dueID, accountID, everyMinute),
			quietID: scheduledTemplate(quietID, accountID, neverFires),
		},
	}
	nodes := &scanNodeRepo{}

	scanService(repo, nodes, 0).tickScheduler(context.Background(), map[string]string{})

	if len(repo.loadedByIDs) != 1 || len(repo.loadedByIDs[0]) != 1 || repo.loadedByIDs[0][0] != dueID {
		t.Fatalf("loaded %v, want only the due playbook %s", repo.loadedByIDs, dueID)
	}
	if nodes.asked != 1 {
		t.Errorf("fired %d playbooks, want 1", nodes.asked)
	}
}

// The cap is applied by the QUERY, so the scan must ask for one row past it:
// that extra row is the only way it can still report an account being over the
// cap, and asking for exactly the cap would silently stop reporting it.
func TestTheScanAsksForOneRowPastTheCap(t *testing.T) {
	repo := &scanTemplateRepo{}
	scanService(repo, &scanNodeRepo{}, 10).tickScheduler(context.Background(), map[string]string{})
	if repo.askedLimit != 11 {
		t.Errorf("asked for %d rows per account, want cap+1 = 11", repo.askedLimit)
	}

	repo = &scanTemplateRepo{}
	scanService(repo, &scanNodeRepo{}, 0).tickScheduler(context.Background(), map[string]string{})
	if repo.askedLimit != 0 {
		t.Errorf("asked for %d rows per account with the cap off, want unbounded (0)", repo.askedLimit)
	}
}

// A playbook past the cap is not loaded either — the cap has to bind before the
// expensive read, or it only limits what fires while the tick still pays for
// everything the tenant owns.
func TestPlaybooksPastTheCapAreNeitherLoadedNorFired(t *testing.T) {
	accountID := uuid.New()
	firstID, secondID := uuid.New(), uuid.New()
	repo := &scanTemplateRepo{
		candidates: []workflow_template.ScheduledCandidate{
			{ID: firstID, AccountID: accountID, Name: "first", Cron: everyMinute},
			{ID: secondID, AccountID: accountID, Name: "second", Cron: everyMinute},
		},
		templates: map[uuid.UUID]workflow_template.WorkflowTemplate{
			firstID:  scheduledTemplate(firstID, accountID, everyMinute),
			secondID: scheduledTemplate(secondID, accountID, everyMinute),
		},
	}
	nodes := &scanNodeRepo{}

	scanService(repo, nodes, 1).tickScheduler(context.Background(), map[string]string{})

	if len(repo.loadedByIDs) != 1 || len(repo.loadedByIDs[0]) != 1 || repo.loadedByIDs[0][0] != firstID {
		t.Fatalf("loaded %v, want only the playbook within the cap", repo.loadedByIDs)
	}
	if nodes.asked != 1 {
		t.Errorf("fired %d playbooks, want 1 (the cap is 1)", nodes.asked)
	}
}

// Not being loaded is correct and invisible. A playbook silently held back every
// minute, while looking perfectly configured in the UI, is the one skip an
// operator has no other way to discover — so the tick reports it.
func TestAPlaybookPastTheCapIsReportedToTheOperator(t *testing.T) {
	accountID := uuid.New()
	firstID, secondID := uuid.New(), uuid.New()
	repo := &scanTemplateRepo{
		candidates: []workflow_template.ScheduledCandidate{
			{ID: firstID, AccountID: accountID, Name: "first", Cron: everyMinute},
			{ID: secondID, AccountID: accountID, Name: "second", Cron: everyMinute},
		},
		templates: map[uuid.UUID]workflow_template.WorkflowTemplate{
			firstID: scheduledTemplate(firstID, accountID, everyMinute),
		},
	}
	anomalies := &fakeAnomalyRepo{}
	svc := scanService(repo, &scanNodeRepo{}, 1)
	svc.anomalyRepo = anomalies

	svc.tickScheduler(context.Background(), map[string]string{})

	// The playbook that DID fire reports its own skip here (this fake account has
	// no nodes), so look for the cap report specifically rather than counting.
	wantKey := instance_anomaly.ScheduledPlaybookSkipKey(secondID, skipReasonCronCapExceeded)
	var got *instance_anomaly.Anomaly
	for _, a := range anomalies.recorded {
		if a.DedupeKey == wantKey {
			got = a
		}
		if a.DedupeKey == instance_anomaly.ScheduledPlaybookSkipKey(firstID, skipReasonCronCapExceeded) {
			t.Error("a playbook within the cap was reported as past it")
		}
	}
	if got == nil {
		t.Fatalf("the playbook held back by the cap was not reported: %v", anomalies.recorded)
	}
	if got.Details["cap"] != 1 {
		t.Errorf("details lost the cap the operator has to raise: %v", got.Details)
	}
	if got.Details["template_name"] != "second" {
		t.Errorf("details lost the playbook name: %v", got.Details)
	}
}

// schedule_cron is a write-time copy; the graph is the source of truth. A
// candidate is therefore a claim to re-check, and a stale column must cost a
// tick rather than fire something whose graph does not say so.
func TestAStaleScheduleCronDoesNotFire(t *testing.T) {
	accountID := uuid.New()
	id := uuid.New()
	repo := &scanTemplateRepo{
		candidates: []workflow_template.ScheduledCandidate{
			{ID: id, AccountID: accountID, Name: "drifted", Cron: everyMinute},
		},
		templates: map[uuid.UUID]workflow_template.WorkflowTemplate{
			id: scheduledTemplate(id, accountID, neverFires),
		},
	}
	nodes := &scanNodeRepo{}

	scanService(repo, nodes, 0).tickScheduler(context.Background(), map[string]string{})

	if nodes.asked != 0 {
		t.Error("a playbook fired on a stale schedule_cron its own graph contradicts")
	}
}

// A load failure must leave the playbook due, not consume its minute: the
// dedupe map is the only thing standing between a retry and a double fire, so
// marking something fired that never ran hides it until the next match.
func TestALoadFailureLeavesThePlaybookDue(t *testing.T) {
	accountID := uuid.New()
	id := uuid.New()
	repo := &scanTemplateRepo{
		candidates: []workflow_template.ScheduledCandidate{
			{ID: id, AccountID: accountID, Name: "due", Cron: everyMinute},
		},
		loadErr: errors.New("database is unhappy"),
	}
	lastFired := map[string]string{}

	scanService(repo, &scanNodeRepo{}, 0).tickScheduler(context.Background(), lastFired)

	if _, marked := lastFired[id.String()]; marked {
		t.Error("a playbook that never ran was recorded as fired this minute")
	}
}
