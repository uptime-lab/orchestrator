package workflow_execution

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/cronexpr"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/instance_anomaly"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// StartScheduler runs a minute-granularity loop that fires schedule-triggered
// playbooks whose cron matches the current minute against their scoped nodes.
// A template fires at most once per minute (in-memory dedupe; a backend
// restart within the same minute may re-fire — accepted).
func (s *Service) StartScheduler(ctx context.Context) {
	s.schedulerStart = time.Now()
	go func() {
		lastFired := map[string]string{} // templateID -> "YYYY-MM-DDTHH:MM"
		// One immediate tick so a playbook due in the boot minute still fires,
		// then re-align to each minute boundary. The boundary is recomputed
		// every iteration (rather than a fixed 1-minute ticker from an
		// arbitrary boot offset), so a slow tick cannot drift the loop into
		// skipping boundaries silently.
		var prevTickMinute time.Time
		tick := func() {
			now := time.Now().UTC().Truncate(time.Minute)
			// A tick that runs longer than a minute (a wedged repository, a
			// slow StartExecution fan-out) skips minutes NOTHING else reports:
			// a cron due in a skipped minute simply never fires. Missed
			// minutes are logged, deliberately never caught up — an unattended
			// playbook fired minutes late is the same surprise as an
			// imperative command applied hours late, which is why those fail
			// fast rather than queue.
			if missed := missedMinutes(prevTickMinute, now); missed > 0 {
				s.logger.Warn("scheduler: minute(s) skipped — a tick ran long or the loop stalled; crons due in them did not fire",
					"missed_minutes", missed, "resumed_at", now.Format("2006-01-02T15:04"))
			}
			prevTickMinute = now
			s.tickScheduler(ctx, lastFired)
		}
		tick()
		for {
			next := time.Now().Truncate(time.Minute).Add(time.Minute)
			timer := time.NewTimer(time.Until(next) + 500*time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				tick()
			}
		}
	}()
	s.logger.Info("workflow scheduler started")
}

// missedMinutes reports how many whole minutes lie strictly between two
// consecutive tick minutes. 0 means no gap (same minute, or adjacent minutes);
// a zero prev means this is the first tick.
func missedMinutes(prev, now time.Time) int {
	if prev.IsZero() {
		return 0
	}
	gap := int(now.Sub(prev) / time.Minute)
	if gap <= 1 {
		return 0
	}
	return gap - 1
}

func (s *Service) tickScheduler(ctx context.Context, lastFired map[string]string) {
	now := time.Now().UTC()
	minuteKey := now.Format("2006-01-02T15:04")

	// Phase 1 — scan. A projection (id, account, name, cron) pre-filtered in
	// SQL to active templates with a cron, ordered account_id then created_at,
	// and bounded per account by the cap below. Nothing about a playbook's
	// CONTENT is read here: on almost every tick nothing is due, and this loop
	// runs once a minute for every account on the instance.
	//
	// One row past the cap is fetched on purpose — that extra row is how the
	// scan can still tell the operator an account is over the cap, which
	// filtering at exactly the cap would silently stop reporting.
	perAccountLimit := 0
	if maxCron := s.cfg.MaxCronPlaybooksPerAccount; maxCron > 0 {
		perAccountLimit = maxCron + 1
	}
	candidates, err := s.templateRepo.ListScheduledCandidates(ctx, perAccountLimit)
	if err != nil {
		s.logger.Warn("scheduler: failed to list scheduled templates", "error", err)
		return
	}
	// Report liveness AFTER the query that can wedge, and before any firing:
	// this is a "the loop reached here" signal, so beating first would keep
	// claiming health while the repository hangs. A failure to write it is not
	// allowed to stop the tick — the scheduler's job is firing playbooks, and
	// telling the console about itself is strictly secondary.
	if s.heartbeat != nil {
		if err := s.heartbeat.Beat(ctx, repository.ComponentWorkflowScheduler, s.schedulerStart, "", int64(len(candidates))); err != nil {
			s.logger.Warn("scheduler: failed to record heartbeat", "error", err)
		}
	}

	var due []uuid.UUID
	consideredPerAccount := map[uuid.UUID]int{}
	overCapNow := map[uuid.UUID]bool{}
	for _, cand := range candidates {
		if cand.Cron == "" {
			continue // defensive; the query already filters these out
		}
		// Cap how many cron playbooks one account gets to run: a tenant with
		// hundreds of scheduled templates must not own the scheduler tick (or
		// the executions table). Counted over playbooks CONSIDERED (not just
		// fired) so the honored set is stable across ticks.
		if maxCron := s.cfg.MaxCronPlaybooksPerAccount; maxCron > 0 {
			consideredPerAccount[cand.AccountID]++
			if consideredPerAccount[cand.AccountID] > maxCron {
				s.logger.Warn("scheduler: per-account cron playbook cap exceeded, ignoring the rest",
					"account_id", cand.AccountID, "cap", maxCron, "first_ignored_template_id", cand.ID)
				// The one skip the operator could not otherwise see. Every other
				// reason leaves something behind — a bad cron is at least visible
				// in the playbook — while this one is a silent decision taken
				// about a playbook that looks correctly configured, and taken
				// again every minute forever. Reported here rather than only
				// logged; resolveOverCapSkips below clears it when the playbook
				// comes back under the cap.
				//
				// The scan asks for cap+1 rows per account, so this branch is
				// entered for at most ONE template per account per tick even when
				// a tenant has hundreds over the cap. The report therefore names
				// the first ignored playbook and the cap, which is the actionable
				// pair; it is not an inventory of everything that did not run.
				overCapNow[cand.ID] = true
				s.recordScheduleSkip(ctx, cand.ID, cand.AccountID, cand.Name, skipReasonCronCapExceeded,
					instance_anomaly.SeverityWarning,
					"A scheduled playbook is not being run: the account is at its cap for cron playbooks.",
					map[string]interface{}{"cap": maxCron, "cron": cand.Cron})
				continue
			}
		}
		matches, err := cronexpr.Matches(cand.Cron, now)
		if err != nil {
			s.logger.Warn("scheduler: invalid cron", "template_id", cand.ID, "cron", cand.Cron, "error", err)
			s.recordScheduleSkip(ctx, cand.ID, cand.AccountID, cand.Name, skipReasonInvalidCron,
				instance_anomaly.SeverityWarning,
				"A scheduled playbook has a cron expression the scheduler cannot parse, so it never fires.",
				map[string]interface{}{"cron": cand.Cron, "error": err.Error()})
			continue
		}
		if !matches {
			continue
		}
		if lastFired[cand.ID.String()] == minuteKey {
			continue // already fired this minute
		}
		lastFired[cand.ID.String()] = minuteKey
		due = append(due, cand.ID)
	}

	// Phase 2 — fire. Only now is a graph document loaded, and only for the
	// playbooks that are actually due.
	if len(due) > 0 {
		templates, err := s.templateRepo.GetByIDs(ctx, due)
		if err != nil {
			s.logger.Warn("scheduler: failed to load due templates", "count", len(due), "error", err)
			// Nothing fired, so let the next tick retry them rather than
			// leaving them marked as fired for this minute.
			for _, id := range due {
				delete(lastFired, id.String())
			}
		}
		for i := range templates {
			tpl := templates[i]
			if !s.stillDue(ctx, tpl, now) {
				continue
			}
			s.fireScheduled(ctx, tpl)
		}
	}

	// Clear the cap report for anything that is no longer past it. Done after
	// firing so a playbook that both came back under the cap AND fired this
	// minute is resolved once, by whichever path gets there first — both are
	// idempotent.
	s.resolveOverCapSkips(ctx, overCapNow)

	// Entries from earlier minutes can never match again (the key is the
	// current minute) — drop them so the map doesn't grow with every template
	// that ever fired.
	for id, fired := range lastFired {
		if fired != minuteKey {
			delete(lastFired, id)
		}
	}
}

// stillDue re-asks the question phase 1 answered from the denormalized
// schedule_cron column, this time against the template's graph.
//
// The graph is the source of truth for a playbook's schedule; the column is a
// write-time copy that exists only to keep the per-minute scan cheap. So a
// candidate is a claim to be checked, not a decision: a stale column can cost a
// tick (nothing fires) but must never fire something whose graph does not say
// so. A false NEGATIVE is not new — the SQL prefilter has always been on the
// column — and would show as a playbook that stopped running.
func (s *Service) stillDue(ctx context.Context, tpl workflow_template.WorkflowTemplate, now time.Time) bool {
	if tpl.Status == workflow_template.StatusDraft {
		return false // defensive; the scan already filters drafts
	}
	graph := tpl.EffectiveGraph()
	if graph == nil {
		return false
	}
	cron := graph.ScheduleCron()
	if cron == "" {
		s.logger.Warn("scheduler: schedule_cron is set but the graph has no schedule trigger, not firing",
			"template_id", tpl.ID, "account_id", tpl.AccountID)
		return false
	}
	matches, err := cronexpr.Matches(cron, now)
	if err != nil {
		// Already reported against the column value in phase 1.
		return false
	}
	if !matches {
		s.logger.Warn("scheduler: schedule_cron disagrees with the graph, not firing",
			"template_id", tpl.ID, "account_id", tpl.AccountID, "graph_cron", cron)
		return false
	}
	return true
}

// authorMayStillFire re-checks, at fire time, that the playbook's author still
// holds the right that authorizing its content required. A cron playbook runs
// unattended with no principal, so without this a privileged playbook authored
// by a since-demoted admin would keep firing forever.
//
// Only privileged playbooks are gated — a plain one needs no special right, and
// its author losing `playbook.author` should not silently stop a schedule the
// account still wants. For privileged ones the check is FAIL-CLOSED: an unknown
// author (legacy row predating the author column) or a missing checker means
// the playbook does not fire. Re-saving it in the UI stamps the author and
// restores the schedule.
func (s *Service) authorMayStillFire(ctx context.Context, tpl workflow_template.WorkflowTemplate) bool {
	privileged := false
	for _, blockType := range tpl.BlockTypes() {
		if authz.IsPrivilegedBlock(blockType) {
			privileged = true
			break
		}
	}
	if !privileged {
		return true
	}
	if tpl.AuthorID == nil {
		s.logger.Warn("scheduler: skipping privileged playbook with no recorded author — re-save it to restore the schedule",
			"template_id", tpl.ID, "account_id", tpl.AccountID)
		// Permanent until someone acts: there is no backfill for author_id
		// anywhere, so this playbook will never fire again on its own.
		s.recordScheduleSkip(ctx, tpl.ID, tpl.AccountID, tpl.Name, skipReasonNoAuthor,
			instance_anomaly.SeverityWarning,
			"A privileged scheduled playbook has no recorded author and will never fire again until it is re-saved.",
			nil)
		return false
	}
	if s.authzChecker == nil {
		s.logger.Warn("scheduler: skipping privileged playbook, no authorization checker wired",
			"template_id", tpl.ID)
		s.recordScheduleSkip(ctx, tpl.ID, tpl.AccountID, tpl.Name, skipReasonNoAuthzChecker,
			instance_anomaly.SeverityCritical,
			"No authorization checker is wired, so every privileged scheduled playbook is refused.",
			nil)
		return false
	}
	// EVERY unit in the scope, not a representative one: a playbook spanning
	// two units fires in both, so an author who lost the right in one of them
	// must stop firing it. authorMayStillFireIn folds the set.
	if !s.authorHoldsPrivilegedInEveryOU(ctx, tpl) {
		s.logger.Warn("scheduler: skipping privileged playbook, its author no longer holds privileged authoring",
			"template_id", tpl.ID, "account_id", tpl.AccountID, "author_id", *tpl.AuthorID)
		s.recordScheduleSkip(ctx, tpl.ID, tpl.AccountID, tpl.Name, skipReasonAuthorDemoted,
			instance_anomaly.SeverityWarning,
			"A privileged scheduled playbook stopped firing: its author no longer holds privileged authoring.",
			map[string]interface{}{"author_id": tpl.AuthorID.String()})
		return false
	}
	return true
}

// authorHoldsPrivilegedInEveryOU asks the privileged-authoring question once
// per unit the playbook is scoped to, and once account-wide when it is
// unscoped. Conservative by construction: a single "no" stops the fire.
func (s *Service) authorHoldsPrivilegedInEveryOU(ctx context.Context, tpl workflow_template.WorkflowTemplate) bool {
	if !tpl.IsScoped() {
		return s.authzChecker.Can(ctx, tpl.AccountID, *tpl.AuthorID, authz.ActionPlaybookAuthorPrivileged, nil)
	}
	for i := range tpl.OUIDs {
		ouID := tpl.OUIDs[i]
		if !s.authzChecker.Can(ctx, tpl.AccountID, *tpl.AuthorID, authz.ActionPlaybookAuthorPrivileged, &ouID) {
			return false
		}
	}
	return true
}

// fireScheduled resolves a scheduled template's target nodes from its OU scope
// (all account nodes when unscoped) and starts an execution.
func (s *Service) fireScheduled(ctx context.Context, tpl workflow_template.WorkflowTemplate) {
	if !s.authorMayStillFire(ctx, tpl) {
		return
	}
	nodes, err := s.nodeRepo.GetAllByAccountID(ctx, tpl.AccountID)
	if err != nil {
		s.logger.Warn("scheduler: failed to list nodes", "template_id", tpl.ID, "error", err)
		return
	}
	// Scoped playbook targets nodes in its OU or a child OU (same subtree rule
	// StartExecution enforces); unscoped targets all.
	var allowedOUs map[uuid.UUID]bool
	if tpl.IsScoped() {
		allowedOUs, err = s.scopeTargetOUs(ctx, tpl.AccountID, tpl.OUIDs)
		if err != nil {
			s.logger.Warn("scheduler: failed to resolve playbook OU scope", "template_id", tpl.ID, "error", err)
			return
		}
	}
	cmd := workflow_execution_start.WorkflowExecutionStartCommand{
		TemplateID:  tpl.ID,
		AuthContext: commands.NewAuthContext(tpl.AccountID, uuid.Nil),
		Scheduled:   true,
	}
	for i := range nodes {
		n := nodes[i]
		if tpl.IsScoped() && (n.OUID == nil || !allowedOUs[*n.OUID]) {
			continue
		}
		cmd.NodeIDs = append(cmd.NodeIDs, n.ID)
	}
	if len(cmd.NodeIDs) == 0 {
		s.logger.Info("scheduler: no target nodes for scheduled playbook, skipping", "template_id", tpl.ID)
		s.recordScheduleSkip(ctx, tpl.ID, tpl.AccountID, tpl.Name, skipReasonNoTargetNodes,
			instance_anomaly.SeverityInfo,
			"A scheduled playbook has no target nodes in its scope, so it does nothing each time it is due.",
			nil)
		return
	}
	if _, err := s.StartExecution(ctx, cmd); err != nil {
		s.logger.Warn("scheduler: failed to start scheduled execution", "template_id", tpl.ID, "error", err)
		// The common cause is the per-account ACTIVE execution cap, i.e. earlier
		// runs that never reached completed_at — so this is usually the visible
		// symptom of a stuck run rather than a problem with this playbook.
		s.recordScheduleSkip(ctx, tpl.ID, tpl.AccountID, tpl.Name, skipReasonStartRefused,
			instance_anomaly.SeverityWarning,
			"A scheduled playbook was due but could not be started.",
			map[string]interface{}{"error": err.Error()})
		return
	}
	// It ran: clear whatever was blocking it before. A condition nothing ever
	// resolves is a condition operators learn to ignore.
	s.resolveScheduleSkips(ctx, tpl.ID)
	s.logger.Info("scheduler: started scheduled playbook", "template_id", tpl.ID, "targets", len(cmd.NodeIDs))
}
