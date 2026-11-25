package node

import (
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// failedApplyFor builds a terminally failed apply_access_config whose result
// echoes the desired hash the agent was asked for, exactly as the agent reports
// a refusal (see settleAccessApply: a failure carries the DESIRED generation).
func failedApplyFor(hash string, offset time.Duration) node_command.NodeCommand {
	cmd := cmdAt(node_command.SlugApplyAccessConfig, node_command.StatusFailed, offset)
	cmd.Result = map[string]string{accessGenerationResultKey: hash}
	return cmd
}

// The drift self-heal's attempt cap: a desired config the
// device keeps refusing must stop being re-dispatched after
// accessDriftMaxConsecutiveFailures, and resume the moment anything about the
// situation changes — the hash, or a successful apply.
func TestAccessDriftReapplyExhausted(t *testing.T) {
	apply := node_command.SlugApplyAccessConfig
	const desired = "aaaa1111"

	cases := []struct {
		name   string
		recent []node_command.NodeCommand
		want   bool
	}{
		{"no history", nil, false},
		{"one failure is not a pattern", newestFirst(
			failedApplyFor(desired, 3*time.Hour),
		), false},
		{"two failures still under the cap", newestFirst(
			failedApplyFor(desired, 3*time.Hour),
			failedApplyFor(desired, 2*time.Hour),
		), false},
		{"three consecutive failures for this hash stand the heal down", newestFirst(
			failedApplyFor(desired, 3*time.Hour),
			failedApplyFor(desired, 2*time.Hour),
			failedApplyFor(desired, 1*time.Hour),
		), true},
		// The recovery the cap promises: an entry edit changes the desired
		// hash, so the recorded failures no longer describe what would be
		// dispatched, and the re-apply must proceed.
		{"a changed desired hash re-arms the heal", newestFirst(
			failedApplyFor("stale-hash", 3*time.Hour),
			failedApplyFor("stale-hash", 2*time.Hour),
			failedApplyFor("stale-hash", 1*time.Hour),
		), false},
		{"a success breaks the streak", newestFirst(
			failedApplyFor(desired, 4*time.Hour),
			failedApplyFor(desired, 3*time.Hour),
			cmdAt(apply, node_command.StatusSuccess, 2*time.Hour),
			failedApplyFor(desired, 1*time.Hour),
		), false},
		// Superseded/expired applies never reached the device — the queue
		// retiring its own backlog must neither count as refusals nor hide the
		// real ones behind it.
		{"superseded applies are transparent to the streak", newestFirst(
			failedApplyFor(desired, 5*time.Hour),
			failedApplyFor(desired, 4*time.Hour),
			cmdAt(apply, node_command.StatusSuperseded, 3*time.Hour),
			cmdAt(apply, node_command.StatusExpired, 2*time.Hour),
			failedApplyFor(desired, 1*time.Hour),
		), true},
		{"unrelated commands between failures do not break the streak", newestFirst(
			failedApplyFor(desired, 5*time.Hour),
			cmdAt(node_command.SlugStatusCheck, node_command.StatusSuccess, 4*time.Hour),
			failedApplyFor(desired, 3*time.Hour),
			cmdAt(node_command.SlugRunScript, node_command.StatusFailed, 2*time.Hour),
			failedApplyFor(desired, 1*time.Hour),
		), true},
		// An agent that echoes no generation ties its failure to nothing, so
		// the cap conservatively keeps re-applying — the failure could belong
		// to any config, and a wrong stand-down is the harm to avoid.
		{"a failure with no echoed hash breaks the streak", newestFirst(
			failedApplyFor(desired, 4*time.Hour),
			cmdAt(apply, node_command.StatusFailed, 3*time.Hour), // no Result at all
			failedApplyFor(desired, 2*time.Hour),
			failedApplyFor(desired, 1*time.Hour),
		), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := accessDriftReapplyExhausted(tc.recent, "aaaa1111"); got != tc.want {
				t.Errorf("accessDriftReapplyExhausted() = %v, want %v", got, tc.want)
			}
		})
	}
}
