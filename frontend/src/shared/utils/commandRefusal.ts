import { t } from 'i18next'

type RefusalLike = {
  response?: { data?: { code?: unknown; error?: unknown; message?: unknown } }
}

// commandRefusalCode is the machine-readable code of a refusal. It arrives in
// `code` on most routes and in `error` on the generic command route, which
// keeps its older body shape. Empty when the error carries neither.
export const commandRefusalCode = (error: unknown): string => {
  const data = (error as RefusalLike | undefined)?.response?.data
  if (!data) return ''
  if (typeof data.code === 'string' && data.code) return data.code
  return typeof data.error === 'string' ? data.error : ''
}

// commandRefusalText words the node-state refusals a device command can meet.
// The server's own sentences name ids, statuses and request fields; these name
// the operator's next step. Anything else gives undefined, and the caller
// shows the server's message.
export const commandRefusalText = (error: unknown): string | undefined => {
  const data = (error as RefusalLike | undefined)?.response?.data
  if (!data) return undefined
  const code = commandRefusalCode(error)
  switch (code) {
    case 'node_unavailable':
      return t('shared.commandRefusal.nodeUnavailable')
    case 'node_not_approved':
      return t('shared.commandRefusal.nodeNotApproved')
    case 'node_not_connected':
      return t('shared.commandRefusal.nodeNotConnected')
    // The generic command route names this case by its message only.
    case 'service_unavailable':
      return data.message === 'node is not connected'
        ? t('shared.commandRefusal.nodeNotConnected')
        : undefined
    case 'agent_already_current':
      return t('shared.commandRefusal.agentAlreadyCurrent')
    case 'recovery_unreachable':
      return t('shared.commandRefusal.recoveryUnreachable')
    default:
      return undefined
  }
}
