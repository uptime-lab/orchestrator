import clsx from 'clsx'
import { t } from 'i18next'
import type { KeyboardEvent, MouseEvent, ReactNode } from 'react'

import type {
  NodeData,
  NodeMetricsSnapshot,
} from '@/entities/node/api/types.ts'
import { nodeBoard, nodeRamGB, primaryDiskGB } from '@/entities/node/utils.ts'

// Everything a cell renderer may need, precomputed by the row component
// (hooks like useUptime run there, not in the renderers).
export type NodeCellContext = {
  node: NodeData
  ouName: string
  osText: string
  uptime: string
  isOffline: boolean
  storage: { totalGB: number; usedGB: number | null } | null
  // Latest live sample (WebSocket); undefined when the node is not streaming.
  metrics?: NodeMetricsSnapshot
  // Favourite state and its toggle, so the Fav column renders like any other
  // cell. The row component owns the store subscription; a renderer cannot
  // hold hooks.
  isFavorite: boolean
  onToggleFavorite: (e: MouseEvent | KeyboardEvent) => void
}

// One entry per metric column, shared by the header row and the data rows so
// their widths and container-query visibility always stay in sync. Columns
// appear by priority as the list container (see the @container wrapper in
// NodeList) widens; the thresholds account for the sidebar shrinking the list.
//
// A column never shrinks below its width (flex-shrink-0) and appears only once
// the list can hold it beside everything already shown — the row's fixed
// parts, the favourite and the columns of higher priority. Shrinkable columns
// under too-low thresholds cut values down to two or three characters. The
// node name takes whatever is left. nodeColumns.test.ts checks the sums.
// Stage-4 features (user-toggled visibility, filters) extend this model.
export type NodeColumn = {
  key: string
  label: string
  className: string
  render: (ctx: NodeCellContext) => ReactNode
}

// A FUNCTION, not a const array: the labels come from i18next, which is not
// initialised when this module is first evaluated. Resolving them at module
// load would freeze whatever the (empty) catalog answered — call it at use
// time instead, exactly as widgets/auditTrail/describe.ts does.
export const nodeColumns = (): NodeColumn[] => [
  {
    // Favourite. A column of its own, so it hides and sorts like every other
    // one — as a glyph on the name line it was hover-revealed (unreadable
    // while scanning the list) and reachable by neither the column menu nor a
    // sort. span, not button: the whole row is already a button.
    key: 'favorite',
    label: t('entities.nodeList.columns.favorite'),
    // No container-query threshold: every other column may drop out as the
    // list narrows, but favouriting must not stop being reachable just because
    // the details panel is open. Hiding it stays the user's choice.
    className: 'block w-8 min-w-0 flex-shrink-0',
    render: ({ isFavorite, onToggleFavorite }) => (
      <span
        role="button"
        tabIndex={0}
        aria-pressed={isFavorite}
        aria-label={
          isFavorite
            ? t('entities.nodeList.columns.removeFromFavorites')
            : t('entities.nodeList.columns.addToFavorites')
        }
        title={
          isFavorite
            ? t('entities.nodeList.columns.removeFromFavorites')
            : t('entities.nodeList.columns.addToFavorites')
        }
        onClick={onToggleFavorite}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onToggleFavorite(e)
          }
        }}
        className={clsx(
          'flex items-center justify-center w-7 h-7 rounded border',
          'cursor-pointer transition-colors',
          isFavorite
            ? 'border-yellow-7 bg-yellow-6 text-neutral-14 shadow-[0_0_10px_rgba(255,247,61,0.45)]'
            : 'border-neutral-11 bg-neutral-13 text-neutral-8 hover:border-yellow-8 hover:text-yellow-6',
        )}
      >
        <svg
          className="w-3.5 h-3.5"
          viewBox="0 0 20 20"
          fill={isFavorite ? 'currentColor' : 'none'}
          stroke="currentColor"
          strokeWidth={isFavorite ? 0 : 1.5}
          aria-hidden="true"
        >
          <path d="M9.049 2.927c.3-.921 1.603-.921 1.902 0l1.07 3.292a1 1 0 00.95.69h3.462c.969 0 1.371 1.24.588 1.81l-2.8 2.034a1 1 0 00-.364 1.118l1.07 3.292c.3.921-.755 1.688-1.54 1.118l-2.8-2.034a1 1 0 00-1.175 0l-2.8 2.034c-.784.57-1.838-.197-1.539-1.118l1.07-3.292a1 1 0 00-.364-1.118L2.98 8.72c-.783-.57-.38-1.81.588-1.81h3.461a1 1 0 00.951-.69l1.07-3.292z" />
        </svg>
      </span>
    ),
  },
  {
    key: 'hostname',
    label: t('entities.nodeList.columns.hostname'),
    className: 'hidden @[55rem]:block w-36 flex-shrink-0',
    render: ({ node }) => (
      <span className="block truncate" title={node.hostname || undefined}>
        {node.hostname || <span className="text-neutral-6">—</span>}
      </span>
    ),
  },
  {
    key: 'address',
    label: t('entities.nodeList.columns.address'),
    className: 'hidden @[36rem]:block w-36 flex-shrink-0',
    render: ({ node }) => (
      <span className="block truncate font-mono">
        {node.ipv4 || node.processor_serial || '—'}
      </span>
    ),
  },
  {
    key: 'uptime',
    label: t('entities.nodeList.columns.uptime'),
    className: 'hidden @[44rem]:block w-28 flex-shrink-0',
    render: ({ uptime, isOffline }) =>
      // Offline nodes report when they were last seen, not an uptime — make
      // the different semantics explicit under the shared column header.
      isOffline ? (
        <span className="block truncate text-neutral-6">
          {t('entities.nodeList.columns.seen', { uptime })}
        </span>
      ) : (
        <span className="block truncate">{uptime}</span>
      ),
  },
  {
    key: 'storage',
    label: t('entities.nodeList.columns.storage'),
    className: 'hidden @[64rem]:block w-32 flex-shrink-0',
    render: ({ storage }) => {
      if (!storage) {
        return <span className="text-neutral-6">—</span>
      }
      return (
        <>
          <span className="block truncate">
            {storage.usedGB !== null
              ? `${storage.usedGB.toFixed(0)} GB / ${storage.totalGB.toFixed(0)} GB`
              : `${storage.totalGB.toFixed(0)} GB`}
          </span>
          {storage.usedGB !== null && (
            <div className="mt-1 h-1 w-full rounded-full bg-neutral-12 overflow-hidden">
              <div
                className="h-full bg-lime-6"
                style={{
                  // Keep a sliver visible even at <1% so "in use" reads at a glance.
                  width: `${Math.min(100, Math.max(2, (storage.usedGB / storage.totalGB) * 100)).toFixed(0)}%`,
                }}
              />
            </div>
          )}
        </>
      )
    },
  },
  {
    key: 'unit',
    label: t('entities.nodeList.columns.unit'),
    className: 'hidden @[78rem]:block w-28 flex-shrink-0',
    render: ({ ouName }) => <span className="block truncate">{ouName}</span>,
  },
  {
    // Board type derived from the CPU model (CM4/CM5) — static hardware, not
    // the live CPU% column below.
    key: 'board',
    label: t('entities.nodeList.columns.board'),
    className: 'hidden @[70rem]:block w-16 flex-shrink-0',
    render: ({ node }) => {
      const soc = nodeBoard(node)
      return soc ? (
        <span className="block truncate">{soc}</span>
      ) : (
        <span className="text-neutral-6">—</span>
      )
    },
  },
  {
    // Installed RAM size (static) — distinct from the live Memory% column.
    key: 'ram',
    label: t('entities.nodeList.columns.ram'),
    className: 'hidden @[84rem]:block w-16 flex-shrink-0',
    render: ({ node }) => {
      const gb = nodeRamGB(node)
      return gb !== null ? (
        <span className="block truncate">{gb} GB</span>
      ) : (
        <span className="text-neutral-6">—</span>
      )
    },
  },
  {
    // Primary (largest non-removable) disk capacity — distinct from the Storage
    // used/total aggregate column.
    key: 'disk',
    label: t('entities.nodeList.columns.disk'),
    className: 'hidden @[102rem]:block w-20 flex-shrink-0',
    render: ({ node }) => {
      const gb = primaryDiskGB(node)
      return gb !== null ? (
        <span className="block truncate">{gb} GB</span>
      ) : (
        <span className="text-neutral-6">—</span>
      )
    },
  },
  {
    key: 'os',
    label: t('entities.nodeList.columns.os'),
    // w-40 flex-auto, NOT flex-1: flex-1 is flex-basis 0, so when the row is
    // oversubscribed (which it is at every width where this column first
    // appears — the fixed columns alone outrun the strip) the only flexible
    // column starts at zero and absorbs the whole shortfall: header present,
    // every cell blank. A real basis makes it shrink and
    // ellipsize proportionally with its fixed neighbours instead.
    className: 'hidden @[95rem]:block w-40 flex-shrink-0',
    render: ({ osText }) => (
      <span className="block truncate" title={osText}>
        {osText}
      </span>
    ),
  },
  {
    key: 'cpu',
    label: t('entities.nodeList.columns.cpu'),
    className: 'hidden @[107rem]:block w-16 flex-shrink-0',
    render: ({ metrics }) =>
      metrics ? (
        <span className="block truncate">
          {metrics.cpu_percent.toFixed(0)}%
        </span>
      ) : (
        <span className="text-neutral-6">—</span>
      ),
  },
  {
    key: 'memory',
    label: t('entities.nodeList.columns.memory'),
    className: 'hidden @[115rem]:block w-24 flex-shrink-0',
    render: ({ metrics }) =>
      metrics && metrics.mem_total_bytes > 0 ? (
        <span className="block truncate">
          {((metrics.mem_used_bytes / metrics.mem_total_bytes) * 100).toFixed(
            0,
          )}
          % · {(metrics.mem_used_bytes / 1024 ** 3).toFixed(1)} GB
        </span>
      ) : (
        <span className="text-neutral-6">—</span>
      ),
  },
  {
    key: 'temp',
    label: t('entities.nodeList.columns.temp'),
    className: 'hidden @[120rem]:block w-16 flex-shrink-0',
    render: ({ metrics }) => {
      const temp = metrics?.thermal?.length
        ? Math.max(...metrics.thermal.map((z) => z.temp_celsius))
        : undefined
      return temp !== undefined ? (
        <span className="block truncate">{temp.toFixed(0)} °C</span>
      ) : (
        <span className="text-neutral-6">—</span>
      )
    },
  },
]

// Rendering order follows the visual layout (left to right), not the
// priority order above.
export const nodeColumnsOrdered = (): NodeColumn[] => {
  const columns = nodeColumns()
  const byKey = (key: string) => columns.find((c) => c.key === key)!
  return [
    byKey('favorite'),
    byKey('hostname'),
    byKey('address'),
    byKey('unit'),
    byKey('board'),
    byKey('ram'),
    byKey('disk'),
    byKey('os'),
    byKey('cpu'),
    byKey('memory'),
    byKey('temp'),
    byKey('uptime'),
    byKey('storage'),
  ]
}
