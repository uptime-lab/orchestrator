import { t } from 'i18next'

import type { AdoptionRefusal } from '@/entities/node/api/adoptionRefusals'

/**
 * What to say about one refused device, and which refusals are worth showing.
 *
 * A pure module so the decisions are testable without the store graph — the
 * shape `entities/node/restoreOutlook.ts` uses for the same reason.
 */
export interface RefusalOutlook {
  /** The sentence under the serial. */
  text: string
  /** Red while the device is still being turned away; amber once it is history. */
  tone: 'error' | 'warning'
}

/**
 * How long a refusal that is no longer live stays on the list. The platform's
 * record is never deleted, so without a window a device switched off in July
 * would sit on Add Node in December. A week covers "I set it up on Friday and
 * came back on Monday".
 */
export const REFUSAL_HISTORY_WINDOW_MS = 7 * 24 * 60 * 60 * 1000

/**
 * The refusals worth putting in front of the operator, newest first.
 *
 * - Live ones always: the device is retrying right now.
 * - A stale one only while the serial is STILL bound elsewhere and it happened
 *   within the window: it is the account's only note of a device that came,
 *   was refused, and went quiet.
 * - A stale one whose serial has been freed is dropped: the device is either
 *   admitted (then the backend already dropped it) or switched off, and either
 *   way there is nothing left to say.
 */
export const visibleRefusals = (
  refusals: AdoptionRefusal[],
  now: Date = new Date(),
): AdoptionRefusal[] =>
  refusals
    .filter((r) => {
      if (r.live) return true
      if (!r.still_bound_elsewhere) return false
      const lastSeen = new Date(r.last_seen_at).getTime()
      return (
        Number.isFinite(lastSeen) &&
        now.getTime() - lastSeen < REFUSAL_HISTORY_WINDOW_MS
      )
    })
    .sort((a, b) => (a.last_seen_at < b.last_seen_at ? 1 : -1))

/**
 * The sentence for one refusal. Claims a serial binding, never a device
 * movement and never who holds it — the platform withholds the owner from the
 * tenant on purpose, and the copy must not pretend to know.
 */
export const refusalOutlook = (r: AdoptionRefusal): RefusalOutlook => {
  const time = new Date(r.last_seen_at).toLocaleString()
  if (r.live && r.still_bound_elsewhere) {
    return { text: t('widgets.addNode.refused.liveBound'), tone: 'error' }
  }
  if (r.live) {
    return { text: t('widgets.addNode.refused.liveFreed'), tone: 'warning' }
  }
  return {
    text: t('widgets.addNode.refused.staleBound', { time }),
    tone: 'warning',
  }
}
