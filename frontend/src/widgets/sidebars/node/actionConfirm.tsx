import { FC, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/shared/ui/button/button.tsx'

import { SubViewHeader } from './subViewHeader.tsx'

// ActionConfirm is a slide-in confirmation sub-view for the Actions tab — used
// instead of a modal popup so confirmations feel like the other sub-views
// (Install OS, Run playbook…). Back or Cancel returns to the action list.
export const ActionConfirm: FC<{
  title: string
  message: string
  confirmText: string
  destructive?: boolean
  // Extra classes for the confirm button (e.g. a violet tone for recovery).
  confirmClassName?: string
  // A line under the message that is not part of the question (the GPL
  // source notice under the recovery confirm); rendered as given.
  note?: ReactNode
  onConfirm: () => void
  onBack: () => void
}> = ({
  title,
  message,
  confirmText,
  destructive = false,
  confirmClassName,
  note,
  onConfirm,
  onBack,
}) => {
  const { t } = useTranslation()

  return (
    <div className="flex flex-col gap-4">
      <SubViewHeader title={title} onBack={onBack} />
      <div className="text-sm text-neutral-4">{message}</div>
      {note}
      <div className="flex gap-2">
        <Button
          variant={destructive ? 'destructive' : 'normal'}
          onClick={onConfirm}
          className={confirmClassName}
          grow
        >
          {confirmText}
        </Button>
        <Button variant="normal" kind="secondary" onClick={onBack}>
          {t('widgets.sidebars.node.actionConfirm.cancel')}
        </Button>
      </div>
    </div>
  )
}
