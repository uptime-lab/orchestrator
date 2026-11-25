import clsx from 'clsx'
import { FC, ReactNode, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useOUs, type OU } from '@/entities/ou'

// Shared selection sentinels persisted in the URL alongside OU ids.
export const OU_FILTER_ALL = 'all'
export const OU_FILTER_UNASSIGNED = 'unassigned'
export const OU_FILTER_FAVORITES = 'favorites'

// dataTransfer key for dragging an OU row itself (edit-mode reparent/reorder).
const OU_DND_TYPE = 'text/ou-id'

// A non-OU row above the tree (All / Favorites / Unassigned / Not attached).
export type PseudoRow = {
  value: string
  label: string
  count: number
  tone?: 'default' | 'warn'
  // When set, the row is a drop target for assignment; the dropped item is
  // dispatched with this target value (null = clear/unassign).
  dropTarget?: string | null
}

export type OURailProps = {
  // Per-OU (leaf) counts keyed by OU id; roots roll up their children.
  counts: Record<string, number>
  // Disable the root roll-up and show counts[root.id] as-is. Needed when the
  // page's counts are not disjoint memberships but overlapping sets (e.g.
  // playbook availability: a global playbook is counted in EVERY unit, and a
  // run's subtree matching already includes children) — rolling up would
  // double-count and show a root number larger than the page total.
  rawRootCounts?: boolean
  selected: string
  onSelect: (value: string) => void
  pseudoRows?: PseudoRow[]
  // The level ABOVE the top-level units, drawn as the tree's own top node with
  // the units indented beneath it. The Organization page passes "Account
  // defaults" here: the account policy is the one root of the chain (it
  // reaches every unit and the unassigned nodes), and drawn as a sibling row
  // it read as one more unit. In edit mode a nested unit dropped on it becomes
  // top-level.
  treeParent?: PseudoRow
  // Accepted dataTransfer types for assignment drops onto OU rows (and any
  // pseudo-row with a dropTarget). The matched type is passed to onDropItem.
  dropTypes?: string[]
  onDropItem?: (itemId: string, ouId: string | null, type: string) => void
  // Accept assignment drops even when the rail is not in edit mode. Pages set
  // this while their own selection mode is active (node multiselect), so a
  // group of nodes can be dropped onto a unit without editing units.
  alwaysAcceptDrops?: boolean
  // Edit mode (OU create/delete/reparent/reorder) — ON for the whole life of
  // the rail when set; there is no in-rail toggle. Only the Organization page
  // sets it (for `ou.manage` holders): every other rail is a filter tree plus
  // assignment drops, and unit management is one click away on that page.
  // Selecting a row IS the unit's editor there, so the rail has no pencil.
  editable?: boolean
  onCreate?: () => void
  onDelete?: (ouId: string) => void
  onReparent?: (ouId: string, parentId: string | null) => void
  onReorder?: (orderedIds: string[]) => void
  // Extra sections rendered below the tree (e.g. the secrets Tags section).
  sections?: ReactNode
  // One-line hint under the tree explaining what a drop does on this page.
  dropHint?: string
  // Collapse the whole rail (the page owns the collapsed state).
  onCollapse?: () => void
}

const TrashIcon = () => (
  <svg className="w-3.5 h-3.5" viewBox="0 0 20 20" fill="currentColor">
    <path
      fillRule="evenodd"
      d="M9 2a1 1 0 00-.894.553L7.382 4H4a1 1 0 000 2v10a2 2 0 002 2h8a2 2 0 002-2V6a1 1 0 100-2h-3.382l-.724-1.447A1 1 0 0011 2H9zM7 8a1 1 0 012 0v6a1 1 0 11-2 0V8zm5-1a1 1 0 00-1 1v6a1 1 0 102 0V8a1 1 0 00-1-1z"
      clipRule="evenodd"
    />
  </svg>
)

type RowProps = {
  label: string
  count: number
  selected: boolean
  tone?: 'default' | 'warn'
  editMode?: boolean
  branch?: { expanded: boolean; onToggle: () => void }
  onSelect: () => void
  onDelete?: () => void
  // Whether this row accepts assignment drops right now (edit mode, or a page
  // override such as node multiselect). Kept separate from editMode so the
  // edit/delete affordances stay gated on edit mode alone.
  dropEnabled?: boolean
  // Assignment drop target (an OU id, or null for a clear/unassign row).
  dropTarget?: string | null
  dropTypes?: string[]
  onDropItem?: (itemId: string, target: string | null, type: string) => void
  // OU drag (edit mode): draggable, reparent-onto, reorder-before.
  draggableOU?: boolean
  onDragStartOU?: () => void
  onReparentOnto?: () => void
  onReorderBefore?: () => void
}

const TreeRow: FC<RowProps> = ({
  label,
  count,
  selected,
  tone = 'default',
  editMode = false,
  branch,
  onSelect,
  onDelete,
  dropEnabled = false,
  dropTarget,
  dropTypes = [],
  onDropItem,
  draggableOU = false,
  onDragStartOU,
  onReparentOnto,
  onReorderBefore,
}) => {
  const { t } = useTranslation()
  // Drag-to-assign is gated by dropEnabled (edit mode, or a page override like
  // node multiselect). OU reorder/reparent stays gated via draggableOU.
  const acceptsItem =
    dropEnabled &&
    dropTarget !== undefined &&
    !!onDropItem &&
    dropTypes.length > 0
  const [dragOver, setDragOver] = useState<false | 'body' | 'before'>(false)

  const matchType = (types: readonly string[]) =>
    dropTypes.find((candidate) => types.includes(candidate))

  const handleDragOver = (e: React.DragEvent<HTMLDivElement>) => {
    const types = e.dataTransfer.types
    const isOU = types.includes(OU_DND_TYPE)
    const itemType = acceptsItem ? matchType(types) : undefined
    if (!isOU && !itemType) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    if (isOU) {
      const rect = e.currentTarget.getBoundingClientRect()
      const before = e.clientY - rect.top < rect.height / 3
      setDragOver(before && onReorderBefore ? 'before' : 'body')
    } else {
      setDragOver('body')
    }
  }

  const handleDrop = (e: React.DragEvent<HTMLDivElement>) => {
    const mode = dragOver
    setDragOver(false)
    if (e.dataTransfer.getData(OU_DND_TYPE)) {
      e.preventDefault()
      if (mode === 'before') onReorderBefore?.()
      else onReparentOnto?.()
      return
    }
    const itemType = acceptsItem ? matchType(e.dataTransfer.types) : undefined
    if (itemType) {
      const itemId = e.dataTransfer.getData(itemType)
      if (!itemId) return
      e.preventDefault()
      onDropItem?.(itemId, dropTarget ?? null, itemType)
    }
  }

  return (
    <div
      draggable={draggableOU}
      onDragStart={(e) => {
        if (!draggableOU) return
        e.dataTransfer.setData(OU_DND_TYPE, '1')
        e.dataTransfer.effectAllowed = 'move'
        onDragStartOU?.()
      }}
      onDragOver={handleDragOver}
      onDragLeave={() => setDragOver(false)}
      onDrop={handleDrop}
      className={clsx(
        'group relative flex items-center rounded transition-colors',
        dragOver === 'body'
          ? 'bg-lime-6/10 ring-1 ring-lime-6'
          : selected
            ? 'bg-neutral-12'
            : 'hover:bg-neutral-13',
        draggableOU && 'cursor-grab active:cursor-grabbing',
      )}
    >
      {dragOver === 'before' && (
        <div className="absolute -top-px left-2 right-2 h-0.5 bg-lime-6 rounded" />
      )}
      {branch ? (
        <button
          type="button"
          onClick={branch.onToggle}
          aria-label={
            branch.expanded
              ? t('widgets.ouRail.collapseBranch', { label })
              : t('widgets.ouRail.expandBranch', { label })
          }
          className="flex-shrink-0 pl-1.5 -mr-1.5 text-neutral-6 hover:text-neutral-2 transition-colors"
        >
          <svg
            className={clsx(
              'w-3 h-3 transition-transform',
              branch.expanded && 'rotate-90',
            )}
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2.5}
              d="M9 5l7 7-7 7"
            />
          </svg>
        </button>
      ) : (
        // Reserve the chevron's footprint on EVERY row without a toggle, so
        // siblings align and a nested row sits exactly one wrapper indent
        // deeper than its parent. Same padding + icon box as the button: with
        // `w-3` on the padded span the padding ate into the width, and a
        // childless unit sat a few pixels LEFT of its siblings, which read as
        // the next one being nested under it. Nested rows used to skip the
        // footprint and tighten their padding, which left them only a few
        // pixels deeper than the top level — indistinguishable at a glance.
        <span className="flex-shrink-0 pl-1.5 -mr-1.5">
          <span className="block w-3 h-3" />
        </span>
      )}
      <button
        type="button"
        onClick={onSelect}
        className={clsx(
          'flex-1 min-w-0 flex items-center justify-between gap-2 px-3 py-1.5 text-left text-sm',
          selected
            ? tone === 'warn'
              ? 'text-yellow-6'
              : 'text-lime-6'
            : tone === 'warn'
              ? 'text-yellow-6/80'
              : 'text-neutral-2',
        )}
      >
        <span className="truncate">{label}</span>
        <span className="text-xs text-neutral-4 flex-shrink-0">{count}</span>
      </button>
      {/* The delete affordance only in edit mode — nothing on hover in the
          plain filter view. */}
      {editMode && onDelete && (
        <div className="flex items-center flex-shrink-0 pr-2 gap-1">
          {onDelete && (
            <button
              type="button"
              onClick={onDelete}
              className="p-1 text-neutral-7 hover:text-red-6"
              aria-label={t('widgets.ouRail.deleteUnit', { label })}
            >
              <TrashIcon />
            </button>
          )}
        </div>
      )}
    </div>
  )
}

// The single OU rail used by every list page: read-only filter tree + optional
// assignment drops + optional edit mode + optional extra sections.
export const OURail: FC<OURailProps> = ({
  counts,
  rawRootCounts = false,
  selected,
  onSelect,
  pseudoRows = [],
  treeParent,
  dropTypes = [],
  onDropItem,
  alwaysAcceptDrops = false,
  editable = false,
  onCreate,
  onDelete,
  onReparent,
  onReorder,
  sections,
  dropHint,
  onCollapse,
}) => {
  const { t } = useTranslation()
  const { ous } = useOUs()
  const editMode = editable
  const [draggedOU, setDraggedOU] = useState<string | null>(null)
  const [foldedBranches, setFoldedBranches] = useState<Set<string>>(new Set())

  // Item drops are accepted in edit mode OR when a page forces it (e.g. node
  // multiselect). Delete/drag affordances stay tied to editMode only.
  const itemDropsOn = editMode || alwaysAcceptDrops

  const toggleBranch = (rootId: string) =>
    setFoldedBranches((prev) => {
      const next = new Set(prev)
      if (next.has(rootId)) next.delete(rootId)
      else next.add(rootId)
      return next
    })

  const rootOUs = useMemo(
    () =>
      ous
        .filter((ou) => !ou.parent_id)
        .slice()
        .sort((a, b) => a.position - b.position),
    [ous],
  )
  const childrenByParent = useMemo(
    () =>
      ous.reduce(
        (acc, ou) => {
          if (ou.parent_id) (acc[ou.parent_id] ??= []).push(ou)
          return acc
        },
        {} as Record<string, OU[]>,
      ),
    [ous],
  )

  const branchCount = (root: OU): number => {
    if (rawRootCounts) {
      return counts[root.id] ?? 0
    }
    const children = childrenByParent[root.id] ?? []
    return (
      (counts[root.id] ?? 0) +
      children.reduce((sum, child) => sum + (counts[child.id] ?? 0), 0)
    )
  }

  const reorderBefore = (targetId: string) => {
    if (!draggedOU || draggedOU === targetId || !onReorder) return
    const dragged = ous.find((o) => o.id === draggedOU)
    const target = ous.find((o) => o.id === targetId)
    if (!dragged || !target) return
    if ((dragged.parent_id ?? null) !== (target.parent_id ?? null)) return
    const group = (
      target.parent_id ? (childrenByParent[target.parent_id] ?? []) : rootOUs
    )
      .slice()
      .sort((a, b) => a.position - b.position)
    const ids = group.map((o) => o.id).filter((id) => id !== draggedOU)
    ids.splice(ids.indexOf(targetId), 0, draggedOU)
    onReorder(ids)
  }

  // Dropping a nested unit on the tree's parent row makes it top-level; the
  // backend reads an empty parent as "make it a top-level unit".
  const reparentToTop = () => {
    if (!draggedOU || !onReparent) return
    const dragged = ous.find((o) => o.id === draggedOU)
    if (!dragged || !dragged.parent_id) return
    onReparent(draggedOU, null)
  }

  const reparentOnto = (rootId: string) => {
    if (!draggedOU || draggedOU === rootId || !onReparent) return
    const dragged = ous.find((o) => o.id === draggedOU)
    if (!dragged || dragged.parent_id === rootId) return
    const targetIsRoot = !ous.find((o) => o.id === rootId)?.parent_id
    const draggedHasChildren = (childrenByParent[draggedOU] ?? []).length > 0
    if (!targetIsRoot || draggedHasChildren) return
    onReparent(draggedOU, rootId)
  }

  return (
    <div className="flex flex-col">
      {/* Header: title + collapse on one row, separated from the tree by a
          divider. Whether the tree is editable is the page's decision. */}
      <div className="px-3 py-2 mb-1 border-b border-neutral-12">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-xs uppercase font-bold text-neutral-4 truncate min-w-0">
            {t('widgets.ouRail.title')}
          </h2>
          {onCollapse && (
            <button
              type="button"
              onClick={onCollapse}
              aria-label={t('widgets.ouRail.hide')}
              className="p-0.5 text-neutral-6 hover:text-neutral-2 transition-colors flex-shrink-0"
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
                  d="M15 19l-7-7 7-7"
                />
              </svg>
            </button>
          )}
        </div>
      </div>

      <div className="flex flex-col gap-0.5">
        {pseudoRows.map((row) => (
          <TreeRow
            key={row.value}
            label={row.label}
            count={row.count}
            tone={row.tone}
            selected={selected === row.value}
            editMode={editMode}
            onSelect={() => onSelect(row.value)}
            dropEnabled={itemDropsOn}
            dropTarget={row.dropTarget}
            dropTypes={dropTypes}
            onDropItem={onDropItem}
          />
        ))}

        {pseudoRows.length > 0 && rootOUs.length > 0 && (
          <div className="border-t border-neutral-12 my-1" />
        )}

        {treeParent && (
          <TreeRow
            label={treeParent.label}
            count={treeParent.count}
            tone={treeParent.tone}
            selected={selected === treeParent.value}
            editMode={editMode}
            onSelect={() => onSelect(treeParent.value)}
            dropEnabled={itemDropsOn}
            dropTarget={treeParent.dropTarget}
            dropTypes={dropTypes}
            onDropItem={onDropItem}
            onReparentOnto={reparentToTop}
          />
        )}

        <div
          className={clsx(
            'flex flex-col gap-0.5',
            // Under a parent row the top-level units read as ITS children,
            // the same indent and rule their own children get.
            treeParent && 'ml-4 pl-1 border-l border-neutral-11',
          )}
        >
          {rootOUs.map((root) => (
            <div key={root.id} className="flex flex-col gap-0.5">
              <TreeRow
                label={root.name}
                count={branchCount(root)}
                selected={selected === root.id}
                editMode={editMode}
                branch={
                  (childrenByParent[root.id] ?? []).length > 0
                    ? {
                        expanded: !foldedBranches.has(root.id),
                        onToggle: () => toggleBranch(root.id),
                      }
                    : undefined
                }
                onSelect={() => onSelect(root.id)}
                onDelete={
                  editMode && onDelete ? () => onDelete(root.id) : undefined
                }
                dropEnabled={itemDropsOn}
                dropTarget={root.id}
                dropTypes={dropTypes}
                onDropItem={onDropItem}
                draggableOU={editMode}
                onDragStartOU={() => setDraggedOU(root.id)}
                onReparentOnto={() => reparentOnto(root.id)}
                onReorderBefore={() => reorderBefore(root.id)}
              />
              {!foldedBranches.has(root.id) &&
                (childrenByParent[root.id] ?? []).length > 0 && (
                  // Indented, left-ruled group so children read as nested under
                  // the root (not siblings of it).
                  <div className="ml-4 pl-1 border-l border-neutral-11 flex flex-col gap-0.5">
                    {(childrenByParent[root.id] ?? [])
                      .slice()
                      .sort((a, b) => a.position - b.position)
                      .map((child) => (
                        <TreeRow
                          key={child.id}
                          label={child.name}
                          count={counts[child.id] ?? 0}
                          selected={selected === child.id}
                          editMode={editMode}
                          onSelect={() => onSelect(child.id)}
                          onDelete={
                            editMode && onDelete
                              ? () => onDelete(child.id)
                              : undefined
                          }
                          dropEnabled={itemDropsOn}
                          dropTarget={child.id}
                          dropTypes={dropTypes}
                          onDropItem={onDropItem}
                          draggableOU={editMode}
                          onDragStartOU={() => setDraggedOU(child.id)}
                          onReparentOnto={() => reparentOnto(child.id)}
                          onReorderBefore={() => reorderBefore(child.id)}
                        />
                      ))}
                  </div>
                )}
            </div>
          ))}
        </div>

        {editMode && onCreate && (
          <button
            type="button"
            onClick={onCreate}
            className="mt-2 mx-3 px-3 py-2 text-xs uppercase text-lime-6 border border-lime-12 rounded hover:bg-lime-10/50 transition-colors"
          >
            {t('widgets.ouRail.newOU')}
          </button>
        )}
      </div>

      {dropHint && itemDropsOn && (
        <div className="px-3 pt-2 text-xs text-neutral-6">{dropHint}</div>
      )}

      {sections}
    </div>
  )
}
