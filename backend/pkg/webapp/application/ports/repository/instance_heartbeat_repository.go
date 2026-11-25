package repository

import (
	"context"
	"time"
)

// Component names. Stable strings: an operator console outside this tree
// matches on them, and they live here rather than beside the GORM model so a
// service can name itself without an application layer reaching into
// infrastructure.
const (
	// ComponentBackend is the HTTP/gRPC process itself.
	ComponentBackend = "backend"
	// ComponentWorkflowScheduler is the minute-granularity cron ticker.
	ComponentWorkflowScheduler = "workflow_scheduler"
)

// InstanceHeartbeatRepository records that a long-running component of the
// control plane is alive.
//
// The consumer is an operator console outside this tree, which has no way to
// ask the backend anything. See the model for what it cannot
// otherwise answer — a backend restarting in a loop looks healthy to every
// other detector, and a wedged scheduler produces no error at all.
type InstanceHeartbeatRepository interface {
	// Beat upserts one component's liveness row. startedAt is the PROCESS start
	// and must stay constant for the life of the process: it changing between
	// samples is exactly what identifies a restart.
	Beat(ctx context.Context, component string, startedAt time.Time, version string, detailCount int64) error
}
