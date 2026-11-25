package results

import (
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

func templateWithCron(t *testing.T, cron string) *WorkflowTemplateResult {
	t.Helper()
	tpl := &workflow_template.WorkflowTemplate{
		ID:        uuid.New(),
		AccountID: uuid.New(),
		Name:      "nightly",
		Status:    workflow_template.StatusActive,
		Graph: &workflow_template.WorkflowGraph{
			Nodes: []workflow_template.GraphNode{{
				ID:     "start",
				Type:   "trigger.schedule",
				Params: map[string]interface{}{"cron": cron},
			}},
		},
	}
	return NewWorkflowTemplateResult(tpl)
}

// The schedule is read by the SERVER, using the same cronexpr the scheduler
// runs on. A row promising Tuesday while the scheduler means Wednesday is worse
// than a row with no schedule column.
func TestScheduleFieldsAreServerComputed(t *testing.T) {
	got := templateWithCron(t, "0 3 * * 1")
	if got.ScheduleCron != "0 3 * * 1" {
		t.Fatalf("ScheduleCron = %q", got.ScheduleCron)
	}
	if got.ScheduleDescription == "" {
		t.Fatal("no description for a valid weekly schedule")
	}
	if got.NextRunAt == nil {
		t.Fatal("no next run for a schedule that fires weekly")
	}
	if got.NextRunAt.Weekday().String() != "Monday" {
		t.Fatalf("next run is a %s; the expression says Monday", got.NextRunAt.Weekday())
	}
}

// `0 0 30 2 *` is valid, saves cleanly, and does nothing forever. The list row
// is the only place an operator can notice it, so the schedule shows and the
// next run does not.
func TestAScheduleThatNeverFiresHasNoNextRun(t *testing.T) {
	got := templateWithCron(t, "0 0 30 2 *")
	if got.ScheduleCron != "0 0 30 2 *" {
		t.Fatalf("ScheduleCron = %q, want the expression to still show", got.ScheduleCron)
	}
	if got.NextRunAt != nil {
		t.Fatalf("NextRunAt = %v for an expression that can never fire", got.NextRunAt)
	}
}

// A manual playbook carries no schedule at all, and the fields are omitempty so
// the row renders nothing rather than an empty schedule column.
func TestManualPlaybookHasNoScheduleFields(t *testing.T) {
	got := templateWithCron(t, "")
	if got.ScheduleCron != "" || got.ScheduleDescription != "" || got.NextRunAt != nil {
		t.Fatalf("manual playbook carries a schedule: %+v", got)
	}
}

// An expression that no longer compiles must not fail the LIST. The row still
// exists and opening it is the only way to fix it.
func TestAnUnreadableCronStillListsThePlaybook(t *testing.T) {
	got := templateWithCron(t, "not a cron")
	if got.Name != "nightly" {
		t.Fatalf("the template did not survive an unreadable schedule: %+v", got)
	}
	if got.ScheduleDescription != "" || got.NextRunAt != nil {
		t.Fatalf("an unreadable schedule was described anyway: %+v", got)
	}
}
