// Which serial-lease line the browser-flash tab shows. The server reports
// its configured lease in the capabilities; an older server does not, and
// the line then names the default instead of a number it cannot know.
export type SerialLeaseHint =
  { kind: 'days'; days: number } | { kind: 'unknown' } | { kind: 'none' }

export const serialLeaseHint = (
  days: number | undefined | null,
): SerialLeaseHint => {
  if (typeof days !== 'number' || !Number.isFinite(days) || days < 0) {
    return { kind: 'unknown' }
  }
  if (days === 0) return { kind: 'none' }
  return { kind: 'days', days }
}
