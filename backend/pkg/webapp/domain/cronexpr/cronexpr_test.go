package cronexpr

import (
	"testing"
	"time"
)

func TestMatches(t *testing.T) {
	// Monday 2026-07-13 03:00 UTC (weekday 1).
	mon0300 := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		expr string
		at   time.Time
		want bool
	}{
		{"* * * * *", mon0300, true},
		{"0 3 * * 1", mon0300, true},    // weekly Monday 03:00
		{"0 3 * * 0", mon0300, false},   // Sunday only
		{"0 4 * * 1", mon0300, false},   // wrong hour
		{"*/15 * * * *", mon0300, true}, // minute 0 divisible by 15
		{"*/15 * * * *", mon0300.Add(5 * time.Minute), false},
		{"0 0-6 * * *", mon0300, true},     // hour in range
		{"0 3 13 7 *", mon0300, true},      // day 13, month 7
		{"0 3 1,13,28 * *", mon0300, true}, // day list
		{"0 3 1,28 * *", mon0300, false},   // day not in list
	}
	// Sunday 2026-07-12 03:00 UTC — Go's Weekday() is 0 here, and standard cron
	// spells this day BOTH 0 and 7. Every one of these used to be false, so a
	// playbook scheduled the way crontab(5) documents simply never ran.
	sun0300 := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	tests = append(tests, []struct {
		expr string
		at   time.Time
		want bool
	}{
		{"0 3 * * 0", sun0300, true},
		{"0 3 * * 7", sun0300, true},   // 7 is Sunday too
		{"0 3 * * 1-7", sun0300, true}, // a range that names 7 includes Sunday
		{"0 3 * * 0-6", sun0300, true},
		{"0 3 * * 1-6", sun0300, false}, // Mon-Sat genuinely excludes Sunday
		{"0 3 * * 7", mon0300, false},   // and 7 must not match anything else
		{"0 3 * * 1-7", mon0300, true},  // ...while the range still covers Monday
		{"0 3 * * 6,7", sun0300, true},  // in a list
	}...)
	for _, tt := range tests {
		got, err := Matches(tt.expr, tt.at)
		if err != nil {
			t.Errorf("Matches(%q) error: %v", tt.expr, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Matches(%q, %s) = %v, want %v", tt.expr, tt.at.Format(time.RFC3339), got, tt.want)
		}
	}
}

// crontab(5): when day-of-month and day-of-week are BOTH restricted the two are
// an OR. This platform ANDed them, so `0 3 1 * 1` meant "a Monday that is also
// the 1st" — roughly one date a year — where the same line in a crontab means
// "the 1st, and every Monday".
func TestDayOfMonthAndWeekdayAreORedWhenBothRestricted(t *testing.T) {
	// 2026-07-01 is a Wednesday; 2026-07-13 is a Monday; 2026-07-08 neither.
	wed1st := time.Date(2026, 7, 1, 3, 0, 0, 0, time.UTC)
	mon13th := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	wed8th := time.Date(2026, 7, 8, 3, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		at   time.Time
		want bool
		why  string
	}{
		{wed1st, true, "the 1st matches the day-of-month field"},
		{mon13th, true, "a Monday matches the day-of-week field"},
		{wed8th, false, "neither field matches"},
	} {
		got, err := Matches("0 3 1 * 1", tt.at)
		if err != nil {
			t.Fatalf("Matches() error: %v", err)
		}
		if got != tt.want {
			t.Errorf("Matches(\"0 3 1 * 1\", %s) = %v, want %v — %s",
				tt.at.Format("2006-01-02"), got, tt.want, tt.why)
		}
	}
}

// With one of the two unrestricted the relationship stays an AND, or `0 3 1 * *`
// would fire every day. `*/2` counts as unrestricted, which is what Vixie cron
// does — its parser sets the star flag from the first character.
func TestDayFieldsStayANDedWhenOneIsUnrestricted(t *testing.T) {
	wed8th := time.Date(2026, 7, 8, 3, 0, 0, 0, time.UTC)

	if got, _ := Matches("0 3 1 * *", wed8th); got {
		t.Error("Matches(\"0 3 1 * *\") on the 8th = true, want false")
	}
	if got, _ := Matches("0 3 */2 * 1", wed8th); got {
		t.Error("a step day-of-month must not turn the pair into an OR")
	}
}

// The whole point of Validate: these used to save cleanly and then never fire.
func TestValidateRejectsExpressionsThatCouldNeverFire(t *testing.T) {
	for _, expr := range []string{
		"99 * * * *",   // minute out of range
		"* 25 * * *",   // hour out of range
		"* * 0 * *",    // day-of-month is 1-based
		"* * 32 * *",   // ...and stops at 31
		"* * * 13 *",   // month out of range
		"* * * * 8",    // weekday stops at 7
		"* * * * 5-1",  // a range that counts down
		"0 3 * * MON",  // names are not supported; say so rather than never fire
		"* * * *",      // too few fields
		"bad * * * *",  // unparseable
		"*/x * * * *",  // bad step
		"*/0 * * * *",  // zero step
		"*/99 * * * *", // step wider than the field
	} {
		if err := Validate(expr); err == nil {
			t.Errorf("Validate(%q) = nil, want an error", expr)
		}
		// Matches must agree: a bad expression is an error there too, never a
		// quiet "did not match this minute".
		if _, err := Matches(expr, time.Now()); err == nil {
			t.Errorf("Matches(%q) = nil error, want one", expr)
		}
	}
}

func TestValidateAcceptsWhatTheSchedulerCanFire(t *testing.T) {
	for _, expr := range []string{
		"* * * * *",
		"0 3 * * *",
		"*/15 * * * *",
		"0 0-6 * * 1-5",
		"0 3 1,15 * *",
		"0 3 * * 7",
		"30 2 29 2 *", // a real date, even if it is rare
	} {
		if err := Validate(expr); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", expr, err)
		}
	}
}
