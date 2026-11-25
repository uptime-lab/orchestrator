package workflow_execution

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
)

// Every path where the scheduler deliberately declines to fire a
// due playbook used to be a bare log line. The operator-visible behaviour was
// "it stopped running and nobody was told" — worse than a failure, because a
// failed run at least leaves an execution row with an error on it. A skipped
// one leaves nothing: no execution, no command, no audit event.
//
// This is also what an external "cron drift detector" would have to be. Such
// a detector needs a last-fired timestamp that is persisted nowhere (lastFired
// is an in-memory map cleared every tick), and
// retention would delete a monthly playbook's history before a detector could
// use it. The scheduler already knows, at the moment of the decision, both that
// the playbook was due and why it is not running — so it reports rather than
// having something else try to infer it later.

// Skip reasons. These are stable strings: they are part of the dedupe key and
// an operator reads them straight off the row.
const (
	skipReasonCronCapExceeded = "cron_cap_exceeded"
	skipReasonInvalidCron     = "invalid_cron"
	skipReasonNoAuthor        = "no_recorded_author"
	skipReasonNoAuthzChecker  = "no_authz_checker"
	skipReasonAuthorDemoted   = "author_lost_privilege"
	skipReasonNoTargetNodes   = "no_target_nodes"
	skipReasonStartRefused    = "start_refused"
)

// recordScheduleSkip reports a playbook that was due and did not run.
//
// Takes the three fields it reports rather than a whole template: the scan that
// finds most skips deliberately never loads one (see ListScheduledCandidates),
// and a parameter that can only be satisfied by loading a graph document would
// quietly undo that.
//
// Best-effort, like every other anomaly write: this is a report ABOUT a
// decision the scheduler has already made, and failing the tick because the
// report could not be written would stop the OTHER playbooks from firing too.
func (s *Service) recordScheduleSkip(
	ctx context.Context,
	templateID uuid.UUID,
	accountID uuid.UUID,
	templateName string,
	reason string,
	severity instance_anomaly.Severity,
	summary string,
	extra map[string]interface{},
) {
	if s.anomalyRepo == nil {
		return
	}
	details := map[string]interface{}{
		"template_id":   templateID.String(),
		"template_name": templateName,
		"reason":        reason,
	}
	for k, v := range extra {
		details[k] = v
	}
	anomaly := instance_anomaly.NewWithClass(
		instance_anomaly.KindScheduledPlaybookSkipped,
		instance_anomaly.ScheduledPlaybookSkipKey(templateID, reason),
		severity,
		summary,
		details,
		&accountID,
		// A CONDITION, not an event: it persists until the operator changes
		// something (raises the cap, re-saves the playbook, assigns a node), and
		// it re-detects every minute while it lasts. As an event it would be a
		// new row per tick — 1440 a day for one misconfigured playbook.
		instance_anomaly.ClassCondition,
		instance_anomaly.StateFiring,
	)
	if err := s.anomalyRepo.Record(ctx, anomaly); err != nil {
		s.logger.WarnContext(ctx, "failed to record scheduled-playbook skip",
			"error", err, "template_id", templateID, "reason", reason)
	}
}

// resolveOverCapSkips clears the cap report for every template that was past the
// per-account cron cap on an earlier tick and is not past it now.
//
// This is the resolve path the cap skip needs and the other reasons do not.
// resolveScheduleSkips runs when a playbook FIRES, and a playbook held back by
// the cap is by definition one that never fires — so without this, reporting the
// skip would create a condition with no way out, which is the failure mode the
// whole skip-reporting design exists to avoid.
//
// It is driven by a diff against the previous tick rather than by resolving
// every under-cap playbook every minute: in the steady state (nobody near the
// cap) that is zero writes instead of one per cron playbook per tick — a
// per-tick write load this loop deliberately does not carry.
func (s *Service) resolveOverCapSkips(ctx context.Context, overCapNow map[uuid.UUID]bool) {
	for id := range s.reportedOverCap {
		if overCapNow[id] {
			continue
		}
		if s.anomalyRepo != nil {
			key := instance_anomaly.ScheduledPlaybookSkipKey(id, skipReasonCronCapExceeded)
			if err := s.anomalyRepo.ResolveCondition(ctx, key); err != nil {
				s.logger.WarnContext(ctx, "failed to resolve a cron-cap skip",
					"error", err, "template_id", id)
				// Keep it in the set so the next tick tries again; dropping it
				// here would leave the row firing forever, which is the exact
				// outcome this function exists to prevent.
				overCapNow[id] = true
			}
		}
	}
	s.reportedOverCap = overCapNow
}

// resolveScheduleSkips clears the skip conditions of a playbook that has just
// fired successfully.
//
// Without this the row stays firing forever: the condition is defined by an
// absence (it did not run), and nothing else ever observes that the absence
// ended. A detector that can fire and never resolve teaches the operator to
// ignore it.
func (s *Service) resolveScheduleSkips(ctx context.Context, templateID uuid.UUID) {
	if s.anomalyRepo == nil {
		return
	}
	for _, reason := range []string{
		skipReasonCronCapExceeded,
		skipReasonInvalidCron,
		skipReasonNoAuthor,
		skipReasonNoAuthzChecker,
		skipReasonAuthorDemoted,
		skipReasonNoTargetNodes,
		skipReasonStartRefused,
	} {
		key := instance_anomaly.ScheduledPlaybookSkipKey(templateID, reason)
		if err := s.anomalyRepo.ResolveCondition(ctx, key); err != nil {
			s.logger.WarnContext(ctx, "failed to resolve scheduled-playbook skip",
				"error", err, "template_id", templateID, "reason", reason)
		}
	}
}
