import {
  Button as HeadlessButton,
  ButtonProps as HeadlessButtonProps,
} from '@headlessui/react'
import clsx from 'clsx'

import { ButtonSize, ButtonStyle, ButtonVariant } from '@/shared/ui/button'

export interface ButtonProps extends HeadlessButtonProps<'button'> {
  kind?: ButtonStyle
  variant?: ButtonVariant
  size?: ButtonSize
  grow?: boolean
  onClick?: React.MouseEventHandler<HTMLButtonElement>
}

const baseClasses = [
  'rounded',
  'flex',
  'justify-center',
  'items-center',
  'whitespace-nowrap',
]
const disabledClasses = ['disabled:text-neutral-6', 'disabled:bg-neutral-9']

const sizeClasses: Record<ButtonSize, string[]> = {
  sm: ['text-xxs', 'tracking-widest', 'px-2', 'py-1', 'max-h-[22px]'],
  md: ['text-xs', 'tracking-wider', 'px-3', 'py-1.5', 'max-h-[28px]'],
  lg: ['text-sm', 'tracking-wide', 'px-4', 'py-2', 'max-h-[34px]'],
}

const sizeClassNames = (size: ButtonSize, grow: boolean): string[] => [
  ...sizeClasses[size],
  grow ? 'w-full block' : 'w-auto inline-block',
]

const styleClasses: Record<ButtonVariant, Record<ButtonStyle, string[]>> = {
  normal: {
    primary: ['bg-lime-12', 'text-lime-6', 'hover:bg-lime-10'],
    secondary: [
      'border',
      'bg-transparent',
      'text-lime-6',
      'border-lime-12',
      'hover:bg-lime-10/50',
    ],
    tertiary: ['bg-transparent', 'text-lime-6', 'hover:bg-neutral-1/10'],
  },
  destructive: {
    // Red text on a red fill measured 2.43:1 — below WCAG AA (4.5) and below
    // even the large-text floor (3.0), so it was hard to read. The fix was
    // already inside this button: it
    // switched to neutral-1 on hover, which is 8.51:1 on the same fill. Using it
    // at rest also removes a text colour that changed under the cursor.
    //
    // The other two destructive styles are NOT touched: red-6 on the page
    // background measures 4.53–4.97:1 and passes.
    primary: [
      'bg-red-9',
      'text-neutral-1',
      // `hover:bg-red-6` used to sit here too, dead: Tailwind emits both and the
      // later one wins, so it only made the intent unreadable.
      'hover:bg-red-10',
    ],
    secondary: [
      'border',
      'bg-transparent',
      'text-red-6',
      'border-red-9',
      'hover:bg-red-10/50',
    ],
    tertiary: ['bg-transparent', 'text-red-6', 'hover:bg-neutral-1/10'],
  },
}

const textClasses: Record<ButtonStyle, string[]> = {
  primary: ['text-center', 'uppercase', 'font-medium', 'font-mono'],
  secondary: ['text-center', 'uppercase', 'font-medium', 'font-mono'],
  tertiary: ['text-center', 'uppercase', 'font-medium', 'font-mono'],
}

export const Button = ({
  kind = 'primary',
  variant = 'normal',
  size = 'md',
  grow = false,
  children,
  className,
  ...props
}: React.PropsWithChildren<ButtonProps>) => {
  // className is merged LAST (not spread over the computed classes) so callers
  // can extend/override specific utilities without wiping the base button
  // styling — passing `className` used to replace it entirely, and an undefined
  // className even blanked it.
  const buttonClasses = clsx(
    baseClasses,
    disabledClasses,
    styleClasses[variant][kind],
    textClasses[kind],
    sizeClassNames(size, grow),
    className,
  )

  return (
    <HeadlessButton className={buttonClasses} {...props}>
      {children}
    </HeadlessButton>
  )
}
