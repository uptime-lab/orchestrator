// Package cronexpr evaluates the 5-field cron expressions scheduled playbooks
// carry. It is deliberately small — enough for a schedule trigger, not a
// general cron library — but the dialect it does implement is crontab(5)'s,
// because an operator writing a cron expression has written one before.
//
// It lives in the domain so that saving a playbook and firing one answer the
// same question with the same code. They used to be separate: nothing parsed
// the expression at save time, so `99 * * * *` saved cleanly and then never
// fired.
package cronexpr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Fields is the number of fields in the supported expression:
// minute hour day-of-month month day-of-week.
const Fields = 5

// dayOfMonthField and weekdayField index the two fields whose relationship is
// special (see Matches).
const (
	dayOfMonthField = 2
	weekdayField    = 4
)

type bounds struct{ min, max int }

// fieldBounds are the accepted ranges, in field order. Weekday accepts 0-7
// because crontab(5) spells Sunday BOTH ways; see weekdayMatches for why the
// wider range alone is not enough.
var fieldBounds = [Fields]bounds{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 7},  // day of week
}

// Validate reports whether expr is a cron expression this scheduler can fire.
//
// It exists so a bad expression is refused where the operator can still fix it.
// Without it the failure mode was silence: an out-of-range field resolved to
// "matches nothing", so the playbook saved, the scheduler examined it every
// minute forever, and the run simply never happened.
func Validate(expr string) error {
	fields, err := split(expr)
	if err != nil {
		return err
	}
	return validateFields(fields)
}

func validateFields(fields []string) error {
	for i, field := range fields {
		if err := validateField(field, fieldBounds[i].min, fieldBounds[i].max); err != nil {
			return fmt.Errorf("cron field %d (%s): %w", i+1, fieldName(i), err)
		}
	}
	return nil
}

// Matches reports whether expr fires at time t (evaluated in UTC). Supports
// '*', lists (1,2), ranges (1-5) and steps (*/5, 1-30/2).
//
// It goes through Compile so that a live match, an upcoming-runs preview and a
// human description are all the same dialect by construction rather than by
// three careful readings of one another. Compiling is ~134 field evaluations,
// which is nothing against a per-minute scan that already loads rows.
func Matches(expr string, t time.Time) (bool, error) {
	schedule, err := Compile(expr)
	if err != nil {
		return false, err
	}
	return schedule.MatchesTime(t), nil
}

func restricted(field string) bool {
	return !strings.HasPrefix(strings.TrimSpace(field), "*")
}

func split(expr string) ([]string, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != Fields {
		return nil, fmt.Errorf("cron expression must have %d fields (minute hour day-of-month month day-of-week), got %d",
			Fields, len(fields))
	}
	return fields, nil
}

func fieldName(i int) string {
	return [Fields]string{"minute", "hour", "day-of-month", "month", "day-of-week"}[i]
}

// weekdayMatches evaluates the day-of-week field, where 0 and 7 both mean
// Sunday — as they do in Vixie cron, crontab(5) and every scheduler an operator
// is likely to have used before this one.
//
// Go's time.Weekday only ever produces 0-6, so `0 3 * * 7` and `0 3 * * 1-7`
// used to match NOTHING: the playbook saved cleanly, the scheduler ran every
// minute, and the run simply never happened — no error, no log, nothing to
// notice. Widening the range to 0-7 alone does not fix it, because the VALUE
// being tested is still 0 on a Sunday; the second evaluation below is what makes
// a field that names 7 match the day Go calls 0.
func weekdayMatches(field string, weekday int) (bool, error) {
	ok, err := fieldMatches(field, weekday, 0, 7)
	if err != nil || ok {
		return ok, err
	}
	if weekday != 0 {
		return false, nil
	}
	return fieldMatches(field, 7, 0, 7)
}

func fieldMatches(field string, value, min, max int) (bool, error) {
	for _, part := range strings.Split(field, ",") {
		lo, hi, step, err := parsePart(part, min, max)
		if err != nil {
			return false, err
		}
		if value < lo || value > hi {
			continue
		}
		if (value-lo)%step == 0 {
			return true, nil
		}
	}
	return false, nil
}

func validateField(field string, min, max int) error {
	if strings.TrimSpace(field) == "" {
		return fmt.Errorf("is empty")
	}
	for _, part := range strings.Split(field, ",") {
		if _, _, _, err := parsePart(part, min, max); err != nil {
			return err
		}
	}
	return nil
}

// parsePart resolves one comma-separated element into an inclusive range and a
// step, rejecting anything outside the field's bounds.
//
// Rejecting is the change: an out-of-range value used to resolve to a range no
// clock value can fall into, i.e. to "never matches", which is the same
// observable behaviour as a correct expression whose time has not come.
func parsePart(part string, min, max int) (lo, hi, step int, err error) {
	part = strings.TrimSpace(part)
	if part == "" {
		return 0, 0, 0, fmt.Errorf("empty element in cron field")
	}
	step = 1
	if idx := strings.Index(part, "/"); idx >= 0 {
		s, serr := strconv.Atoi(strings.TrimSpace(part[idx+1:]))
		if serr != nil || s <= 0 {
			return 0, 0, 0, fmt.Errorf("invalid step in cron field %q", part)
		}
		if s > max-min+1 {
			return 0, 0, 0, fmt.Errorf("step %d in cron field %q is larger than the whole %d-%d range", s, part, min, max)
		}
		step = s
		part = strings.TrimSpace(part[:idx])
	}

	lo, hi = min, max
	if part != "*" {
		if idx := strings.Index(part, "-"); idx > 0 {
			a, err1 := strconv.Atoi(strings.TrimSpace(part[:idx]))
			b, err2 := strconv.Atoi(strings.TrimSpace(part[idx+1:]))
			if err1 != nil || err2 != nil {
				return 0, 0, 0, fmt.Errorf("invalid range in cron field %q", part)
			}
			lo, hi = a, b
		} else {
			v, verr := strconv.Atoi(part)
			if verr != nil {
				return 0, 0, 0, fmt.Errorf("invalid value in cron field %q (names like MON and JAN are not supported)", part)
			}
			lo, hi = v, v
		}
	}
	if lo < min || hi > max {
		return 0, 0, 0, fmt.Errorf("cron field %q is outside the allowed range %d-%d", part, min, max)
	}
	if lo > hi {
		return 0, 0, 0, fmt.Errorf("cron field %q counts down (%d-%d); ranges must ascend", part, lo, hi)
	}
	return lo, hi, step, nil
}
