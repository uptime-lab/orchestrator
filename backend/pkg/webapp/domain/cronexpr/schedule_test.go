package cronexpr

import (
	"strings"
	"testing"
	"time"
)

func mustCompile(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := Compile(expr)
	if err != nil {
		t.Fatalf("Compile(%q) = %v", expr, err)
	}
	return s
}

// The compiled form and the live matcher must agree on every minute of a
// representative window — they are one implementation, and this is what says so.
func TestCompiledScheduleAgreesWithMatchesMinuteByMinute(t *testing.T) {
	start := time.Date(2026, 2, 26, 0, 0, 0, 0, time.UTC) // crosses a month end
	for _, expr := range []string{
		"* * * * *", "*/15 * * * *", "0 3 * * 1", "0 3 * * 7", "0 3 1 * 1",
		"0 3 1 * *", "30 2 29 2 *", "0 0-6 * * 1-5", "0 3 */2 * *",
	} {
		schedule := mustCompile(t, expr)
		for i := 0; i < 3*24*60; i++ {
			at := start.Add(time.Duration(i) * time.Minute)
			want, err := Matches(expr, at)
			if err != nil {
				t.Fatalf("Matches(%q) = %v", expr, err)
			}
			if got := schedule.MatchesTime(at); got != want {
				t.Fatalf("%q at %s: compiled=%v, Matches=%v", expr, at.Format(time.RFC3339), got, want)
			}
		}
	}
}

func TestNextRunsFindsTheExpectedTimes(t *testing.T) {
	// Friday 2026-08-14 12:00 UTC.
	from := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		expr string
		want []string
	}{
		{"0 3 * * 1", []string{"2026-08-17T03:00:00Z", "2026-08-24T03:00:00Z"}},
		{"0 3 * * *", []string{"2026-08-15T03:00:00Z", "2026-08-16T03:00:00Z"}},
		{"*/30 * * * *", []string{"2026-08-14T12:30:00Z", "2026-08-14T13:00:00Z"}},
		{"0 0 1 * *", []string{"2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z"}},
		// Both day fields restricted: the 1st OR a Monday, per crontab(5).
		{"0 3 1 * 1", []string{"2026-08-17T03:00:00Z", "2026-08-24T03:00:00Z", "2026-08-31T03:00:00Z", "2026-09-01T03:00:00Z"}},
	} {
		got := mustCompile(t, tt.expr).NextRuns(from, len(tt.want))
		if len(got) != len(tt.want) {
			t.Errorf("NextRuns(%q) = %v, want %d entries", tt.expr, got, len(tt.want))
			continue
		}
		for i, want := range tt.want {
			if got[i].Format(time.RFC3339) != want {
				t.Errorf("NextRuns(%q)[%d] = %s, want %s", tt.expr, i, got[i].Format(time.RFC3339), want)
			}
		}
	}
}

// A valid expression that never fires is the single most useful thing a preview
// can report, and it is invisible in the expression itself: February has no
// 30th, so this saves, validates, and silently does nothing forever.
func TestNextRunsReportsAScheduleThatNeverFires(t *testing.T) {
	from := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	if got := mustCompile(t, "0 0 30 2 *").NextRuns(from, 5); len(got) != 0 {
		t.Errorf("NextRuns = %v, want none: February has no 30th", got)
	}
	// ...while the same day-of-month with a weekday still fires, because the two
	// are ORed.
	if got := mustCompile(t, "0 0 30 2 1").NextRuns(from, 1); len(got) == 0 {
		t.Error("NextRuns = none, want the Monday leg of the OR to fire")
	}
}

func TestNextRunsIsStrictlyAfterTheGivenTime(t *testing.T) {
	at := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC) // itself a firing minute
	got := mustCompile(t, "0 3 * * 1").NextRuns(at, 1)
	if len(got) != 1 || !got[0].After(at) {
		t.Errorf("NextRuns(%s) = %v, want the following week", at.Format(time.RFC3339), got)
	}
}

func TestDescribe(t *testing.T) {
	for _, tt := range []struct {
		expr     string
		contains []string
	}{
		{"* * * * *", []string{"Every minute"}},
		{"*/15 * * * *", []string{"Every 15 minutes"}},
		{"0 * * * *", []string{"Every hour at :00"}},
		{"0 3 * * *", []string{"At 03:00 UTC", "every day"}},
		{"0 3 * * 1", []string{"At 03:00 UTC", "Monday"}},
		{"0 3 1 * *", []string{"At 03:00 UTC", "1st"}},
		{"0 3 * 7 1", []string{"Monday", "July"}},
		// The case cron surprises people with must be stated, not implied.
		{"0 3 1 * 1", []string{"1st", "Monday", "either one fires it"}},
	} {
		got, err := Describe(tt.expr)
		if err != nil {
			t.Errorf("Describe(%q) = %v", tt.expr, err)
			continue
		}
		for _, want := range tt.contains {
			if !strings.Contains(got, want) {
				t.Errorf("Describe(%q) = %q, want it to contain %q", tt.expr, got, want)
			}
		}
	}
}

func TestDescribeRejectsWhatValidateRejects(t *testing.T) {
	if _, err := Describe("99 * * * *"); err == nil {
		t.Error("Describe(99 * * * *) = nil error, want the same refusal Validate gives")
	}
}

// "Every 15 minutes every day" reads as a stutter; an interval already says how
// often, while a fixed time still needs "every day" or it could be read as a
// one-off.
func TestDescribeDropsTheRedundantEveryDayAfterAnInterval(t *testing.T) {
	for expr, want := range map[string]string{
		"*/15 * * * *": "Every 15 minutes",
		"0 * * * *":    "Every hour at :00",
		"* * * * *":    "Every minute",
	} {
		if got, _ := Describe(expr); got != want {
			t.Errorf("Describe(%q) = %q, want %q", expr, got, want)
		}
	}
	if got, _ := Describe("0 3 * * *"); got != "At 03:00 UTC every day" {
		t.Errorf("Describe(0 3 * * *) = %q, want the daily phrasing kept", got)
	}
}
