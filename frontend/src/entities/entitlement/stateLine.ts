import type { Entitlement } from './api'

// A key under widgets.subscriptionCard and its values.
export type StateLine = {
  key:
    | 'stateFree'
    | 'stateActive'
    | 'stateGrace'
    | 'stateLapsed'
    | 'stateRejected'
    | 'tenantStateActive'
    | 'tenantStateGrace'
    | 'tenantStateLimited'
  values: Record<string, string | number>
}

// stateLine is the card's first line. The operator reads the install's terms
// with their numbers. Any other account reads about the install, never a
// limit that sounds like its own, and learns who renews it.
export const stateLine = (
  e: Entitlement,
  formatDate: (iso: string | undefined) => string,
  capText: string,
): StateLine => {
  if (!e.instance_operator) {
    switch (e.state) {
      case 'active':
        return {
          key: 'tenantStateActive',
          values: { until: formatDate(e.expires_at) },
        }
      case 'grace':
        return {
          key: 'tenantStateGrace',
          values: {
            expired: formatDate(e.expires_at),
            until: formatDate(e.grace_until),
          },
        }
      default:
        return { key: 'tenantStateLimited', values: {} }
    }
  }
  switch (e.state) {
    case 'free':
      return { key: 'stateFree', values: { cap: e.free_device_cap } }
    case 'active':
      return {
        key: 'stateActive',
        values: { cap: capText, until: formatDate(e.expires_at) },
      }
    case 'grace':
      return {
        key: 'stateGrace',
        values: {
          cap: capText,
          expired: formatDate(e.expires_at),
          until: formatDate(e.grace_until),
        },
      }
    case 'lapsed':
      return {
        key: 'stateLapsed',
        values: {
          expired: formatDate(e.grace_until),
          cap: e.free_device_cap,
        },
      }
    case 'rejected':
      return { key: 'stateRejected', values: { cap: e.free_device_cap } }
  }
}
