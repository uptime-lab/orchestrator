// Power and thermal facts for a node, decoded from `vcgencmd get_throttled`.
//
// Lives in entities/ rather than in the node sidebar because both the fleet list
// and the node Overview read it, and the fleet list (an entity) cannot import
// from a widget. The board classifier next door exists for the same reason, after
// four copies of it disagreed about what a node was.
//
// Under-voltage is the classic Raspberry Pi failure mode, reported by agents
// from 0.5.4 on. Without it, a node fed from a computer's USB port browns out,
// its USB disk stops answering, and the only thing the UI offers is a `wipefs`
// internal that points at the disk — a disk that was never faulty.

import { t } from 'i18next'

import type { PowerInfo } from '@/entities/node/api/types'
import type { NodeData } from '@/entities/node/ui/nodeList'

// A power/thermal reading rendered for display. `tone` drives the colour, `text`
// is the value, `title` is the explanation on hover.
export type PowerLine = {
  text: string
  title: string
  tone: 'ok' | 'warn' | 'bad'
  // True when the SUPPLY is implicated (now or since boot) — the fleet list shows
  // a pill for this and not for thermal throttling, because it explains failures
  // nothing else on the node can explain.
  supplyProblem: boolean
}

// powerSummary turns the decoded flags into one line.
//
// Two collapses are deliberately avoided, and both matter more than they look:
//
//   - Live and sticky stay distinct. A node that browned out once under a heavy
//     install would otherwise stay red until it reboots, and a signal that is
//     always lit stops being a signal — the lesson the watchdog disarm_source
//     work already paid for.
//   - Power and thermal stay distinct. Under-voltage means the supply or the
//     cable; a soft temperature limit means cooling. One "throttled" badge
//     destroys the difference between buying a PSU and buying a heatsink.
//
// Returns null when the node reported nothing at all, so callers omit the row
// rather than showing a green "OK" they have no evidence for. `power` is absent
// on non-Pi hardware, where vcgencmd fails, and on agents older than 0.5.4.
export const powerSummary = (power?: PowerInfo): PowerLine | null => {
  if (!power) return null

  const live: string[] = []
  if (power.under_voltage_now) live.push(t('entities.node.power.underVoltage'))
  if (power.soft_temp_limit_now)
    live.push(t('entities.node.power.overTemperature'))
  if (power.throttled_now) live.push(t('entities.node.power.throttled'))
  if (power.freq_capped_now) live.push(t('entities.node.power.clockCapped'))

  const since: string[] = []
  if (power.under_voltage_since_boot)
    since.push(t('entities.node.power.underVoltage'))
  if (power.soft_temp_limit_since_boot)
    since.push(t('entities.node.power.overTemperature'))
  if (power.throttled_since_boot) since.push(t('entities.node.power.throttled'))
  if (power.freq_capped_since_boot)
    since.push(t('entities.node.power.clockCapped'))

  const supplyProblem = Boolean(
    power.under_voltage_now || power.under_voltage_since_boot,
  )

  if (live.length === 0 && since.length === 0) {
    return {
      text: t('entities.node.power.ok'),
      title: t('entities.node.power.okTitle'),
      tone: 'ok',
      supplyProblem: false,
    }
  }

  // Live conditions outrank sticky ones: one is happening, the other happened.
  const text =
    live.length > 0
      ? live.join(', ')
      : t('entities.node.power.sinceBoot', { conditions: since.join(', ') })

  const explanation = supplyProblem
    ? t('entities.node.power.supplyExplanation')
    : t('entities.node.power.thermalExplanation')

  const timing =
    live.length > 0
      ? t('entities.node.power.happeningNow')
      : t('entities.node.power.happenedSinceBoot')

  return {
    text,
    title: t('entities.node.power.title', {
      explanation,
      timing,
      mask: power.throttled_flags.toString(16),
    }),
    tone: live.length > 0 ? 'bad' : 'warn',
    supplyProblem,
  }
}

// nodePowerLine reads the facts off a node row, for callers that hold a node
// rather than the nested hardware object.
export const nodePowerLine = (node: NodeData): PowerLine | null =>
  powerSummary(node.system_info?.hardware?.power)
