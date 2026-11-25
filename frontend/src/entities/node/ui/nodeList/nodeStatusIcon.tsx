import clsx from 'clsx'

import { NodeStatus } from '@/entities/node/api/types'
import {
  NodeStatusColourClassMap,
  nodeStatusText,
  normalizeStatus,
} from '@/entities/node/utils.ts'

enum NodeStatusStyle {
  Solid,
  Outline,
}

const styleTypeMap: Record<NodeStatus, NodeStatusStyle> = {
  online: NodeStatusStyle.Solid,
  maintenance: NodeStatusStyle.Outline,
  standby: NodeStatusStyle.Outline,
  offline: NodeStatusStyle.Outline,
  unreachable: NodeStatusStyle.Outline,
  ready_for_adoption: NodeStatusStyle.Outline,
  flashing: NodeStatusStyle.Outline,
  adopting: NodeStatusStyle.Outline,
  adoption_failed: NodeStatusStyle.Outline,
  restarting: NodeStatusStyle.Outline,
  shutting_down: NodeStatusStyle.Outline,
  shutdown: NodeStatusStyle.Outline,
  busy: NodeStatusStyle.Outline,
  os_installing: NodeStatusStyle.Outline,
  os_installation_failed: NodeStatusStyle.Outline,
}

export type NodeStatusProps = {
  status: NodeStatus | string
  className?: string
  backgroundColor?: string
}

export const NodeStatusIcon: React.FC<NodeStatusProps> = ({
  status,
  backgroundColor = 'transparent',
  className = '',
}) => {
  const normalizedStatus = normalizeStatus(status)
  const mode = styleTypeMap[normalizedStatus]
  const fill = mode === NodeStatusStyle.Solid
  const label = nodeStatusText(normalizedStatus, null)

  return (
    <div
      role="img"
      aria-label={label}
      title={label}
      className={clsx(
        'relative w-3 h-[52px] flex items-start',
        NodeStatusColourClassMap[normalizedStatus],
        className,
      )}
    >
      <svg
        className="absolute top-0 left-0 w-full h-full"
        xmlns="http://www.w3.org/2000/svg"
        viewBox="0 0 12 52"
      >
        <title>{label}</title>
        <path
          d="M 0 0 L 12 15 L 12 39 L 0 52 Z"
          fill={backgroundColor}
          className="transition-colors duration-300"
        />
        {fill && (
          <path
            d="M 0 14 L 4 18 L 4 34 L 0 39 Z"
            fill="currentColor"
            className="transition-colors duration-300"
          />
        )}
        {!fill && (
          <path // overlay
            d="M -1 14 L 4 20 L 4 34 L -1 39 Z"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.25"
            className="transition-colors duration-300"
          />
        )}
      </svg>
    </div>
  )
}
