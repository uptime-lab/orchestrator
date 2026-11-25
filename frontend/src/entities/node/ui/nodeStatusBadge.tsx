import clsx from 'clsx'
import { colorSystem } from '@root/tailwind.config.ts'

import { NodeStatus } from '@/entities/node/api/types'
import {
  NodeStatusColourMap,
  nodeStatusText,
  normalizeStatus,
} from '@/entities/node/utils.ts'

// Static class strings so Tailwind's scanner picks them up.
const badgeClassMap: Record<keyof typeof colorSystem, string> = {
  neutral: 'border-neutral-7/50 text-neutral-5 bg-neutral-7/10',
  red: 'border-red-7/50 text-red-6 bg-red-6/10',
  pink: 'border-pink-7/50 text-pink-6 bg-pink-6/10',
  purple: 'border-purple-7/50 text-purple-5 bg-purple-6/10',
  violet:
    'border-violet-7/50 text-violet-5 bg-violet-6/10 shadow-[0_0_14px_rgba(142,102,255,0.35)]',
  blue: 'border-blue-7/50 text-blue-5 bg-blue-6/10',
  lightBlue: 'border-lightBlue-7/50 text-lightBlue-7 bg-lightBlue-6/10',
  green:
    'border-green-8/50 text-green-7 bg-green-7/10 shadow-[0_0_14px_rgba(64,255,61,0.25)]',
  lime: 'border-lime-7/50 text-lime-6 bg-lime-6/10',
  yellow: 'border-yellow-7/50 text-yellow-7 bg-yellow-6/10',
  amber: 'border-amber-7/50 text-amber-6 bg-amber-6/10',
  orange: 'border-orange-7/50 text-orange-6 bg-orange-6/10',
}

export type NodeStatusBadgeProps = {
  status: NodeStatus | string
  // Presentation override (e.g. "Recovery" for a netbooted node whose raw
  // status is still online). The color is picked via `color` or the status.
  label?: string
  color?: keyof typeof colorSystem
  // Inline color override (e.g. the reboot-to-recovery orange-to-violet
  // transition); wins over the palette classes.
  style?: React.CSSProperties
  className?: string
}

// Compact status pill (dot + label) used in the node details header.
export const NodeStatusBadge = ({
  status,
  label,
  color,
  style,
  className,
}: NodeStatusBadgeProps) => {
  const normalized = normalizeStatus(status)
  return (
    <span
      className={clsx(
        'inline-flex items-center gap-1.5 px-2 py-0.5 rounded border',
        'font-mono text-xxs uppercase tracking-wider whitespace-nowrap',
        badgeClassMap[color ?? NodeStatusColourMap[normalized]],
        className,
      )}
      style={style}
    >
      <span className="w-1.5 h-1.5 rounded-full bg-current" aria-hidden />
      {label ?? nodeStatusText(normalized, null)}
    </span>
  )
}
