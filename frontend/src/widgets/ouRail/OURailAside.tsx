import { FC, useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { OURail, OURailProps } from './index.tsx'

// Shared localStorage key so the collapsed state is consistent across every
// list page (Nodes, OS Images, Secrets, Workflows).
const STORAGE_KEY = 'ou-panel-collapsed'

export type OURailAsideProps = Omit<OURailProps, 'onCollapse'> & {
  // Override the persistence key when a page wants an independent collapse
  // state (defaults to the shared key).
  storageKey?: string
}

// The collapsible `<aside>` wrapper around the shared OU rail. It owns the
// collapse state (persisted across sessions) and renders either a thin icon
// strip with an expand chevron or the full rail with a collapse chevron, so
// every page gets identical collapse behavior for free.
export const OURailAside: FC<OURailAsideProps> = ({
  storageKey = STORAGE_KEY,
  ...railProps
}) => {
  const { t } = useTranslation()
  const [collapsed, setCollapsed] = useState(
    () => localStorage.getItem(storageKey) === '1',
  )
  const toggle = useCallback(() => {
    setCollapsed((prev) => {
      localStorage.setItem(storageKey, prev ? '0' : '1')
      return !prev
    })
  }, [storageKey])

  if (collapsed) {
    return (
      <aside className="hidden md:flex flex-none w-10 flex-col items-center border-r border-neutral-12 bg-neutral-14 py-4">
        <button
          type="button"
          onClick={toggle}
          aria-label={t('widgets.ouRail.show')}
          title={t('widgets.ouRail.title')}
          className="p-1.5 text-neutral-6 hover:text-neutral-2 transition-colors"
        >
          <svg
            className="w-4 h-4"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M9 5l7 7-7 7"
            />
          </svg>
        </button>
      </aside>
    )
  }

  return (
    <aside className="hidden md:flex flex-none w-64 flex-col border-r border-neutral-12 bg-neutral-14 overflow-y-auto py-6 px-2">
      <OURail {...railProps} onCollapse={toggle} />
    </aside>
  )
}
