import clsx from 'clsx'
import { ReactNode, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

// A small, self-contained table with per-column sorting and show/hide, state
// persisted per storageKey. Kept deliberately minimal (no paging/selection) —
// the credentials tabs need consistent columns, not the full node-table model.

export type Column<T> = {
  key: string
  header: string
  // Cell renderer.
  render: (row: T) => ReactNode
  // Value used for sorting; omit to make the column unsortable.
  sortValue?: (row: T) => string | number
  // Hidden by default until the user enables it in the columns menu.
  defaultHidden?: boolean
  // Never offered in the columns menu (always shown) — e.g. the identity column.
  alwaysVisible?: boolean
  className?: string
}

type SortState = { key: string; dir: 'asc' | 'desc' } | null

type DataTableProps<T> = {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string
  onRowClick?: (row: T) => void
  isSelected?: (row: T) => boolean
  // Drag support: called on dragstart to populate the dataTransfer.
  onRowDragStart?: (row: T, e: React.DragEvent) => void
  rowDraggable?: (row: T) => boolean
  storageKey: string
  empty: ReactNode
  // Translated label for the columns menu trigger (defaults to "Columns").
  columnsLabel?: string
}

const readState = <V,>(key: string, fallback: V): V => {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as V) : fallback
  } catch {
    return fallback
  }
}

export const DataTable = <T,>({
  columns,
  rows,
  rowKey,
  onRowClick,
  isSelected,
  onRowDragStart,
  rowDraggable,
  storageKey,
  empty,
  columnsLabel,
}: DataTableProps<T>) => {
  const { t } = useTranslation()
  const hiddenKey = `${storageKey}:hidden`
  const sortKey = `${storageKey}:sort`
  const [hidden, setHidden] = useState<string[]>(() =>
    readState<string[]>(
      hiddenKey,
      columns.filter((c) => c.defaultHidden).map((c) => c.key),
    ),
  )
  const [sort, setSort] = useState<SortState>(() =>
    readState<SortState>(sortKey, null),
  )
  const [menuOpen, setMenuOpen] = useState(false)

  const persistHidden = (next: string[]) => {
    setHidden(next)
    localStorage.setItem(hiddenKey, JSON.stringify(next))
  }
  const persistSort = (next: SortState) => {
    setSort(next)
    localStorage.setItem(sortKey, JSON.stringify(next))
  }

  const visible = columns.filter((c) => !hidden.includes(c.key))

  const toggleSort = (col: Column<T>) => {
    if (!col.sortValue) return
    if (sort?.key !== col.key) persistSort({ key: col.key, dir: 'asc' })
    else if (sort.dir === 'asc') persistSort({ key: col.key, dir: 'desc' })
    else persistSort(null)
  }

  const sortedRows = useMemo(() => {
    if (!sort) return rows
    const col = columns.find((c) => c.key === sort.key)
    if (!col?.sortValue) return rows
    const dir = sort.dir === 'asc' ? 1 : -1
    return [...rows].sort((a, b) => {
      const va = col.sortValue!(a)
      const vb = col.sortValue!(b)
      if (va < vb) return -dir
      if (va > vb) return dir
      return 0
    })
  }, [rows, sort, columns])

  // What the menu can actually offer. alwaysVisible columns were listed
  // DISABLED, which contradicted the flag's own comment ("never offered in the
  // columns menu") and read as broken once a control column arrived: a
  // checkbox and a Hide button have no header, so the menu showed two greyed
  // rows with no label at all.
  const toggleable = useMemo(
    () => columns.filter((c) => !c.alwaysVisible),
    [columns],
  )

  return (
    <div className="flex flex-col gap-2">
      <div className="flex justify-end">
        {/* No control when there is nothing to toggle: a trigger that opens an
            empty box is worse than no trigger. */}
        <div className={clsx('relative', toggleable.length === 0 && 'hidden')}>
          <button
            type="button"
            onClick={() => setMenuOpen((v) => !v)}
            className="flex items-center gap-1.5 rounded border border-neutral-12 px-2.5 py-1 text-xs text-neutral-5 hover:text-neutral-2 hover:border-neutral-9"
          >
            <svg
              className="h-3.5 w-3.5"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M4 6h16M4 12h16M4 18h16"
              />
            </svg>
            {columnsLabel ?? t('shared.dataTable.columns')}
          </button>
          {menuOpen ? (
            <>
              <div
                className="fixed inset-0 z-10"
                onClick={() => setMenuOpen(false)}
              />
              <div className="absolute right-0 z-20 mt-1 w-48 rounded border border-neutral-11 bg-neutral-14 py-1 shadow-lg">
                {toggleable.map((c) => {
                  const shown = !hidden.includes(c.key)
                  return (
                    <label
                      key={c.key}
                      className="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-xs text-neutral-3 hover:bg-neutral-13"
                    >
                      <input
                        type="checkbox"
                        checked={shown}
                        onChange={() =>
                          persistHidden(
                            shown
                              ? [...hidden, c.key]
                              : hidden.filter((k) => k !== c.key),
                          )
                        }
                      />
                      {c.header}
                      {/* Surface an active sort applied to a HIDDEN column, so
                          rows are not in a mystery order with no visible arrow. */}
                      {sort?.key === c.key && !shown ? (
                        <span className="ml-auto text-lime-6">
                          {sort.dir === 'asc' ? '↑' : '↓'}
                        </span>
                      ) : null}
                    </label>
                  )
                })}
              </div>
            </>
          ) : null}
        </div>
      </div>

      {rows.length === 0 ? (
        <div className="rounded border border-dashed border-neutral-12 p-8 text-center text-sm text-neutral-6">
          {empty}
        </div>
      ) : (
        <div className="overflow-x-auto rounded border border-neutral-12">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-neutral-12 text-left text-xs uppercase text-neutral-7">
                {visible.map((c) => (
                  <th
                    key={c.key}
                    onClick={() => toggleSort(c)}
                    className={clsx(
                      'px-4 py-2 font-medium',
                      c.sortValue &&
                        'cursor-pointer select-none hover:text-neutral-4',
                      c.className,
                    )}
                  >
                    <span className="inline-flex items-center gap-1">
                      {c.header}
                      {sort?.key === c.key ? (
                        <span className="text-lime-6">
                          {sort.dir === 'asc' ? '↑' : '↓'}
                        </span>
                      ) : null}
                    </span>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {sortedRows.map((row) => (
                <tr
                  key={rowKey(row)}
                  draggable={rowDraggable?.(row)}
                  onDragStart={
                    onRowDragStart ? (e) => onRowDragStart(row, e) : undefined
                  }
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                  className={clsx(
                    'border-b border-neutral-13 last:border-b-0 hover:bg-neutral-13',
                    onRowDragStart
                      ? 'cursor-grab active:cursor-grabbing'
                      : onRowClick && 'cursor-pointer',
                    isSelected?.(row) && 'bg-[#3C4B12]',
                  )}
                >
                  {visible.map((c) => (
                    <td key={c.key} className={clsx('px-4 py-2', c.className)}>
                      {c.render(row)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
