package cronexpr

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// MaxHorizonDays bounds the search for upcoming runs. Five years is far past
// anything an operator plans and still cheap, because the walk skips whole days
// that cannot match rather than testing every minute.
//
// Reaching the horizon is a RESULT, not a failure: `0 0 30 2 *` is a valid
// expression that never fires, and saying so is the single most useful thing a
// preview can tell someone.
const MaxHorizonDays = 5 * 366

// Schedule is a compiled cron expression: the set of values each field admits.
//
// It is derived by evaluating the SAME matcher the scheduler uses over each
// field's domain, so a compiled schedule and a live match cannot disagree —
// there is one implementation of the dialect, not two. Matches() itself goes
// through here.
type Schedule struct {
	minutes []int
	hours   []int
	doms    []int
	months  []int
	// weekdays holds Go weekdays (0-6); a field naming 7 contributes 0.
	weekdays []int
	// domRestricted/dowRestricted record whether each day field begins with '*'.
	// When BOTH are restricted the two are ORed, as crontab(5) specifies.
	domRestricted bool
	dowRestricted bool
}

// Compile parses and validates an expression into its value sets.
func Compile(expr string) (*Schedule, error) {
	fields, err := split(expr)
	if err != nil {
		return nil, err
	}
	if err := validateFields(fields); err != nil {
		return nil, err
	}

	s := &Schedule{
		domRestricted: restricted(fields[dayOfMonthField]),
		dowRestricted: restricted(fields[weekdayField]),
	}
	for i, target := range []*[]int{&s.minutes, &s.hours, &s.doms, &s.months} {
		values, verr := expand(fields[i], fieldBounds[i].min, fieldBounds[i].max)
		if verr != nil {
			return nil, verr
		}
		*target = values
	}
	// Weekdays are expanded through weekdayMatches so a field naming 7 admits
	// the day Go calls 0 — see that function for why the wider range alone is
	// not enough.
	for weekday := 0; weekday <= 6; weekday++ {
		ok, werr := weekdayMatches(fields[weekdayField], weekday)
		if werr != nil {
			return nil, werr
		}
		if ok {
			s.weekdays = append(s.weekdays, weekday)
		}
	}
	return s, nil
}

func expand(field string, min, max int) ([]int, error) {
	var out []int
	for value := min; value <= max; value++ {
		ok, err := fieldMatches(field, value, min, max)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, value)
		}
	}
	return out, nil
}

// MatchesTime reports whether the schedule fires at t (evaluated in UTC).
func (s *Schedule) MatchesTime(t time.Time) bool {
	t = t.UTC()
	if !contains(s.minutes, t.Minute()) || !contains(s.hours, t.Hour()) {
		return false
	}
	if !contains(s.months, int(t.Month())) {
		return false
	}
	return s.dayMatches(t)
}

func (s *Schedule) dayMatches(t time.Time) bool {
	dom := contains(s.doms, t.Day())
	dow := contains(s.weekdays, int(t.Weekday()))
	if s.domRestricted && s.dowRestricted {
		return dom || dow
	}
	return dom && dow
}

// NextRuns returns up to n firing times strictly after `after`, in UTC.
//
// Fewer than n (or none) means the schedule genuinely does not fire again
// within MaxHorizonDays — `0 0 30 2 *` is the honest example. Callers should
// say so rather than treat it as an error.
func (s *Schedule) NextRuns(after time.Time, n int) []time.Time {
	if n <= 0 || len(s.minutes) == 0 || len(s.hours) == 0 || len(s.months) == 0 {
		return nil
	}
	after = after.UTC().Truncate(time.Minute)
	out := make([]time.Time, 0, n)

	day := time.Date(after.Year(), after.Month(), after.Day(), 0, 0, 0, 0, time.UTC)
	for d := 0; d < MaxHorizonDays && len(out) < n; d++ {
		if d > 0 {
			day = day.AddDate(0, 0, 1)
		}
		if !contains(s.months, int(day.Month())) || !s.dayMatches(day) {
			continue
		}
		for _, hour := range s.hours {
			for _, minute := range s.minutes {
				at := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, time.UTC)
				if !at.After(after) {
					continue
				}
				out = append(out, at)
				if len(out) == n {
					return out
				}
			}
		}
	}
	return out
}

func contains(values []int, v int) bool {
	i := sort.SearchInts(values, v)
	return i < len(values) && values[i] == v
}

var weekdayNames = [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

var monthNames = [13]string{"", "January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}

// Describe renders an expression as a sentence, for an editor that must not
// make an operator parse cron in their head.
//
// It never guesses: a shape it does not recognise is spelled out field by field
// rather than approximated. The upcoming-runs list is the ground truth either
// way, which is why a preview shows both.
func Describe(expr string) (string, error) {
	s, err := Compile(expr)
	if err != nil {
		return "", err
	}
	return s.Describe(), nil
}

func (s *Schedule) Describe() string {
	timePart, interval := s.describeTime()
	dayPart := s.describeDays()
	monthPart := s.describeMonths()

	// "Every 15 minutes every day" — the tail is noise once the phrase already
	// says how often. It earns its place only after a fixed time of day, where
	// "At 03:00 UTC" alone could read as a one-off.
	if interval && dayPart == "every day" {
		dayPart = ""
	}

	switch {
	case dayPart == "" && monthPart == "":
		return timePart
	case monthPart == "":
		return timePart + " " + dayPart
	case dayPart == "":
		return timePart + " " + monthPart
	default:
		return timePart + " " + dayPart + " " + monthPart
	}
}

// describeTime covers the minute/hour pair, which is where the common shapes
// live: every minute, every N minutes, hourly, and a fixed time of day.
// The bool reports an INTERVAL shape ("every N minutes", "every hour"), as
// opposed to a fixed time of day — the caller uses it to drop a redundant
// "every day".
func (s *Schedule) describeTime() (string, bool) {
	everyMinute := len(s.minutes) == 60
	everyHour := len(s.hours) == 24

	if everyMinute && everyHour {
		return "Every minute", true
	}
	if everyHour && isEvenStep(s.minutes) && len(s.minutes) > 1 {
		return fmt.Sprintf("Every %d minutes", s.minutes[1]-s.minutes[0]), true
	}
	if everyHour && len(s.minutes) == 1 {
		return fmt.Sprintf("Every hour at :%02d", s.minutes[0]), true
	}
	if everyMinute && len(s.hours) == 1 {
		return fmt.Sprintf("Every minute of the %02d:00 hour UTC", s.hours[0]), true
	}
	if len(s.hours) == 1 && len(s.minutes) == 1 {
		return fmt.Sprintf("At %02d:%02d UTC", s.hours[0], s.minutes[0]), false
	}
	// Several times of day: list them while the list stays readable.
	var times []string
	for _, h := range s.hours {
		for _, m := range s.minutes {
			times = append(times, fmt.Sprintf("%02d:%02d", h, m))
			if len(times) > 6 {
				return fmt.Sprintf("At %d times a day (UTC)", len(s.hours)*len(s.minutes)), false
			}
		}
	}
	return "At " + strings.Join(times, ", ") + " UTC", false
}

// describeDays states the day rule, and states the OR explicitly when both day
// fields are restricted — that is the case where cron surprises people, so it
// is the one case the sentence must not leave implicit.
func (s *Schedule) describeDays() string {
	domAll := len(s.doms) == 31
	dowAll := len(s.weekdays) == 7

	dom := ""
	if !domAll {
		dom = "on the " + joinOrdinals(s.doms)
	}
	dow := ""
	if !dowAll {
		dow = "on " + joinNames(s.weekdays, func(v int) string { return weekdayNames[v] })
	}

	switch {
	case dom == "" && dow == "":
		return "every day"
	case dom != "" && dow != "":
		// crontab(5): both restricted means EITHER matches.
		return dom + ", and " + dow + " (either one fires it)"
	case dom != "":
		return dom
	default:
		return dow
	}
}

func (s *Schedule) describeMonths() string {
	if len(s.months) == 12 {
		return ""
	}
	return "in " + joinNames(s.months, func(v int) string { return monthNames[v] })
}

// isEvenStep reports whether the values are an arithmetic progression covering
// the field from its first value — i.e. a `*/n` shape, however it was written.
func isEvenStep(values []int) bool {
	if len(values) < 2 {
		return false
	}
	step := values[1] - values[0]
	for i := 2; i < len(values); i++ {
		if values[i]-values[i-1] != step {
			return false
		}
	}
	return true
}

func joinNames(values []int, name func(int) string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, name(v))
	}
	return joinWithAnd(parts)
}

func joinOrdinals(values []int) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, ordinal(v))
	}
	return joinWithAnd(parts)
}

func joinWithAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	if len(parts) > 6 {
		return fmt.Sprintf("%s and %d others", strings.Join(parts[:3], ", "), len(parts)-3)
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func ordinal(v int) string {
	suffix := "th"
	switch {
	case v%100 >= 11 && v%100 <= 13:
	case v%10 == 1:
		suffix = "st"
	case v%10 == 2:
		suffix = "nd"
	case v%10 == 3:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s", v, suffix)
}
