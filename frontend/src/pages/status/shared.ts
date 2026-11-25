import { getRouteApi } from '@tanstack/react-router'

import type { AttentionSeverity } from '@/entities/fleet'

import type { BadgeColor } from '@/shared/ui/badge'

export const routeApi = getRouteApi('/_mainLayout/status')

// Two levels, and the split is "can this device still be reached from here".
// Critical is offline and the three ways a node stops being recoverable at
// all; everything else is work an operator should get to.
export const severityColor = (severity: AttentionSeverity): BadgeColor =>
  severity === 'critical' ? 'red' : 'yellow'

// A comma-separated URL filter, read back as a set. Empty means "no filter",
// never "match nothing" — the same distinction the server draws on its
// visibility set.
export const parseCsv = (raw: string | undefined): string[] =>
  raw
    ? raw
        .split(',')
        .map((v) => v.trim())
        .filter(Boolean)
    : []

export const toCsv = (values: string[]): string | undefined =>
  values.length ? values.join(',') : undefined

// Windows the log's time filter offers. "all" sends no `since` at all rather
// than a very old timestamp, so the query stays an index range scan.
export const SINCE_WINDOWS = ['24h', '7d', '30d', 'all'] as const
export type SinceWindow = (typeof SINCE_WINDOWS)[number]

const WINDOW_HOURS: Record<Exclude<SinceWindow, 'all'>, number> = {
  '24h': 24,
  '7d': 24 * 7,
  '30d': 24 * 30,
}

export const sinceTimestamp = (window: SinceWindow): string | undefined => {
  if (window === 'all') {
    return undefined
  }
  const from = new Date(Date.now() - WINDOW_HOURS[window] * 60 * 60 * 1000)
  return from.toISOString()
}
