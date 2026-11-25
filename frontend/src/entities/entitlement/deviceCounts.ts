import type { Entitlement } from './api'

// A count the card can show: a number, or 'unknown' when the server said it
// could not read it (-1). A line the card must not show is null.
export type DeviceCount = number | 'unknown'

export type DeviceCountLines = {
  // The install-wide count, every account counted: operator only.
  install: DeviceCount | null
  // The caller's own account's count.
  account: DeviceCount | null
  // The link to get or extend an entitlement: extending the install's
  // licence is the operator's business, so only the operator sees it.
  subscriptionLink: boolean
}

const asCount = (n: number | undefined): DeviceCount | null => {
  if (typeof n !== 'number') return null
  return n >= 0 ? n : 'unknown'
}

// deviceCountLines decides which device counts the subscription card shows.
// A tenant sees only its own account's count, never the install's — even
// when an older backend still sends admitted_devices to a tenant. A field
// the server did not send is no line at all, rather than a wrong number.
export const deviceCountLines = (e: Entitlement): DeviceCountLines => ({
  install: e.instance_operator ? asCount(e.admitted_devices) : null,
  account: asCount(e.account_devices),
  subscriptionLink: e.instance_operator && Boolean(e.subscription_url),
})
