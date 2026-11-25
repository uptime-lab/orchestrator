import clsx from 'clsx'

import { IconColor, IconSize } from './types'

export type IconLabelProps = {
  icon: React.ReactNode
  size?: IconSize
  iconColor?: IconColor
  className?: string
  children: React.ReactNode
}

const sizeClasses: Record<string, string[]> = {
  xs: ['w-2', 'h-2'],
  sm: ['w-3', 'h-3'],
  md: ['w-4', 'h-4'],
  lg: ['w-5', 'h-5'],
  xl: ['w-6', 'h-6'],
}

export const IconLabel: React.FC<IconLabelProps> = ({
  icon,
  size = 'md',
  className,
  children,
}) => {
  return (
    <div className={clsx('flex flex-row items-center gap-1', className)}>
      <div className={clsx('flex-none', sizeClasses[size])}>{icon}</div>
      {children}
    </div>
  )
}
