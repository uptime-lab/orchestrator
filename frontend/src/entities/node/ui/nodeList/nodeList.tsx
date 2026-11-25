import clsx from 'clsx'
import { createLocator, Locator, Mark } from 'create-locator'
import { useUnit } from 'effector-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { DeletedNode } from '@/entities/node/api/deletedNodes'
import type { NodeData } from '@/entities/node/api/types.ts'
import { useNodes } from '@/entities/node/hooks.ts'
import { $nodesLoadFailed } from '@/entities/node/model.ts'
import { NodeCard, NodeCardLocator } from '@/entities/node/ui/nodeList'
import { DeletedNodeCard } from '@/entities/node/ui/nodeList/deletedNodeCard'
import { nodeColumnsOrdered } from '@/entities/node/ui/nodeList/nodeColumns'

export type NodeListLocator = Locator<{
  card: NodeCardLocator
}>

// Active column sort; the page owns the state (sorting happens before
// pagination), the table header only renders and toggles it.
export type NodeSort = {
  key: string
  dir: 'asc' | 'desc'
}

const HIDDEN_COLUMNS_KEY = 'nodes-hidden-columns'

const loadHiddenColumns = (): Set<string> => {
  try {
    const raw = localStorage.getItem(HIDDEN_COLUMNS_KEY)
    return new Set(raw ? (JSON.parse(raw) as string[]) : [])
  } catch {
    return new Set()
  }
}

export type NodeListProps = {
  selected?: number[]
  selectedNodeId?: string
  onNodeClick?: (id: string) => unknown
  isMultiselectMode?: boolean
  selectedNodeIds?: Set<string>
  onSelectNode?: (id: string) => unknown
  onSelectAll?: () => unknown
  allSelected?: boolean
  // Whether node rows may be dragged onto an OU (selecting or editing units).
  dragEnabled?: boolean
  // Optional pre-filtered list; when omitted the full store list is used.
  nodes?: NodeData[]
  sort?: NodeSort | null
  onSortChange?: (key: string) => unknown
  // Deleted nodes whose devices may still be out there, rendered as grey rows
  // AFTER the live ones. The page decides which of them belong on this page
  // (filters, paging); the list only draws them. Empty by default, so a caller
  // that never fetched them gets the plain list.
  deletedNodes?: DeletedNode[]
  selectedDeletedNodeId?: string
  onDeletedNodeClick?: (id: string) => unknown
  // Shown instead of the default "add one" copy when the list is empty because
  // of the caller's filters, not because the account has no nodes.
  emptyMessage?: string
} & Mark<NodeListLocator>

export const NodeList = ({
  onNodeClick,
  selectedNodeId,
  isMultiselectMode = false,
  selectedNodeIds = new Set(),
  onSelectNode,
  onSelectAll,
  allSelected = false,
  dragEnabled = false,
  nodes: nodesProp,
  sort = null,
  onSortChange,
  deletedNodes = [],
  selectedDeletedNodeId,
  onDeletedNodeClick,
  emptyMessage,
  ...rest
}: NodeListProps) => {
  const { t } = useTranslation()
  const { nodes: storeNodes } = useNodes()
  const nodes = nodesProp ?? storeNodes
  // Only meaningful for the store-backed list: when the caller passes nodes in
  // (a filtered subset), an empty result is genuinely empty.
  const storeLoadFailed = useUnit($nodesLoadFailed)
  const loadFailed = nodesProp === undefined && storeLoadFailed
  const locator = createLocator(rest)

  // User-hidden columns, persisted across sessions. Container-query priority
  // hiding still applies on top for the columns that remain.
  const [hiddenColumns, setHiddenColumns] =
    useState<Set<string>>(loadHiddenColumns)
  const [columnsMenuOpen, setColumnsMenuOpen] = useState(false)
  // How many rows the drag currently in flight carries. Lives here rather than
  // in the card because the rows that need to react are the ones the cursor is
  // NOT on: a multiselect drag moves the whole selection, and dimming only the
  // dragged row made the operator believe only that row would move.
  // 0 = no drag, 1 = a single row (which already shows itself).
  const [groupDragCount, setGroupDragCount] = useState(0)

  const toggleColumn = (key: string) => {
    setHiddenColumns((prev) => {
      const next = new Set(prev)
      if (next.has(key)) {
        next.delete(key)
      } else {
        next.add(key)
      }
      localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify([...next]))
      return next
    })
  }

  const visibleColumns = useMemo(
    () => nodeColumnsOrdered().filter((c) => !hiddenColumns.has(c.key)),
    [hiddenColumns],
  )

  const setAllColumns = (hidden: boolean) => {
    const next = hidden
      ? new Set(nodeColumnsOrdered().map((c) => c.key))
      : new Set<string>()
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify([...next]))
    setHiddenColumns(next)
  }

  // A header label that toggles the column's sort (asc -> desc -> off).
  const sortHeader = (key: string, label: string) => (
    <button
      type="button"
      onClick={() => onSortChange?.(key)}
      className={clsx(
        'flex items-center gap-1 uppercase tracking-wide transition-colors',
        sort?.key === key
          ? 'text-lime-6'
          : 'text-neutral-7 hover:text-neutral-4',
      )}
    >
      {label}
      <span className="w-2">
        {sort?.key === key ? (sort.dir === 'asc' ? '↑' : '↓') : ''}
      </span>
    </button>
  )

  if (nodes.length === 0 && deletedNodes.length === 0) {
    // An empty list and a failed fetch are not the same message. Telling an
    // operator with a live fleet to "add one" is the worst possible reading of
    // a transient error — and so is telling one whose filter matched nothing.
    if (loadFailed) {
      return (
        <div className="text-center text-neutral-4 py-12" {...locator()}>
          {t('entities.nodeList.list.loadFailed')}
        </div>
      )
    }
    return (
      <div className="text-center text-neutral-4 py-12" {...locator()}>
        {emptyMessage ?? t('entities.nodeList.list.empty')}
      </div>
    )
  }

  return (
    // @container makes column visibility respond to the actual list width
    // (the details sidebar shrinks it), not the viewport.
    <div className="@container flex flex-col gap-2" {...locator()}>
      {/* Header row mirrors the row skeleton (status icon gutter + identity
          column + metric columns) so labels align with the cells below. */}
      <div
        className={clsx(
          'hidden @[30rem]:flex items-center gap-4 pl-0 pr-4 pb-1',
          'text-xxs uppercase tracking-wide text-neutral-7 select-none',
        )}
      >
        <div className="w-1 flex-shrink-0" aria-hidden />
        {isMultiselectMode && (
          <div className="flex-shrink-0">
            <input
              type="checkbox"
              checked={allSelected}
              onChange={() => onSelectAll?.()}
              aria-label={
                allSelected
                  ? t('entities.nodeList.list.deselectAll')
                  : t('entities.nodeList.list.selectAll')
              }
              className="w-4 h-4 accent-lime-6 bg-neutral-14 border-neutral-12 rounded focus:ring-lime-6 focus:ring-2 cursor-pointer"
            />
          </div>
        )}
        {/* Sized like the row's identity column: it takes what the metric
            columns leave. */}
        <div className="flex-1 basis-56 min-w-44">
          {sortHeader('name', t('entities.nodeList.list.columnNode'))}
        </div>
        {/* Matches the row's ID-button slot, so the metric columns line up. */}
        <div className="w-8 flex-shrink-0" aria-hidden />
        <div className="flex items-center gap-6 flex-none">
          {visibleColumns.map((column) => (
            <div key={column.key} className={column.className}>
              {sortHeader(column.key, column.label)}
            </div>
          ))}
        </div>

        {/* Column visibility menu */}
        <div className="relative flex-shrink-0">
          <button
            type="button"
            onClick={() => setColumnsMenuOpen((v) => !v)}
            aria-label={t('entities.nodeList.list.chooseColumns')}
            aria-expanded={columnsMenuOpen}
            className={clsx(
              'p-1 transition-colors',
              columnsMenuOpen
                ? 'text-lime-6'
                : 'text-neutral-6 hover:text-neutral-2',
            )}
          >
            <svg
              className="w-4 h-4"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M9 4v16m6-16v16M4 4h16v16H4z"
              />
            </svg>
          </button>
          {columnsMenuOpen && (
            <>
              <div
                className="fixed inset-0 z-40"
                onClick={() => setColumnsMenuOpen(false)}
              />
              <div className="absolute right-0 mt-1 z-50 bg-neutral-12 rounded py-2 min-w-40 shadow-lg normal-case tracking-normal">
                <div className="flex items-center justify-between gap-3 px-3 pb-1.5 mb-1 border-b border-neutral-11">
                  <button
                    type="button"
                    onClick={() => setAllColumns(false)}
                    className="text-xs text-neutral-5 hover:text-lime-6 uppercase"
                  >
                    {t('entities.nodeList.list.showAll')}
                  </button>
                  <button
                    type="button"
                    onClick={() => setAllColumns(true)}
                    className="text-xs text-neutral-5 hover:text-lime-6 uppercase"
                  >
                    {t('entities.nodeList.list.hideAll')}
                  </button>
                </div>
                {nodeColumnsOrdered().map((column) => (
                  <label
                    key={column.key}
                    className="flex items-center gap-2 px-3 py-1.5 text-xs text-neutral-3 hover:bg-neutral-11 cursor-pointer"
                  >
                    <input
                      type="checkbox"
                      checked={!hiddenColumns.has(column.key)}
                      onChange={() => toggleColumn(column.key)}
                      className="w-3.5 h-3.5 accent-lime-6 cursor-pointer"
                    />
                    {column.label}
                  </label>
                ))}
              </div>
            </>
          )}
        </div>
      </div>

      {nodes.map((nodeListItem) => (
        <NodeCard
          key={nodeListItem.id}
          id={nodeListItem.id}
          nodeInfo={nodeListItem}
          selected={selectedNodeId === nodeListItem.id}
          onClick={onNodeClick}
          isMultiselectMode={isMultiselectMode}
          isSelected={selectedNodeIds.has(nodeListItem.id)}
          onSelect={onSelectNode}
          dragEnabled={dragEnabled}
          // Dragging a selected node in multiselect carries the whole selection.
          dragIds={
            isMultiselectMode &&
            selectedNodeIds.size > 0 &&
            selectedNodeIds.has(nodeListItem.id)
              ? Array.from(selectedNodeIds)
              : [nodeListItem.id]
          }
          inGroupDrag={
            groupDragCount > 1 && selectedNodeIds.has(nodeListItem.id)
          }
          onGroupDragChange={setGroupDragCount}
          columns={visibleColumns}
          {...locator.card()}
        />
      ))}
      {deletedNodes.map((row) => (
        <DeletedNodeCard
          key={`deleted-${row.id}`}
          row={row}
          columns={visibleColumns}
          selected={selectedDeletedNodeId === row.id}
          onClick={(id) => onDeletedNodeClick?.(id)}
        />
      ))}
    </div>
  )
}
