import type { AccountLimitEntitlement } from './api'

// A key under widgets.subscriptionCard.account and its values.
export type AccountLimitLine = {
  key:
    | 'freePlan'
    | 'subscriptionPlan'
    | 'subscriptionPlanUntil'
    | 'subscriptionUnlimited'
    | 'subscriptionUnlimitedUntil'
    | 'limitUnknown'
    | 'freeMore'
    | 'freeMoreNoContact'
    | 'subscriptionMore'
    | 'subscriptionMoreNoContact'
    | 'overLimit'
    | 'overLimitNoContact'
    | 'subscriptionEnds'
  values: Record<string, string | number>
}

export type AccountLimitCard = {
  // The plan line: "Free: 3 of 5 devices." — the only date is a dated
  // subscription's end.
  plan: AccountLimitLine
  // What to do for more devices; names the contact when there is one.
  remedy: AccountLimitLine | null
  // Said under a dated subscription: what happens when it ends.
  ends: AccountLimitLine | null
  // Whether remedy names the contact (rendered as a link when it is an
  // e-mail address).
  namesContact: boolean
  contact: string
}

// accountLimitCard decides what the Subscription page says to an account
// bound by the per-account device limit. It never shows the install's
// entitlement: that is the operator's, and its dates are not the account's.
export const accountLimitCard = (
  e: AccountLimitEntitlement,
  formatDate: (iso: string | undefined) => string,
  unknownCount: string,
): AccountLimitCard => {
  const contact = e.account_limit_contact ?? ''
  const devices: string | number =
    e.account_devices >= 0 ? e.account_devices : unknownCount
  const limit = e.account_device_limit
  if (limit < 0 || !e.account_plan) {
    return {
      plan: { key: 'limitUnknown', values: {} },
      remedy: null,
      ends: null,
      namesContact: false,
      contact,
    }
  }
  const until = e.account_plan_until ? formatDate(e.account_plan_until) : ''
  const subscription = e.account_plan === 'subscription'
  let plan: AccountLimitLine
  if (!subscription) {
    plan = { key: 'freePlan', values: { devices, limit } }
  } else if (limit === 0) {
    plan = until
      ? { key: 'subscriptionUnlimitedUntil', values: { devices, until } }
      : { key: 'subscriptionUnlimited', values: { devices } }
  } else {
    plan = until
      ? { key: 'subscriptionPlanUntil', values: { devices, limit, until } }
      : { key: 'subscriptionPlan', values: { devices, limit } }
  }

  const over = limit > 0 && typeof devices === 'number' && devices > limit
  const pick = (
    withContact: AccountLimitLine['key'],
    without: AccountLimitLine['key'],
  ): AccountLimitLine =>
    contact
      ? { key: withContact, values: { contact } }
      : { key: without, values: {} }
  let remedy: AccountLimitLine | null
  if (over) {
    remedy = pick('overLimit', 'overLimitNoContact')
  } else if (!subscription) {
    remedy = pick('freeMore', 'freeMoreNoContact')
  } else if (limit > 0) {
    remedy = pick('subscriptionMore', 'subscriptionMoreNoContact')
  } else {
    remedy = null
  }
  return {
    plan,
    remedy,
    ends:
      subscription && until ? { key: 'subscriptionEnds', values: {} } : null,
    namesContact: remedy !== null && Boolean(contact),
    contact,
  }
}
