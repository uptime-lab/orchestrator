import clsx from 'clsx'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { WorkflowBlock } from '@/entities/workflow/api/types'

import { isPlaceableBlock } from './graphUtils'

// PALETTE_DND_TYPE carries a block type from the palette to the canvas.
export const PALETTE_DND_TYPE = 'application/x-orchestrator-block'

type PaletteProps = {
  blocks: WorkflowBlock[]
  // Clicking a block opens its details rather than placing it: a palette entry
  // is a name, and half of these do something irreversible to a device.
  onPreviewBlock: (block: WorkflowBlock) => void
  previewedType?: string
}

// Palette category order (agent commands first, then control flow, then
// integrations); anything else falls in after.
const CATEGORY_ORDER = [
  'power',
  'os',
  'software',
  'system',
  'boot',
  'files',
  'storage',
  'diagnostics',
  'control',
  'integrations',
]

// Left panel: every placeable block (all but triggers, which are the entry
// point) grouped by category; click adds it to the flow.
export const BlockPalette = ({
  blocks,
  onPreviewBlock,
  previewedType,
}: PaletteProps) => {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')

  // Search the DESCRIPTION as well as the name: an operator looking for a
  // capability types what it does ("recovery", "image", "secret"), not the
  // label somebody chose for it. Category is matched too, so typing "storage"
  // narrows to that group.
  const matching = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (!needle) return blocks
    return blocks.filter((block) =>
      [block.name, block.description, block.category, block.slug]
        .filter(Boolean)
        .some((field) => (field as string).toLowerCase().includes(needle)),
    )
  }, [blocks, query])

  const byCategory = useMemo(() => {
    const groups: Record<string, WorkflowBlock[]> = {}
    matching.forEach((block) => {
      // Triggers are created with the graph; see isPlaceableBlock for the rest.
      if (!isPlaceableBlock(block)) return
      const category = block.category || 'other'
      groups[category] = groups[category] || []
      groups[category].push(block)
    })
    return groups
  }, [matching])

  const orderedCategories = useMemo(() => {
    const keys = Object.keys(byCategory)
    return keys.sort((a, b) => {
      const ia = CATEGORY_ORDER.indexOf(a)
      const ib = CATEGORY_ORDER.indexOf(b)
      return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib)
    })
  }, [byCategory])

  return (
    <div className="w-56 shrink-0 border-r border-neutral-11 overflow-y-auto px-3 pb-12 flex flex-col gap-4">
      {/* Pinned: the search is useless once it has scrolled away, which is
          exactly what happens after the first query narrows nothing. The
          background is the page's own, so the list passes UNDER it rather than
          showing through. The bottom padding keeps the last block off the
          window edge on a full-screen canvas. */}
      {/* The container carries NO top padding: a sticky element pins to the top
          of the scroll box, so any padding above it is a gap the list scrolls
          through. The padding lives inside here, under this background. */}
      <div className="sticky top-0 z-10 -mx-3 px-3 pt-3 pb-2 bg-base-dark flex flex-col gap-2">
        <div className="text-xs font-bold uppercase text-neutral-7">
          {t('pages.workflowEditor.palette.title')}
        </div>
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('pages.workflowEditor.palette.searchPlaceholder')}
          className="bg-neutral-13 border border-neutral-11 rounded px-2 py-1 text-xs text-neutral-2 placeholder:text-neutral-8 focus:outline-none focus:border-purple-5"
        />
      </div>
      {orderedCategories.length === 0 && (
        <div className="text-xs text-neutral-6">
          {t('pages.workflowEditor.palette.noMatch', { query })}
        </div>
      )}
      {orderedCategories.map((category) => (
        <div key={category} className="flex flex-col gap-1">
          <div className="text-[10px] uppercase tracking-wider text-neutral-6">
            {category}
          </div>
          {byCategory[category].map((block) => (
            <button
              key={block.type}
              type="button"
              draggable
              onDragStart={(e) => {
                e.dataTransfer.setData(PALETTE_DND_TYPE, block.type)
                e.dataTransfer.effectAllowed = 'copy'
              }}
              onClick={() => onPreviewBlock(block)}
              title={block.description}
              className={clsx(
                'text-left px-2.5 py-1.5 rounded border text-xs transition-colors cursor-grab',
                'bg-neutral-13 text-neutral-3',
                previewedType === block.type
                  ? 'border-purple-5 text-neutral-1'
                  : 'border-neutral-11 hover:border-purple-5 hover:text-neutral-1',
              )}
            >
              {block.name}
              {block.dangerous && (
                <span className="text-red-6 ml-1.5 text-[10px] uppercase">
                  {t('pages.workflowEditor.palette.destructive')}
                </span>
              )}
            </button>
          ))}
        </div>
      ))}
    </div>
  )
}
