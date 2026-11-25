import { t } from 'i18next'

import type { DeletedNode } from '@/entities/node/api/deletedNodes'

/**
 * What a restore will actually achieve for one deleted node, and whether to
 * offer it at all.
 *
 * A pure module rather than a closure inside the panel, so the decision is
 * testable without the store graph — the shape `widgets/auditTrail/describe.ts`
 * uses for the same reason.
 */
export interface RestoreAffordance {
  /** The sentence shown under the row. */
  text: string
  /** Whether the row's caveat is a warning or a plain note. */
  tone: 'neutral' | 'warning'
  /** Whether the restore can work at all. False disables the offer. */
  canRestore: boolean
  /** Why the offer is disabled, when the generic reason would be wrong. */
  blockedReason?: string
}

/**
 * How recently a refused connect counts as "the device is trying now" rather
 * than history. A stranded agent redials about once a second, so a device that
 * is powered and on the network stamps the record continuously.
 */
export const DEVICE_TRYING_WINDOW_MS = 60 * 60 * 1000

/** Whether the device presented this node's credential within the window. */
export const deviceStillTrying = (
  row: Pick<DeletedNode, 'last_refused_at'>,
  now: number = Date.now(),
): boolean => {
  if (!row.last_refused_at) return false
  const at = new Date(row.last_refused_at).getTime()
  return Number.isFinite(at) && now - at <= DEVICE_TRYING_WINDOW_MS
}

const fmt = (iso: string) => new Date(iso).toLocaleString()

/**
 * The affordance for one row. Precedence, top first:
 *
 * 0. `serial_bound_in_account` — the board is another live node of this
 *    account — disables the offer and names that node.
 * 1. `serial_bound_elsewhere` overrides everything else, including the adoption
 *    method: where the identity lives stops mattering once the identity cannot
 *    be rebound. The backend refuses such a restore outright (409), so offering
 *    it here could only walk the operator into a refusal. That precedence was
 *    the point of the whole module: the `eeprom` sentence is the most confident
 *    one — "restoring is enough, it rejoins on its own" — and shown for a
 *    device that has moved to another account it is exactly wrong.
 * 2. The device REPORTED erasing its adoption file (`identity_released_at`).
 *    What it comes back with, if anything, is now the EEPROM — and only when
 *    the platform itself flashed this node's identity there
 *    (`eeprom_bake_confirmed`). A disk-first boot order (`boot_order_netboots_first`
 *    false) means the EEPROM identity is presented only if the local agent
 *    falls back to it, which depends on the agent; the sentence says so rather
 *    than promising.
 * 3. The release was requested and never confirmed: the wording says asked,
 *    not did (the terminal status arrives after the node row is gone).
 * 4. No release: keyed on the adoption method, with `eeprom` split by whether
 *    the bake is confirmed. HTTP_HOST alone proves the EEPROM points at a
 *    platform, not that it carries this node's token.
 *
 * `undefined` reads as false throughout: an older backend omits the newer
 * fields, and "not known" must never render as the stronger promise.
 */
export const restoreAffordance = (row: DeletedNode): RestoreAffordance => {
  // The board is now another live node of this account; the backend refuses
  // the restore (409) until that node is deleted. Checked first: naming the
  // own node is the actionable answer even when another account holds the
  // serial as well.
  if (row.serial_bound_in_account) {
    const text = t('entities.nodeList.deletedRow.outlook.boundInAccount', {
      name: row.serial_bound_in_account.name,
    })
    return { text, tone: 'warning', canRestore: false, blockedReason: text }
  }
  if (row.serial_bound_elsewhere) {
    return {
      text: t('entities.nodeList.deletedRow.outlook.boundElsewhere'),
      tone: 'warning',
      canRestore: false,
    }
  }

  const baked = row.eeprom_bake_confirmed === true
  const netbootsFirst = row.boot_order_netboots_first

  if (row.identity_released_at) {
    const when = fmt(row.identity_released_at)
    if (baked && netbootsFirst === true) {
      return {
        text: t('entities.nodeList.deletedRow.outlook.releasedEepromNetboot', {
          when,
        }),
        tone: 'neutral',
        canRestore: true,
      }
    }
    if (baked) {
      return {
        text: t('entities.nodeList.deletedRow.outlook.releasedEepromDisk', {
          when,
        }),
        tone: 'warning',
        canRestore: true,
      }
    }
    return {
      text: t('entities.nodeList.deletedRow.outlook.releasedConfirmed', {
        when,
      }),
      tone: 'warning',
      canRestore: true,
    }
  }

  const base =
    row.adoption_method === 'eeprom'
      ? baked
        ? t('entities.nodeList.deletedRow.outlook.eeprom')
        : t('entities.nodeList.deletedRow.outlook.eepromUnconfirmed')
      : row.adoption_method === 'file'
        ? t('entities.nodeList.deletedRow.outlook.file')
        : t('entities.nodeList.deletedRow.outlook.unknown')

  if (!row.identity_release_requested) {
    return { text: base, tone: 'neutral', canRestore: true }
  }
  if (baked) {
    // The EEPROM copy is untouched by the release. Whether the device USES it
    // on a local-disk boot depends on the agent, so this stays a footnote only
    // for a netboot-first device; a disk-first one gets the warning.
    if (netbootsFirst === true) {
      return {
        text: `${base} ${t('entities.nodeList.deletedRow.outlook.releasedEeprom')}`,
        tone: 'neutral',
        canRestore: true,
      }
    }
    return {
      text: `${base} ${t('entities.nodeList.deletedRow.outlook.releasedEepromDiskRequested')}`,
      tone: 'warning',
      canRestore: true,
    }
  }
  // The release is requested and never confirmed (the command's terminal status
  // arrives after the node row is gone), so the wording says asked, not did.
  return {
    text: `${base} ${t('entities.nodeList.deletedRow.outlook.releasedFile')}`,
    tone: 'warning',
    canRestore: true,
  }
}
