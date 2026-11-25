package node_command

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// expirySweepInterval is how often queued commands are checked against their
// TTL. Redelivery already expires a command when the node reconnects, so this
// sweep exists for the node that never comes back — a decommissioned or dead
// device whose queued command would otherwise sit in "registered" forever and
// read as still pending.
const expirySweepInterval = 15 * time.Minute

// expirySweepBatch bounds one round so a large backlog cannot hold long locks.
const expirySweepBatch = 200

// StartQueueExpiry runs the periodic sweep that retires queued commands past
// their TTL.
func (s *Service) StartQueueExpiry(ctx context.Context) {
	go func() {
		s.sweepExpiredQueuedCommands(ctx)
		ticker := time.NewTicker(expirySweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sweepExpiredQueuedCommands(ctx)
			}
		}
	}()
}

func (s *Service) sweepExpiredQueuedCommands(ctx context.Context) {
	commands, err := s.repo.GetExpiredQueuedCommands(ctx, time.Now(), expirySweepBatch)
	if err != nil {
		s.logger.Warn("failed to list expired queued commands", "error", err)
		return
	}
	if len(commands) == 0 {
		return
	}
	s.logger.Info("expiring queued commands past their TTL", "count", len(commands))
	for _, cmd := range commands {
		s.retireQueuedCommand(ctx, cmd, node_command.StatusExpired,
			"Command expired before the node reconnected; re-apply the current configuration")
	}
}
