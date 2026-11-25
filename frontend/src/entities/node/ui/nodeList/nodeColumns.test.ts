import { describe, expect, it } from 'vitest'

import '@/i18n'

import { nodeColumns } from './nodeColumns'

// Widths in rem, read from the Tailwind classes the columns use.
const WIDTH_REM: Record<string, number> = {
  'w-8': 2,
  'w-16': 4,
  'w-20': 5,
  'w-24': 6,
  'w-28': 7,
  'w-32': 8,
  'w-36': 9,
  'w-40': 10,
}
// The row's fixed parts outside the metric columns: pr-4, the status tube,
// the identity column at its 14rem basis, the ID slot (w-8), the trailing
// gutter (w-6) and the four gap-4 between them.
const ROW_BASE_REM = 1 + 0.5 + 14 + 2 + 1.5 + 4
const COLUMN_GAP_REM = 1.5 // gap-6 between columns

const widthOf = (className: string): number => {
  const cls = className.split(' ').find((c) => c in WIDTH_REM)
  if (!cls) throw new Error(`no known width in "${className}"`)
  return WIDTH_REM[cls]
}

const breakpointOf = (className: string): number => {
  const m = /@\[(\d+)rem\]:block/.exec(className)
  return m ? Number(m[1]) : 0
}

describe('the node column breakpoints', () => {
  const columns = nodeColumns()

  it('never let a metric column shrink below its width', () => {
    for (const column of columns) {
      expect(column.className, column.key).toContain('flex-shrink-0')
    }
  })

  // A column shown before everything visible fits squeezes every value down
  // to a few characters.
  it('show a column only when everything visible fits', () => {
    for (const column of columns) {
      const at = breakpointOf(column.className)
      if (at === 0) continue
      const visible = columns.filter((c) => breakpointOf(c.className) <= at)
      const needed =
        ROW_BASE_REM +
        visible.reduce((sum, c) => sum + widthOf(c.className), 0) +
        COLUMN_GAP_REM * (visible.length - 1)
      expect(needed, `${column.key} at @[${at}rem]`).toBeLessThanOrEqual(at)
    }
  })
})
