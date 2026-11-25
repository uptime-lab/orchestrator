import clsx from 'clsx'
import { FC, ReactNode } from 'react'

// ActionRow is a single command entry in the node Actions tab: an icon + label
// button spanning the panel width. Shared so every actionable item on the tab
// (rows, Terminal, Run playbook…) reads as the same stacked, same-size row.
// `danger` styles destructive actions red.
export const ActionRow: FC<{
  icon: ReactNode
  label: string
  onClick: () => void
  danger?: boolean
  disabled?: boolean
  title?: string
}> = ({ icon, label, onClick, danger = false, disabled = false, title }) => (
  <button
    type="button"
    onClick={onClick}
    disabled={disabled}
    title={title}
    className={clsx(
      'flex items-center gap-3 w-full px-3 py-2.5 rounded text-sm transition-colors',
      disabled
        ? 'text-neutral-7 cursor-not-allowed'
        : danger
          ? 'text-red-6 hover:bg-red-6/10'
          : 'text-neutral-3 hover:bg-neutral-13',
    )}
  >
    <span className="flex-shrink-0 text-neutral-6">{icon}</span>
    {label}
  </button>
)
