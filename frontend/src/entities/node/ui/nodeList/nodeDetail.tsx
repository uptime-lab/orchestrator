import clsx from 'clsx'
import { PropsWithChildren } from 'react'

export type NodeDetailProps = {
  title: string
  size?: 'md' | 'lg' | 'xl'
}

export const NodeDetail = ({
  title,
  size = 'md',
  children,
}: PropsWithChildren<NodeDetailProps>) => {
  return (
    <div
      className={clsx(
        'flex flex-col gap-2 text-left',
        size === 'md' && 'w-24',
        size === 'lg' && 'w-28',
        size === 'xl' && 'w-44',
      )}
    >
      <div className="text-xs font-normal font-mono text-neutral-6">
        {title}
      </div>
      {children}
    </div>
  )
}
