package node

// AgentInstanceContinuity says how the agent process behind a reconnecting
// stream relates to the process behind the previous one. It is computed from
// the agent_instance_id in ConnectionInit — a fresh random id per agent
// process start — and it exists for exactly one consumer: the stale-command
// sweep that runs on reconnect. "The stream blipped" and "the agent restarted"
// are indistinguishable from the platform's side without it, which is why that
// sweep otherwise has to guess with a grace period.
type AgentInstanceContinuity int

const (
	// AgentInstanceUnknown: one side or the other reported no instance id (an
	// agent below the field's introduction, or a node that never stored one).
	// The sweep falls back to the grace-period heuristic — today's behaviour.
	AgentInstanceUnknown AgentInstanceContinuity = iota
	// AgentInstanceSame: the same process reconnected. Its in_progress commands
	// are still being executed and will be reported; failing them would report
	// a failure over work that is actually happening.
	AgentInstanceSame
	// AgentInstanceChanged: a different process. Execution does not resume
	// across agent restarts, so the previous session's in_progress work is
	// certainly lost and can be failed immediately, not after a grace period.
	AgentInstanceChanged
)

// AgentInstanceContinuityOf compares the stored previous instance id with the
// one the connecting agent reports. Empty on either side is Unknown, never a
// change: "no information" must not be read as "restarted", or every agent
// below the field's floor would have its in_progress commands failed instantly
// on every reconnect.
func AgentInstanceContinuityOf(previous, current string) AgentInstanceContinuity {
	if previous == "" || current == "" {
		return AgentInstanceUnknown
	}
	if previous == current {
		return AgentInstanceSame
	}
	return AgentInstanceChanged
}
