// Expiry pickers choose a calendar DAY, and the server refuses an expiry that
// is not in the future. A day means "valid through that day" here: the value
// sent is the last moment of the chosen day in the user's time zone, and the
// earliest day offered is tomorrow, so no choice the picker allows is refused.

const pad = (n: number): string => String(n).padStart(2, '0')

// earliestExpiryDay: tomorrow as a `<input type="date">` value (YYYY-MM-DD).
export const earliestExpiryDay = (now: Date = new Date()): string => {
  const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

// expiryEndOfDay: the ISO instant of 23:59:59.999 local time on the chosen
// day, or null for a value that is not a YYYY-MM-DD day.
export const expiryEndOfDay = (day: string): string | null => {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day)
  if (!match) return null
  const [, y, m, d] = match
  const end = new Date(Number(y), Number(m) - 1, Number(d), 23, 59, 59, 999)
  return Number.isNaN(end.getTime()) ? null : end.toISOString()
}

// isSelectableExpiryDay: a day the picker offers — tomorrow or later. A typed
// value bypasses the input's `min`, so the form checks it again.
export const isSelectableExpiryDay = (
  day: string,
  now: Date = new Date(),
): boolean => expiryEndOfDay(day) !== null && day >= earliestExpiryDay(now)
