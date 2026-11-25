import clsx from 'clsx'
import { forwardRef, InputHTMLAttributes } from 'react'

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string
  error?: boolean
}

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ label, className, error, ...props }, ref) => {
    return (
      <div className="flex flex-col gap-2 text-sm">
        {label && (
          <label className={clsx('text-neutral-6', error && 'text-red-6')}>
            {label}
          </label>
        )}
        <input
          ref={ref}
          className={clsx(
            'w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white',
            'outline-none transition-colors',
            error && 'ring-inset ring-1 ring-red-6',
            className,
          )}
          {...props}
        />
      </div>
    )
  },
)

Input.displayName = 'Input'
