package grpc

import (
	"time"

	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"golang.org/x/time/rate"
	gproto "google.golang.org/protobuf/proto"
)

// Bounds on the SIZE of what a device reports, beside the inbound pacer's
// bound on how OFTEN it reports.
//
// The pacer counts frames, and a frame may be as large as the gRPC receive
// ceiling. A system-info report is stored twice on the node row (system_info
// and storage_devices), written to the database shared by every tenant, and
// broadcast whole to every browser of the account, so its size multiplies
// straight into database, WAL and WebSocket load. A command result is stored
// on every report that carries one.

const (
	// maxSystemInfoReportBytes is the largest system-info report accepted.
	// A real report is a few kilobytes — a handful of storage devices and
	// partitions, a short USB list, the CPU summary — so this is two orders of
	// magnitude of headroom, and still a small fraction of the frame ceiling.
	// A larger report is dropped whole, never truncated: a truncated storage
	// map would be read as "that disk is gone".
	maxSystemInfoReportBytes = 256 * 1024

	// systemInfoReportsPerSecond / systemInfoReportBurst pace system-info
	// reports on ONE stream, below the general pacer. An agent reports on
	// connect and when a command changes what it would report, so a burst of
	// five covers every honest sequence; past it a report is DELAYED, never
	// dropped, like every paced frame.
	systemInfoReportsPerSecond = 0.5
	systemInfoReportBurst      = 5
)

// newSystemInfoPacer builds one stream's system-info limiter.
func newSystemInfoPacer() *rate.Limiter {
	return rate.NewLimiter(rate.Limit(systemInfoReportsPerSecond), systemInfoReportBurst)
}

// systemInfoReportTooLarge reports a system-info update's encoded size and
// whether it exceeds maxSystemInfoReportBytes.
func systemInfoReportTooLarge(update *proto.SystemInfoUpdate) (int, bool) {
	size := gproto.Size(update)
	return size, size > maxSystemInfoReportBytes
}

// commandResultToStore returns the result a status report may write. Only a
// terminal report carries a result: the agent attaches outputs to the final
// success or failure alone, and a non-terminal report can repeat as often as
// the pacer allows, each repeat rewriting the stored result.
func commandResultToStore(status string, result map[string]string) map[string]string {
	switch status {
	case "success", "failed":
		return result
	default:
		return nil
	}
}

// warnOversizedReport logs a dropped report at most once per node and kind
// per window: the drop itself repeats at the sender's pace.
func (s *Server) warnOversizedReport(kind, nodeID string, size int) {
	if s.reportWarnings == nil {
		return
	}
	logNow, suppressed := s.reportWarnings.allow(kind+"|"+nodeID, time.Now())
	if !logNow {
		return
	}
	s.logger.Warn("dropped a device report larger than the platform stores",
		"kind", kind,
		"node_id", nodeID,
		"bytes", size,
		"max_bytes", maxSystemInfoReportBytes,
		"suppressed_since_last", suppressed,
		"window_seconds", int(logThrottleWindow.Seconds()))
}
