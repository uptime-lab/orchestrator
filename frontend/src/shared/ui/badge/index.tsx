import clsx from 'clsx'
import { FC, ReactNode } from 'react'
import { colorSystem } from '@root/tailwind.config.ts'

export type BadgeColor = keyof typeof colorSystem
export type BadgeVariant = 'outline' | 'solid'

const colorClassMap: Record<BadgeColor, string> = {
  neutral: 'text-neutral-5 border-neutral-15',
  red: 'text-red-5 border-red-7',
  pink: 'text-pink-5 border-pink-7',
  purple: 'text-purple-5 border-purple-5',
  violet: 'text-violet-5 border-violet-5',
  blue: 'text-blue-5 border-blue-8',
  lightBlue: 'text-lightBlue-5 border-lightBlue-8',
  green: 'text-green-5 border-green-8',
  lime: 'text-lime-5 border-lime-7',
  yellow: 'text-yellow-5 border-yellow-5',
  amber: 'text-amber-5 border-amber-6',
  orange: 'text-orange-5 border-orange-5',
}

// Solid text color per background: the -7/-8 shades are too dark for
// text-neutral-15 to reach WCAG AA (red-7 is 4.13:1), so those use
// near-white; keep any new shade above 4.5:1 against its text.
const bgClassMap: Record<string, string> = {
  neutral: 'text-neutral-15 bg-neutral-5',
  red: 'text-neutral-1 bg-red-7',
  pink: 'text-neutral-1 bg-pink-7',
  purple: 'text-neutral-15 bg-purple-5',
  violet: 'text-neutral-15 bg-violet-5',
  blue: 'text-neutral-1 bg-blue-8',
  lightBlue: 'text-neutral-15 bg-lightBlue-8',
  green: 'text-neutral-15 bg-green-8',
  lime: 'text-neutral-15 bg-lime-7',
  yellow: 'text-neutral-15 bg-yellow-5',
  amber: 'text-neutral-15 bg-amber-5',
  orange: 'text-neutral-15 bg-orange-5',
}

const variantClasses: Record<BadgeVariant, string> = {
  solid: '',
  outline: 'bg-transparent border',
}

type BadgeProps = {
  color: BadgeColor
  variant: BadgeVariant
  children: ReactNode
}

const Badge: FC<BadgeProps> = ({ color, variant = 'outline', children }) => {
  return (
    <div
      className={clsx(
        variantClasses[variant],
        variant === 'solid' ? bgClassMap[color] : colorClassMap[color],
        'rounded-[3px] px-2 py-[3px] text-xs w-fit',
      )}
    >
      {children}
    </div>
  )
}

export default Badge
