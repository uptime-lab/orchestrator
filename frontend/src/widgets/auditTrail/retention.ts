import { t } from 'i18next'

// One line saying how long this account's trail is kept, from the account
// record's read-only audit_retention_days (0 = indefinitely). Null when the
// record did not say, so nothing is claimed.
export const describeAuditRetention = (
  days: number | undefined,
): string | null => {
  if (days === undefined || days < 0) return null
  if (days === 0) return t('widgets.auditTrail.keptIndefinitely')
  return t('widgets.auditTrail.keptForDays', { count: days })
}
