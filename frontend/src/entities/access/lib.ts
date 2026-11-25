import { t } from 'i18next'

import type { OU } from '@/entities/ou'

import type {
  AccessEntry,
  AccessSource,
  EffectiveAccessEntry,
} from './api/types'

// Rail-selection sentinels shared by the Credentials page and its sidebars:
// the two special buckets that are not real OU ids. ACCOUNT_WIDE = the
// fleet-wide default, UNASSIGNED = applies-nowhere (the safe default for a
// new entry). Kept in one place so the string literals never drift.
export const ACCESS_ACCOUNT_WIDE_FILTER = '__account_wide__'
export const ACCESS_UNASSIGNED_FILTER = '__unassigned__'
// NODE = entries bound to a single node (scope==='node'). There is no backend
// list selector for it; the page filters the account-wide `all` set client-side.
export const ACCESS_NODE_FILTER = '__node__'

// ouName resolves an OU id to its display name, falling back to the id.
export const ouName = (ous: OU[], id: string): string =>
  ous.find((o) => o.id === id)?.name ?? id

// A deduplicated CA trust placement: one per unit, plus one "All nodes" chip.
export type CAPlacement = {
  key: string
  label: string
  ouId?: string
  accountWide?: boolean
}

// caPlacements collapses a CA's trust entries into distinct placements: one
// chip per unit (regardless of how many redundant entries name it) plus one
// "All nodes" chip. A CA needs no principals now, so a unit is trusted or not —
// never twice. `t` supplies the labels for the account-wide / node buckets.
export const caPlacements = (
  trusts: AccessEntry[],
  ous: OU[],
  labels: { accountWide: string; node: string },
): CAPlacement[] => {
  const out: CAPlacement[] = []
  const seen = new Set<string>()
  let accountWide = false
  for (const ca of trusts) {
    if (ca.scope === 'account') accountWide = true
    else if (ca.scope === 'node') {
      if (!seen.has('node')) {
        seen.add('node')
        out.push({ key: 'node', label: labels.node })
      }
    } else {
      for (const id of ca.ou_ids) {
        if (!seen.has(id)) {
          seen.add(id)
          out.push({ key: id, ouId: id, label: ouName(ous, id) })
        }
      }
    }
  }
  if (accountWide) {
    out.push({ key: 'account', accountWide: true, label: labels.accountWide })
  }
  return out
}

// lockReasonText explains a locked entry in words rather than leaving a bare
// badge. The lock is reversible and the OS user still exists — saying so is
// what stops "locked" reading as "deleted".
export const lockReasonText = (
  entry: Pick<AccessEntry | EffectiveAccessEntry, 'lifecycle' | 'key_ids'>,
): string | null => {
  if (entry.lifecycle !== 'locked') return null
  // A user is locked only with no way in at all — no keys AND no password.
  // Adding either unlocks it. The OS account and home directory are kept.
  return t('entities.access.lockedReason')
}

// accessLevelLabel names the scope an effective entry was inherited from.
export const accessLevelLabel = (source: AccessSource): string => {
  switch (source.level) {
    case 'node':
      return t('entities.access.level.node')
    case 'ou':
      return t('entities.access.level.ou')
    case 'root':
      return t('entities.access.level.root')
    default:
      return t('entities.access.level.account')
  }
}
