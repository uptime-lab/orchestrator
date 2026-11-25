import { describe, expect, it } from 'vitest'

import '@/i18n'

import { imageColumns } from './imageColumns'

// Widths in rem, read from the Tailwind classes the columns use.
const WIDTH_REM: Record<string, number> = {
  'w-24': 6,
  'w-28': 7,
  'w-32': 8,
  'min-w-[8rem]': 8,
}
// The name cell (w-64), the row's px-4 and the gap-4 after the name.
const ROW_BASE_REM = 16 + 2 + 1
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

describe('the image column breakpoints', () => {
  // The fixed columns never shrink, so a column shown before everything
  // visible fits makes the headers run into each other.
  it('show a column only when everything visible fits', () => {
    const columns = imageColumns()
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
