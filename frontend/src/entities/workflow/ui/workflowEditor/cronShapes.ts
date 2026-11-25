// Recognising and assembling the handful of cron SHAPES the schedule builder
// offers — never evaluating one.
//
// That boundary is the whole design. What an expression means, when it next
// fires and whether it fires at all comes from the server, which runs the same
// code as the scheduler (POST /api/v1/schedule/preview). This file only turns a
// few literal field patterns into UI state and back, so it cannot disagree with
// the scheduler about semantics — it has no opinion about semantics at all.
//
// Everything it does not recognise is 'custom': the raw expression is then
// shown and edited directly and NOTHING rewrites it. A builder that re-emits
// what it half-understood is how a `1-30/2` or an OR of two day fields turns
// into something else while the operator watches.

import { t } from 'i18next'

export type CronMode =
  'interval' | 'hourly' | 'daily' | 'weekly' | 'monthly' | 'custom'

export type CronShape = {
  mode: CronMode
  /** minute of the hour (hourly/daily/weekly/monthly) */
  minute: number
  /** hour of the day, UTC (daily/weekly/monthly) */
  hour: number
  /** Go weekdays 0-6, Sunday = 0 (weekly) */
  weekdays: number[]
  /** days of the month 1-31 (monthly) */
  monthdays: number[]
  /** minutes between runs (interval) */
  intervalMinutes: number
}

export const INTERVAL_CHOICES = [5, 10, 15, 20, 30]

// A FUNCTION, not a const array: the labels come from i18next, which is not
// initialised when this module is first evaluated (same reasoning as
// nodeColumns.tsx).
export const weekdayLabels = (): Array<{
  value: number
  short: string
  long: string
}> => [
  {
    value: 1,
    short: t('entities.workflow.cronField.weekdays.shortMo'),
    long: t('entities.workflow.cronField.weekdays.monday'),
  },
  {
    value: 2,
    short: t('entities.workflow.cronField.weekdays.shortTu'),
    long: t('entities.workflow.cronField.weekdays.tuesday'),
  },
  {
    value: 3,
    short: t('entities.workflow.cronField.weekdays.shortWe'),
    long: t('entities.workflow.cronField.weekdays.wednesday'),
  },
  {
    value: 4,
    short: t('entities.workflow.cronField.weekdays.shortTh'),
    long: t('entities.workflow.cronField.weekdays.thursday'),
  },
  {
    value: 5,
    short: t('entities.workflow.cronField.weekdays.shortFr'),
    long: t('entities.workflow.cronField.weekdays.friday'),
  },
  {
    value: 6,
    short: t('entities.workflow.cronField.weekdays.shortSa'),
    long: t('entities.workflow.cronField.weekdays.saturday'),
  },
  {
    value: 0,
    short: t('entities.workflow.cronField.weekdays.shortSu'),
    long: t('entities.workflow.cronField.weekdays.sunday'),
  },
]

export const DEFAULT_SHAPE: CronShape = {
  mode: 'custom',
  minute: 0,
  hour: 3,
  weekdays: [1],
  monthdays: [1],
  intervalMinutes: 15,
}

const intInRange = (text: string, min: number, max: number): number | null => {
  if (!/^\d{1,2}$/.test(text)) return null
  const value = Number(text)
  return value >= min && value <= max ? value : null
}

// A comma list of plain integers, e.g. "1,15" or "2". Ranges and steps are
// deliberately NOT accepted: they are expressible in the field and not in the
// chips, so they belong to 'custom'.
const intList = (text: string, min: number, max: number): number[] | null => {
  const parts = text.split(',')
  const out: number[] = []
  for (const part of parts) {
    const value = intInRange(part.trim(), min, max)
    if (value === null) return null
    out.push(value)
  }
  return out
}

/**
 * parseCron recognises the shapes the builder can render, and returns
 * mode 'custom' for everything else — including an empty or malformed string.
 */
export const parseCron = (expression: string): CronShape => {
  const fields = expression.trim().split(/\s+/)
  const custom = { ...DEFAULT_SHAPE, mode: 'custom' as CronMode }
  if (fields.length !== 5) return custom

  const [minuteField, hourField, domField, monthField, dowField] = fields
  // A restricted month is a shape the chips cannot show, so it stays raw.
  if (monthField !== '*') return custom

  // Every N minutes: */N, with N one of the offered choices. A step the
  // builder does not offer would otherwise be silently rounded to one it does.
  const stepMatch = /^\*\/(\d{1,2})$/.exec(minuteField)
  if (stepMatch && hourField === '*' && domField === '*' && dowField === '*') {
    const step = Number(stepMatch[1])
    if (INTERVAL_CHOICES.includes(step)) {
      return { ...DEFAULT_SHAPE, mode: 'interval', intervalMinutes: step }
    }
    return custom
  }

  const minute = intInRange(minuteField, 0, 59)
  if (minute === null) return custom

  if (hourField === '*' && domField === '*' && dowField === '*') {
    return { ...DEFAULT_SHAPE, mode: 'hourly', minute }
  }

  const hour = intInRange(hourField, 0, 23)
  if (hour === null) return custom

  if (domField === '*' && dowField === '*') {
    return { ...DEFAULT_SHAPE, mode: 'daily', minute, hour }
  }

  // Exactly ONE of the two day fields may be restricted here. When both are,
  // cron ORs them (crontab(5)) — a meaning the chips cannot show and the
  // operator would misread, so it stays raw.
  if (domField === '*' && dowField !== '*') {
    const weekdays = intList(dowField, 0, 7)
    if (!weekdays) return custom
    // 7 and 0 are both Sunday; fold and de-duplicate.
    const folded = Array.from(
      new Set(weekdays.map((d) => (d === 7 ? 0 : d))),
    ).sort()
    return { ...DEFAULT_SHAPE, mode: 'weekly', minute, hour, weekdays: folded }
  }

  if (dowField === '*' && domField !== '*') {
    const monthdays = intList(domField, 1, 31)
    if (!monthdays) return custom
    return {
      ...DEFAULT_SHAPE,
      mode: 'monthly',
      minute,
      hour,
      monthdays: Array.from(new Set(monthdays)).sort((a, b) => a - b),
    }
  }

  return custom
}

/**
 * buildCron assembles the expression for a shape. 'custom' has no assembled
 * form — the caller keeps the raw string as typed.
 */
export const buildCron = (shape: CronShape): string | null => {
  switch (shape.mode) {
    case 'interval':
      return `*/${shape.intervalMinutes} * * * *`
    case 'hourly':
      return `${shape.minute} * * * *`
    case 'daily':
      return `${shape.minute} ${shape.hour} * * *`
    case 'weekly': {
      if (shape.weekdays.length === 0) return null
      const days = [...shape.weekdays].sort().join(',')
      return `${shape.minute} ${shape.hour} * * ${days}`
    }
    case 'monthly': {
      if (shape.monthdays.length === 0) return null
      const days = [...shape.monthdays].sort((a, b) => a - b).join(',')
      return `${shape.minute} ${shape.hour} ${days} * *`
    }
    default:
      return null
  }
}
