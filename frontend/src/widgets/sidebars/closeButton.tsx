import { FC } from 'react'
import { useTranslation } from 'react-i18next'

// Node-sidebar-style close control: a chevron-right square that collapses the
// panel. Shared by every sidebar whose header action is a plain close (inner
// screens with real back-navigation keep their "← Back" link instead).
export const SidebarCloseButton: FC<{ onClose: () => unknown }> = ({
  onClose,
}) => {
  const { t } = useTranslation()
  return (
    <button
      type="button"
      onClick={onClose}
      aria-label={t('widgets.sidebars.closePanel')}
      title={t('widgets.sidebars.closePanel')}
      className="flex items-center justify-center w-6 h-6 rounded border border-neutral-11 text-neutral-6 hover:text-neutral-2 hover:border-neutral-9 transition-colors flex-shrink-0"
    >
      <svg
        className="w-3.5 h-3.5"
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
  )
}
