// The device cap refusal, as every add-node flow receives it: a 409 whose
// body carries the code and the numbers. One reader for all of them, so the
// copy is rendered from the values rather than from the server's sentence.
export const DEVICE_CAP_REACHED_CODE = 'device_cap_reached'

// The refusal at the caller's ACCOUNT limit: its own code, its own numbers
// (the account's limit and count) and its own remedy (write to the contact).
export const ACCOUNT_DEVICE_LIMIT_REACHED_CODE = 'account_device_limit_reached'

// The admission was refused because the server could not check the device
// limit (a 503). Nothing is known about the count, so it must never render as
// the limit being reached: the next step is to try again.
export const DEVICE_CAP_UNVERIFIED_CODE = 'device_cap_unverified'

export const isDeviceCapUnverified = (error: unknown): boolean =>
  (error as RefusalLike)?.response?.data?.code === DEVICE_CAP_UNVERIFIED_CODE

// The install-wide numbers reach only the account that operates the install.
// Any other account learns that the limit is reached and its own count, never
// how many devices the whole install manages.
export type DeviceCapRefusal = {
  instanceOperator: boolean
  // Install-wide; 0 for a non-operator, whatever the server sent.
  cap: number
  count: number
  // The caller's own admitted count; null when the server did not send it.
  accountDevices: number | null
  // Operator only: extending the install's licence is the operator's business.
  subscriptionUrl: string
  // Set when the refusal is the account's own limit, not the install's cap.
  accountLimit?: AccountLimitRefusal
}

export type AccountLimitRefusal = {
  // The account's limit; null when the server did not send it.
  limit: number | null
  // The account's admitted count; null when the server did not send it.
  devices: number | null
  plan: 'free' | 'subscription'
  // When a dated subscription ends; empty otherwise.
  until: string
  // Where to write for more devices; empty when none is configured.
  contact: string
}

type RefusalLike = {
  response?: {
    status?: number
    data?: {
      code?: unknown
      data?: {
        cap?: unknown
        count?: unknown
        admitted?: unknown
        instance_operator?: unknown
        account_devices?: unknown
        subscription_url?: unknown
        limit?: unknown
        plan?: unknown
        until?: unknown
        contact?: unknown
      }
    }
  }
}

const num = (v: unknown): number | null =>
  typeof v === 'number' && Number.isFinite(v) ? v : null

const ownCount = (v: unknown): number | null => {
  const n = num(v)
  return n !== null && n >= 0 ? n : null
}

// isInstanceOperator reads the flag. A backend that predates it sends none,
// and that is read as the operator: it is what the page rendered before.
export const isInstanceOperator = (v: unknown): boolean => v !== false

// deviceCapRefusal reads the refusal out of an unknown error, or returns null
// for anything that is not one. The numbers are optional in the sense that a
// future server may omit them; the code alone is enough to render the notice.
export const deviceCapRefusal = (error: unknown): DeviceCapRefusal | null => {
  const data = (error as RefusalLike)?.response?.data
  if (data?.code === ACCOUNT_DEVICE_LIMIT_REACHED_CODE) {
    const details = data.data ?? {}
    const str = (v: unknown) => (typeof v === 'string' ? v : '')
    return {
      instanceOperator: false,
      cap: 0,
      count: 0,
      accountDevices: ownCount(details.account_devices),
      subscriptionUrl: '',
      accountLimit: {
        limit: ownCount(details.limit),
        devices: ownCount(details.account_devices),
        plan: details.plan === 'subscription' ? 'subscription' : 'free',
        until: str(details.until),
        contact: str(details.contact),
      },
    }
  }
  if (!data || data.code !== DEVICE_CAP_REACHED_CODE) return null
  const details = data.data ?? {}
  const accountDevices = ownCount(details.account_devices)
  if (!isInstanceOperator(details.instance_operator)) {
    return {
      instanceOperator: false,
      cap: 0,
      count: 0,
      accountDevices,
      subscriptionUrl: '',
    }
  }
  return {
    instanceOperator: true,
    cap: num(details.cap) ?? 0,
    count: num(details.admitted) ?? num(details.count) ?? 0,
    accountDevices,
    subscriptionUrl:
      typeof details.subscription_url === 'string'
        ? details.subscription_url
        : '',
  }
}

// A line of the notice: a key under entities.entitlement.deviceCap and its
// values.
export type DeviceCapLine = {
  key:
    | 'title'
    | 'titleNoNumbers'
    | 'titleTenant'
    | 'fleetKeepsWorking'
    | 'remedy'
    | 'accountDevices'
    | 'accountTitle'
    | 'accountTitleNoNumbers'
    | 'accountRemedy'
    | 'accountRemedyNoContact'
  values?: Record<string, number | string>
}

export type DeviceCapNoticeModel = {
  title: DeviceCapLine
  lines: DeviceCapLine[]
  // The "get an entitlement" link; empty when it must not show.
  subscriptionUrl: string
  // The account's contact, named by the accountRemedy line; set on the
  // account-limit notice only.
  contact?: string
}

// deviceCapNotice decides what the refusal says. The operator gets the
// install's numbers, the remedy and the link; anyone else gets the fact, its
// own count and who acts.
export const deviceCapNotice = (r: DeviceCapRefusal): DeviceCapNoticeModel => {
  if (r.accountLimit) {
    const a = r.accountLimit
    return {
      title:
        a.limit !== null && a.devices !== null
          ? {
              key: 'accountTitle',
              values: { devices: a.devices, limit: a.limit },
            }
          : { key: 'accountTitleNoNumbers' },
      lines: [
        { key: 'fleetKeepsWorking' },
        a.contact
          ? { key: 'accountRemedy', values: { contact: a.contact } }
          : { key: 'accountRemedyNoContact' },
      ],
      subscriptionUrl: '',
      contact: a.contact,
    }
  }
  if (!r.instanceOperator) {
    const lines: DeviceCapLine[] = [{ key: 'fleetKeepsWorking' }]
    if (r.accountDevices !== null) {
      lines.push({
        key: 'accountDevices',
        values: { devices: r.accountDevices },
      })
    }
    return {
      title: { key: 'titleTenant' },
      lines,
      subscriptionUrl: '',
    }
  }
  return {
    title:
      r.cap > 0
        ? { key: 'title', values: { admitted: r.count, cap: r.cap } }
        : { key: 'titleNoNumbers' },
    lines: [{ key: 'fleetKeepsWorking' }, { key: 'remedy' }],
    subscriptionUrl: r.subscriptionUrl,
  }
}

// The same condition as /fleet/attention reports it on a waiting node.
export type DeviceCapAttentionItem = {
  current?: string
  expected?: string
  instance_operator?: boolean
  account_devices?: number
}

// deviceCapTenantDetail is the Status row's text for a non-operator: the fact
// and the account's own count. Null for the operator, whose row keeps the
// install's numbers.
export const deviceCapTenantDetail = (
  item: DeviceCapAttentionItem,
): DeviceCapLine[] | null => {
  if (isInstanceOperator(item.instance_operator)) return null
  const lines: DeviceCapLine[] = [{ key: 'titleTenant' }]
  const own = ownCount(item.account_devices)
  if (own !== null) {
    lines.push({ key: 'accountDevices', values: { devices: own } })
  }
  return lines
}

// The account-limit condition as /fleet/attention reports it on a waiting
// node: the account's limit in expected, its own count in account_devices.
export const accountLimitAttentionDetail = (
  item: DeviceCapAttentionItem,
): DeviceCapLine => {
  const limit = Number(item.expected)
  const own = ownCount(item.account_devices)
  if (!Number.isFinite(limit) || limit <= 0 || own === null) {
    return { key: 'accountTitleNoNumbers' }
  }
  return { key: 'accountTitle', values: { devices: own, limit } }
}
