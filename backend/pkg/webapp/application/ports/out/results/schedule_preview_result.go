package results

import "time"

// SchedulePreviewResult explains a cron expression to whoever is typing it.
//
// The description and the run list are produced by the SAME code that decides
// whether a playbook fires (domain/cronexpr), which is the point of the route:
// an editor that computed either for itself would be a second implementation of
// the dialect, free to disagree with the scheduler about what the operator just
// wrote.
type SchedulePreviewResult struct {
	Valid bool `json:"valid"`
	// Error carries the refusal for an expression the platform cannot fire.
	// Present only when Valid is false; the route still answers 200, because it
	// is called while the operator types.
	Error string `json:"error,omitempty"`
	// Description is one sentence, e.g. "At 03:00 UTC on Monday".
	Description string `json:"description,omitempty"`
	// NextRuns are UTC instants, soonest first. Empty with NeverFires set means
	// the expression is valid and can never fire — '0 0 30 2 *' is the honest
	// example, and it is invisible in the expression itself.
	NextRuns   []time.Time `json:"next_runs"`
	NeverFires bool        `json:"never_fires,omitempty"`
}
