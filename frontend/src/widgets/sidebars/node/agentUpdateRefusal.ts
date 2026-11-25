import { commandRefusalCode } from '@/shared/utils/commandRefusal'

type StatusLike = { response?: { status?: unknown } }

// What the Update button says when update_agent is refused. Every refusal is
// said: the server refuses before any command row exists, so nothing in the
// command list would ever show it. A node that already runs the version is
// not a failure and is said as information. A 403 was already reported by the
// API client's permission toast, so it is not said twice.
export type AgentUpdateRefusal = 'already-current' | 'refused' | 'reported'

export const agentUpdateRefusal = (error: unknown): AgentUpdateRefusal => {
  if ((error as StatusLike | undefined)?.response?.status === 403) {
    return 'reported'
  }
  if (commandRefusalCode(error) === 'agent_already_current') {
    return 'already-current'
  }
  return 'refused'
}
