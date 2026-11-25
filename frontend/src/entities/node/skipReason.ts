import { t } from 'i18next'

// skipReasonText words a batch skip reason the platform names; an unknown one
// gives undefined, so the caller falls back to the server's own message.
//
// Kept apart from the API module so a test can import it without the HTTP
// client, which reaches `window` at import time.
export const skipReasonText = (reason?: string | null): string | undefined => {
  switch (reason) {
    case 'agent_too_old':
      return t('entities.node.batchCommand.skipReason.agentTooOld')
    case 'agent_already_current':
      return t('entities.node.batchCommand.skipReason.agentAlreadyCurrent')
    default:
      return undefined
  }
}
