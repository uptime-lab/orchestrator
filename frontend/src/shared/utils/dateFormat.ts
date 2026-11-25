import { t } from 'i18next'

export const isSameDay = (d1: Date, d2: Date) => {
  return (
    d1.getFullYear() === d2.getFullYear() &&
    d1.getMonth() === d2.getMonth() &&
    d1.getDate() === d2.getDate()
  )
}

export const getStartOfTheDay = (d1: Date, dayDiff = 0) => {
  return new Date(
    d1.getFullYear(),
    d1.getMonth(),
    d1.getDate() + dayDiff,
    0,
    0,
    0,
  )
}

export const getDateFromSeconds = (dateNumber: number) =>
  new Date(dateNumber * 1000)

const dateDifferenceUnits: {
  name: Intl.RelativeTimeFormatUnit
  factor: number
}[] = [
  { name: 'second', factor: 1 },
  { name: 'minute', factor: 60 },
  { name: 'hour', factor: 3600 },
  { name: 'day', factor: 86400 },
  { name: 'month', factor: 2592000 },
  { name: 'year', factor: 31536000 },
]

export const DateDifference = (
  timestamp1: number,
  timestamp2: number,
): { value: number; unit: Intl.RelativeTimeFormatUnit } | null => {
  const secondsDifference = Math.abs((timestamp2 - timestamp1) / 1000)

  for (let i = 0; i < dateDifferenceUnits.length; i++) {
    const nextUnit = dateDifferenceUnits[i + 1]
    if (!nextUnit || secondsDifference < nextUnit.factor) {
      return {
        value: Math.floor(secondsDifference / dateDifferenceUnits[i].factor),
        unit: dateDifferenceUnits[i].name,
      }
    }
  }

  return null
}

export const DateDifferenceString = (
  timestamp1: number,
  timestamp2: number,
  formatter: Intl.RelativeTimeFormat,
) => {
  const diff = DateDifference(timestamp2, timestamp1)

  if (!diff) {
    return t('shared.utils.date.blank')
  }

  // Determine if the difference is in the past or future
  const difference = timestamp1 > timestamp2 ? -1 : 1

  return formatter.format(diff.value * difference, diff.unit)
}

// TODO: For now, this defaults to the system setting. This should be switchable based on a user's current locale selection
export const DefaultRelativeDateFormatter = new Intl.RelativeTimeFormat(
  undefined,
  {
    numeric: 'auto',
  },
)

export const DefaultDateFormatter = new Intl.DateTimeFormat(undefined, {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
})

export const getDurationTillNow = (timestampMs?: number | null) => {
  if (!timestampMs) return Number.POSITIVE_INFINITY
  return Date.now() - timestampMs
}
