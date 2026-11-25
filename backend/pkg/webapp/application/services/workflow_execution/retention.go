package workflow_execution

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	// retentionInterval is how often the sweep runs. Deletions are idempotent
	// and batched, so the exact cadence is not critical.
	retentionInterval = 1 * time.Hour
	// retentionBatchSize bounds one delete round so a first run against a
	// year of per-minute cron history doesn't hold long row locks.
	retentionBatchSize = 500
	// retentionIDChunk bounds a single IN(...) list. One batch of executions
	// can expand to an unbounded number of execution-node ids (500 runs × a
	// large fleet), and pgx caps a statement at 65535 bind parameters — an
	// oversized delete would fail every hour on the same oldest batch and
	// stall retention permanently.
	retentionIDChunk = 10000
)

// chunkIDs splits ids into slices of at most size elements.
func chunkIDs(ids []uuid.UUID, size int) [][]uuid.UUID {
	var chunks [][]uuid.UUID
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}

// StartRetention runs the periodic executions sweep: finished (terminal-status)
// executions older than workflow.execution_retention_days are deleted together
// with their per-node state and workflow-issued command history. In-flight
// executions are never touched regardless of age. Disabled when the retention
// period is 0.
func (s *Service) StartRetention(ctx context.Context) {
	if s.cfg.ExecutionRetentionDays <= 0 {
		s.logger.Info("workflow execution retention disabled")
		return
	}
	go func() {
		s.runRetention(ctx)
		ticker := time.NewTicker(retentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runRetention(ctx)
			}
		}
	}()
	s.logger.Info("workflow execution retention started",
		"retention_days", s.cfg.ExecutionRetentionDays, "interval", retentionInterval)
}

// runRetention deletes expired executions in batches until none remain.
// Order per batch: command history -> execution nodes -> executions, so an
// interrupted sweep leaves no orphaned children (a dangling execution row is
// picked up again next round).
func (s *Service) runRetention(ctx context.Context) {
	cutoff := time.Now().Add(-time.Duration(s.cfg.ExecutionRetentionDays) * 24 * time.Hour)
	var executions, nodes, commands int64
	for {
		ids, err := s.executionRepo.ListExpiredIDs(ctx, cutoff, retentionBatchSize)
		if err != nil {
			s.logger.Warn("retention: failed to list expired executions", "error", err)
			return
		}
		if len(ids) == 0 {
			break
		}

		executionNodeIDs, err := s.executionNodeRepo.GetIDsByExecutionIDs(ctx, ids)
		if err != nil {
			s.logger.Warn("retention: failed to resolve execution nodes", "error", err)
			return
		}
		var deletedCommands int64
		for _, chunk := range chunkIDs(executionNodeIDs, retentionIDChunk) {
			n, err := s.nodeCommandRepo.DeleteByWorkflowExecutionNodeIDs(ctx, chunk)
			if err != nil {
				s.logger.Warn("retention: failed to delete command history", "error", err)
				return
			}
			deletedCommands += n
		}
		deletedNodes, err := s.executionNodeRepo.DeleteByExecutionIDs(ctx, ids)
		if err != nil {
			s.logger.Warn("retention: failed to delete execution nodes", "error", err)
			return
		}
		deletedExecutions, err := s.executionRepo.DeleteByIDs(ctx, ids)
		if err != nil {
			s.logger.Warn("retention: failed to delete executions", "error", err)
			return
		}

		executions += deletedExecutions
		nodes += deletedNodes
		commands += deletedCommands
		if len(ids) < retentionBatchSize {
			break
		}
	}
	if executions > 0 {
		s.logger.Info("retention: swept expired workflow executions",
			"executions", executions, "execution_nodes", nodes, "commands", commands, "cutoff", cutoff)
	}
}
