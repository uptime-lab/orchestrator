import clsx from 'clsx'
import { useTranslation } from 'react-i18next'

import type { DeletedNode } from '@/entities/node/api/deletedNodes'
import type { NodeData } from '@/entities/node/api/types.ts'
import { deviceStillTrying } from '@/entities/node/restoreOutlook'
import { StatusTube } from '@/entities/node/ui/nodeList/nodeCard'
import {
  NodeColumn,
  nodeColumnsOrdered,
} from '@/entities/node/ui/nodeList/nodeColumns'
import { nodeDisplayName } from '@/entities/node/utils.ts'
import { useOUs } from '@/entities/ou'
import { usePreferHostnames } from '@/entities/user/preferences'

/**
 * The row of a DELETED node — a tombstone listed below the live fleet.
 *
 * It exists because deleting a node is a one-click action whose consequence
 * reaches past the platform: the row goes, the device is told nothing, and it
 * retries forever with an identity nothing recognises. Before this the only
 * evidence was a node count that did not add up; then a collapsible panel under
 * the list, which an operator scanning the table for the missing node never
 * opened. A grey row in the table itself, under the unit the node belonged to,
 * is where that operator is already looking.
 *
 * It is deliberately NOT a NodeCard with a flag: it has no metrics, no
 * favourite, no identify button, no checkbox and no drag. What it shares with
 * NodeCard is the skeleton — tube, identity column, ID slot, the metric
 * columns, the same padding — so it is the same height as its neighbours and
 * its cells sit under the same headers. Clicking it opens the deleted node's
 * panel, exactly as a live row opens the node's: putting the outlook sentence and
 * the actions on a second line inside the row would make it taller than
 * every other row and still leave nothing to click.
 *
 * The address column shows a dash rather than the serial: a tombstone has no
 * address, and a serial under the Address header read as "strange data". The
 * serial is in the panel.
 */
type Props = {
  row: DeletedNode
  columns?: NodeColumn[]
  selected: boolean
  onClick: (id: string) => unknown
}

const CELLS_WITHOUT_A_SUBJECT = new Set(['favorite', 'address'])

export const DeletedNodeCard = ({
  row,
  columns = nodeColumnsOrdered(),
  selected,
  onClick,
}: Props) => {
  const { t } = useTranslation()
  const { ous } = useOUs()
  const preferHostnames = usePreferHostnames()

  const name =
    nodeDisplayName(row, preferHostnames) ||
    t('entities.nodeList.deletedRow.fallbackName')
  // The unit the node belonged to, restored with it. A unit deleted since
  // resolves as unassigned on restore, so the dash is honest, not a gap.
  const ouName = row.ou_id
    ? (ous.find((o) => o.id === row.ou_id)?.name ?? '—')
    : '—'
  const deletedAt = new Date(row.deleted_at)
  // The device presented this node's credential within the hour: it is out
  // there and still trying. Said on the row because that is the one fact that
  // changes what the operator does next (restore it, then delete it while it
  // is connected). "Trying", never "online" — the platform saw a credential.
  const trying = deviceStillTrying(row)

  // Just enough of a node for the column renderers: the unit column reads
  // ouName, and every hardware and metric column reads an absence and renders
  // its dash.
  const shell = {
    id: row.id,
    name: row.name,
    hostname: row.hostname,
    ou_id: row.ou_id,
    status: 'offline',
  } as unknown as NodeData
  const cellContext = {
    node: shell,
    ouName,
    osText: '—',
    uptime: '—',
    isOffline: false,
    storage: null,
    metrics: undefined,
    isFavorite: false,
    onToggleFavorite: () => {},
  }

  return (
    <div
      className={clsx(
        'flex items-center gap-4 pl-0 pr-4 select-none rounded transition-colors',
        selected ? 'bg-neutral-12' : 'bg-neutral-14 hover:bg-neutral-13',
      )}
      data-deleted-node={row.id}
    >
      <button
        type="button"
        className="flex items-center gap-4 flex-1 min-w-0 text-left py-3"
        onClick={() => onClick(row.id)}
      >
        <StatusTube
          state="deleted"
          label={t('entities.nodeList.deletedRow.status')}
          blinking={false}
        />
        <div className="flex flex-col min-w-0 flex-1 @[30rem]:flex-none @[30rem]:w-56">
          <span className="text-sm font-medium text-neutral-5 truncate">
            {name}
          </span>
          <span
            className="text-xxs uppercase tracking-wide text-neutral-7 truncate"
            title={deletedAt.toLocaleString()}
          >
            {t('entities.nodeList.deletedRow.deletedOn', {
              date: deletedAt.toLocaleDateString(),
            })}
            {trying && (
              <>
                {' · '}
                <span className="text-yellow-6">
                  {t('entities.nodeList.deletedRow.deviceTrying')}
                </span>
              </>
            )}
          </span>
        </div>
        {/* The ID-button slot, kept for alignment. */}
        <div className="flex-shrink-0 w-8" aria-hidden />
        <div className="flex items-center gap-6 flex-1 min-w-0">
          {columns.map((column) => (
            <div
              key={column.key}
              className={clsx(column.className, 'text-sm text-neutral-6')}
            >
              {/* The favourite cell is a control, and a control on a row that
                  has no node behind it is a trap; the address cell has nothing
                  to show. Both slots keep their width so the cells to the right
                  stay under their headers. */}
              {CELLS_WITHOUT_A_SUBJECT.has(column.key) ? (
                column.key === 'address' ? (
                  <span className="text-neutral-7">—</span>
                ) : null
              ) : (
                column.render(cellContext)
              )}
            </div>
          ))}
        </div>
        {/* Compact address slot, shown only while the Address column is hidden. */}
        <div className="@[30rem]:hidden flex-none text-sm text-neutral-7 font-mono">
          —
        </div>
        <div className="hidden @[30rem]:block w-6 flex-shrink-0" aria-hidden />
      </button>
    </div>
  )
}
