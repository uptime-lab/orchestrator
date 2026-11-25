import clsx from 'clsx'
import { FC } from 'react'

type ProgressBarProps = {
  percent: number // 0..100
  className?: string
}

export const ProgressBar: FC<ProgressBarProps> = ({ percent, className }) => {
  const clamped = Math.max(0, Math.min(100, percent))
  return (
    <div
      role="progressbar"
      aria-valuenow={Math.round(clamped)}
      aria-valuemin={0}
      aria-valuemax={100}
      className={clsx(
        'h-2 w-full rounded bg-neutral-13 overflow-hidden',
        className,
      )}
    >
      <div
        className="h-full rounded bg-lime-6 transition-[width] duration-200 ease-out"
        style={{ width: `${clamped}%` }}
      />
    </div>
  )
}
