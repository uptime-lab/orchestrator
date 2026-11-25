import clsx from 'clsx'
import { FC, ReactNode } from 'react'

// InfoRow is the single row primitive for the node Overview "table": a fixed
// label column on the left and a value cell on the right. Every row on the
// Overview tab (connection details, Agent, Hostname, and the Configuration
// section) uses it with the same label width so the whole panel reads as one
// aligned two-column table rather than a stack of differently-shaped widgets.
export const InfoRow: FC<{
  label: ReactNode
  children: ReactNode
  // 'start' aligns the label to the top of a multi-line value (storage lists);
  // 'center' is the default for single-line rows.
  align?: 'center' | 'start'
  className?: string
}> = ({ label, children, align = 'center', className }) => (
  <div
    className={clsx(
      'flex gap-4 text-sm',
      align === 'center' ? 'items-center' : 'items-start',
      className,
    )}
  >
    <div className="w-24 flex-shrink-0 text-neutral-7">{label}</div>
    <div className="flex-1 min-w-0 text-lime-6">{children}</div>
  </div>
)

// SectionDivider is the subheader that separates groups of InfoRows (e.g.
// "Configuration", "Live Metrics") while keeping them reading as one table: a
// small uppercase label followed by a hairline rule. Shared so every section on
// the Overview tab uses the exact same size and weight.
export const SectionDivider: FC<{
  label: ReactNode
  tone?: 'default' | 'danger'
}> = ({ label, tone = 'default' }) => (
  <div className="flex items-center gap-3">
    <span
      className={clsx(
        'text-xs font-medium uppercase tracking-wider',
        tone === 'danger' ? 'text-red-6/80' : 'text-neutral-7',
      )}
    >
      {label}
    </span>
    <span
      className={clsx(
        'h-px flex-1',
        tone === 'danger' ? 'bg-red-6/20' : 'bg-neutral-12',
      )}
    />
  </div>
)
