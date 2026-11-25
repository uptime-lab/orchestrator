import { clsx } from 'clsx'

import {
  StatusIconColor,
  StatusIconSize,
  StatusIconVariant,
} from '@/shared/ui/statusIcon'

export interface StatusIconProps {
  variant?: StatusIconVariant
  color?: StatusIconColor
  size?: StatusIconSize
}

const colorClasses: Record<StatusIconColor, string[]> = {
  green: ['text-green-6', 'bg-green-6', 'border-green-6'],
  red: ['text-red-6', 'bg-red-6', 'border-red-6'],
  gray: ['text-neutral-6', 'bg-neutral-6', 'border-neutral-6'],
  orange: ['text-orange-6', 'bg-orange-6', 'border-orange-6'],
}

const variantClasses: Record<StatusIconVariant, string[]> = {
  solid: ['rounded-full'],
  outline: ['rounded-full bg-transparent border'],
}

const sizeClasses: Record<StatusIconSize, string[]> = {
  large: ['w-5 h-5 sm:w-6 sm:h-6'],
  medium: ['h-4 w-4 sm:w-5 sm:h-5'],
  small: ['w-3 h-3'],
  xsmall: ['w-2 h-2'],
}

export const StatusIcon = ({
  variant = 'solid',
  color = 'gray',
  size = 'small',
}: StatusIconProps) => {
  const currentColorClassNames = colorClasses[color]
  const currentSizeClassNames = sizeClasses[size]
  const currentVariantClassNames = variantClasses[variant]

  return (
    <div
      className={clsx(
        currentColorClassNames,
        currentSizeClassNames,
        currentVariantClassNames,
      )}
    ></div>
  )
}
