import { describe, expect, it } from 'vitest'

// formatNextRun renders through i18next; the resources must be loaded or
// t() answers the bare key.
import '@/i18n'

import type { WorkflowTemplate } from '../../api/types'
import { formatNextRun, nextRunOf, WORKFLOW_COLUMNS } from './workflowColumns'

const template = (over: Partial<WorkflowTemplate>): WorkflowTemplate => ({
  id: 'w1',
  account_id: 'a1',
  name: 'nightly',
  description: '',
  steps: [],
  created_at: '2026-08-20T00:00:00Z',
  updated_at: '2026-08-20T00:00:00Z',
  ...over,
})

const scheduleColumn = WORKFLOW_COLUMNS.find((c) => c.key === 'schedule')!

describe('the schedule column', () => {
  // The schedule an operator writes is in UTC, and the scheduler fires in UTC.
  // Rendering the next run in local time would silently disagree with the
  // expression shown beside it.
  it('renders the next run in UTC', () => {
    expect(formatNextRun('2026-09-01T03:00:00Z')).toContain('03:00 UTC')
  })

  it('does not render an unparseable time', () => {
    expect(formatNextRun('not a date')).toBe('—')
  })

  // Manual playbooks sort LAST: an empty sort value would put every one of them
  // above the schedule the operator opened this column to find.
  it('sorts the soonest run first and manual playbooks last', () => {
    const soon = Number(
      scheduleColumn.sortValue(
        template({
          schedule_cron: '0 3 * * *',
          next_run_at: '2026-08-21T03:00:00Z',
        }),
        {},
      ),
    )
    const later = Number(
      scheduleColumn.sortValue(
        template({
          schedule_cron: '0 3 * * 1',
          next_run_at: '2026-08-24T03:00:00Z',
        }),
        {},
      ),
    )
    const manual = Number(scheduleColumn.sortValue(template({}), {}))

    expect(soon).toBeLessThan(later)
    expect(later).toBeLessThan(manual)
  })

  // A schedule that can never fire has an expression but no next run, and it
  // must not sort as if it were due immediately.
  it('sorts a never-firing schedule with the manual ones', () => {
    const never = Number(
      scheduleColumn.sortValue(template({ schedule_cron: '0 0 30 2 *' }), {}),
    )
    expect(never).toBe(Infinity)
  })
})

// Widths in rem, read from the Tailwind classes the columns use.
const WIDTH_REM: Record<string, number> = {
  'w-16': 4,
  'w-20': 5,
  'w-28': 7,
  'w-36': 9,
  'w-44': 11,
  'min-w-[8rem]': 8,
}
// The name cell (w-64), the actions cell (w-32), the row's px-4 and the two
// gap-4 between those three parts.
const ROW_BASE_REM = 16 + 8 + 2 + 2
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

describe('the column breakpoints', () => {
  // At every breakpoint the columns visible there must fit beside each other,
  // or the headers overlap: the fixed columns never shrink.
  it('show a column only when everything visible fits', () => {
    for (const column of WORKFLOW_COLUMNS) {
      const at = breakpointOf(column.className)
      if (at === 0) continue
      const visible = WORKFLOW_COLUMNS.filter(
        (c) => breakpointOf(c.className) <= at,
      )
      const needed =
        ROW_BASE_REM +
        visible.reduce((sum, c) => sum + widthOf(c.className), 0) +
        COLUMN_GAP_REM * (visible.length - 1)
      expect(needed, `${column.key} at @[${at}rem]`).toBeLessThanOrEqual(at)
    }
  })
})

describe('a draft playbook', () => {
  // The scheduler runs active playbooks only, so a draft's computed next run
  // never fires; the table must not show or sort by it.
  it('has no next run, whatever its schedule', () => {
    const draft = template({
      status: 'draft',
      schedule_cron: '0 3 * * *',
      next_run_at: '2026-08-21T03:00:00Z',
    })
    expect(nextRunOf(draft)).toBeUndefined()
    expect(Number(scheduleColumn.sortValue(draft, {}))).toBe(Infinity)
    expect(nextRunOf(template({ next_run_at: '2026-08-21T03:00:00Z' }))).toBe(
      '2026-08-21T03:00:00Z',
    )
  })
})
