package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// node_commands and audit_events both grow monotonically, on the single
// filesystem that breaks first. The only deletion path for
// commands was inside the workflow sweep, which reaches command rows only via
// their execution node — so every reboot, scan, access apply, watchdog arm and
// patch_file issued outside a playbook accumulated forever, each carrying an
// encrypted payload. audit_events had no prune at all, and a per-minute cron
// over a fleet writes one row per dispatched command.
//
// Command retention defaults to OFF (0 days): a command sweep that ran unasked
// would be a surprise. The audit trail is kept for a bounded period by default
// (a year unless AUDIT_EVENT_RETENTION_DAYS says otherwise), and an account may
// carry its own period, set by instance administration outside the tenant API;
// 0 keeps a trail forever in either place. Every positive period is held
// between audit_event.MinRetentionDays and MaxRetentionDays.

const (
	// sweepInterval matches the workflow retention job: deletions are
	// idempotent and batched, so the cadence is not critical.
	sweepInterval = time.Hour
	// batchSize bounds one delete round so a first run against a year of
	// history does not hold long row locks.
	batchSize = 500
)

// Config is the retention policy. Zero days means unlimited, for both.
// AuditEventDays is the INSTANCE default; an account's own value wins.
type Config struct {
	NodeCommandDays int
	AuditEventDays  int
}

// Service periodically prunes the two tables nothing else ever deletes from.
type Service struct {
	cfg         Config
	commands    repository.NodeCommandRepository
	auditEvents repository.AuditRetentionRepository
	logger      *slog.Logger
	now         func() time.Time
}

func NewService(
	cfg Config,
	commands repository.NodeCommandRepository,
	auditEvents repository.AuditRetentionRepository,
	logger *slog.Logger,
) *Service {
	if commands == nil || auditEvents == nil {
		panic("retention.NewService: both repositories are required")
	}
	return &Service{
		cfg:         cfg,
		commands:    commands,
		auditEvents: auditEvents,
		logger:      logger.With("service", "retention"),
		now:         time.Now,
	}
}

// Start runs the sweep hourly until ctx is cancelled. It always runs, even
// with both install periods at 0: an account's own audit period is set
// outside this process and may appear at any time, and on such an install
// the hourly cost is one read of the accounts that carry one.
func (s *Service) Start(ctx context.Context) {
	go func() {
		s.sweep(ctx)
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweep(ctx)
			}
		}
	}()
	s.logger.Info("command and audit retention started",
		"node_command_days", s.cfg.NodeCommandDays,
		"audit_event_days", s.cfg.AuditEventDays,
		"audit_event_days_effective", audit_event.EffectiveRetentionDays(nil, s.cfg.AuditEventDays),
		"interval", sweepInterval)
}

func (s *Service) sweep(ctx context.Context) {
	s.sweepNodeCommands(ctx)
	s.sweepAuditEvents(ctx)
}

// sweepNodeCommands deletes only commands in a TERMINAL state.
//
// That restriction is the whole safety of this job and is enforced in SQL
// rather than here. A `registered` command is not old history: queueable slugs
// (access config, hostname, patch_file, watchdog) sit in exactly that state for
// as long as the node stays offline, and a node can be offline for months —
// deleting one by age would silently drop work the operator asked for and the
// platform promised to deliver on reconnect. `sent` and `in_progress` are
// likewise still owed a settle.
func (s *Service) sweepNodeCommands(ctx context.Context) {
	if s.cfg.NodeCommandDays <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(s.cfg.NodeCommandDays) * 24 * time.Hour)
	var total int64
	for {
		deleted, err := s.commands.DeleteTerminalOlderThan(ctx, cutoff, batchSize)
		if err != nil {
			s.logger.WarnContext(ctx, "retention: failed to delete old node commands", "error", err)
			return
		}
		total += deleted
		if deleted < int64(batchSize) {
			break
		}
	}
	if total > 0 {
		s.logger.InfoContext(ctx, "retention: swept old device commands", "deleted", total, "cutoff", cutoff)
	}
}

// sweepAuditEvents deletes by age alone, per account.
//
// Unlike commands there is no "still owed" state to protect: an audit event
// describes something that already happened. Every account is swept on its
// own — those on the install default one by one under the default's cutoff
// (and logged together), each account with its own period under that period —
// so every statement is bounded by rows it can actually delete; a single
// install-default statement excluding the overridden accounts re-read their
// whole history on every tick. Every delete is a bounded batch, and a failure
// ends that group's sweep with a WARN — the next tick is an hour away and the
// rows are not going anywhere.
func (s *Service) sweepAuditEvents(ctx context.Context) {
	now := s.now()
	if days := audit_event.EffectiveRetentionDays(nil, s.cfg.AuditEventDays); days > 0 {
		cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
		s.sweepInstallDefault(ctx, cutoff)
	}
	overrides, err := s.auditEvents.ListAuditRetentionOverrides(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "retention: failed to read per-account audit retention; those accounts are not swept this time", "error", err)
		return
	}
	for _, o := range overrides {
		days := audit_event.EffectiveRetentionDays(&o.Days, s.cfg.AuditEventDays)
		if days <= 0 {
			continue
		}
		cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
		s.deleteInBatches(ctx, "account "+o.AccountID.String(), cutoff, func() (int64, error) {
			return s.auditEvents.DeleteAccountOlderThan(ctx, o, cutoff, batchSize)
		})
	}
}

// sweepInstallDefault sweeps every account the install default governs, one
// account at a time, and logs the group once.
func (s *Service) sweepInstallDefault(ctx context.Context, cutoff time.Time) {
	const scope = "install default"
	accountIDs, err := s.auditEvents.ListInstallDefaultAuditAccounts(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "retention: failed to list the accounts on the install default; they are not swept this time", "error", err)
		return
	}
	var total int64
	for _, accountID := range accountIDs {
		deleted, ok := s.drainBatches(ctx, scope, func() (int64, error) {
			return s.auditEvents.DeleteAccountOlderThanAtInstallDefault(ctx, accountID, cutoff, batchSize)
		})
		total += deleted
		if !ok {
			break
		}
	}
	if total > 0 {
		s.logger.InfoContext(ctx, "retention: swept old audit events", "scope", scope, "deleted", total, "cutoff", cutoff)
	}
}

// deleteInBatches repeats one bounded audit delete while batches come back
// full, and logs what it removed.
func (s *Service) deleteInBatches(ctx context.Context, scope string, cutoff time.Time, deleteBatch func() (int64, error)) {
	total, _ := s.drainBatches(ctx, scope, deleteBatch)
	if total > 0 {
		s.logger.InfoContext(ctx, "retention: swept old audit events", "scope", scope, "deleted", total, "cutoff", cutoff)
	}
}

// drainBatches repeats one bounded audit delete while batches come back full.
// It reports what it removed and whether it finished: false after a failure
// (logged here) or a cancelled context.
func (s *Service) drainBatches(ctx context.Context, scope string, deleteBatch func() (int64, error)) (int64, bool) {
	var total int64
	for {
		if ctx.Err() != nil {
			return total, false
		}
		deleted, err := deleteBatch()
		if err != nil {
			s.logger.WarnContext(ctx, "retention: failed to delete old audit events", "scope", scope, "error", err)
			return total, false
		}
		total += deleted
		if deleted < int64(batchSize) {
			return total, true
		}
	}
}
