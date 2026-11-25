import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import { InfoHint } from '@/shared/ui/infoHint'

// SubViewHeader is the shared header for every Actions-tab sub-view (Install OS,
// Run playbook, Terminal, confirmations…): a "← Back" control and a small title
// on the same line, so all sub-views read identically. An optional hint puts an
// info mark beside the title carrying the sub-view's standing explainer, so the
// panel body stays free of manual-page prose.
export const SubViewHeader: FC<{
  title: string
  onBack: () => void
  hint?: string
}> = ({ title, onBack, hint }) => {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-between">
      <button
        type="button"
        onClick={onBack}
        className="text-neutral-7 hover:text-neutral-5 text-sm"
      >
        {t('widgets.sidebars.node.subViewHeader.back')}
      </button>
      <div className="flex items-center gap-1.5">
        <div className="text-sm text-neutral-3">{title}</div>
        {hint ? <InfoHint text={hint} /> : null}
      </div>
    </div>
  )
}
