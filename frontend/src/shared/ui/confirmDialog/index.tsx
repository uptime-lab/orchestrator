import { Dialog, DialogPanel, DialogTitle } from '@headlessui/react'
import { FC, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/shared/ui/button/button.tsx'

export type ConfirmDialogProps = {
  open: boolean
  title?: string
  message: ReactNode
  confirmText?: string
  cancelText?: string
  destructive?: boolean
  busy?: boolean
  // Notification shape: a single dismiss button (confirmText) instead of the
  // Cancel / Confirm pair — for dialogs that only inform.
  hideCancel?: boolean
  onConfirm: () => void
  onCancel: () => void
}

// Styled replacement for window.confirm(): a centered modal with Cancel /
// Confirm actions. Keep it controlled — the caller owns the open state.
export const ConfirmDialog: FC<ConfirmDialogProps> = ({
  open,
  title,
  message,
  confirmText,
  cancelText,
  destructive = false,
  busy = false,
  hideCancel = false,
  onConfirm,
  onCancel,
}) => {
  const { t } = useTranslation()
  // The defaults cannot live on the parameters: `t` is only callable while the
  // component is rendering, not at module scope. Callers that pass their own
  // text are unaffected.
  const confirmLabel = confirmText ?? t('shared.confirmDialog.continue')
  const cancelLabel = cancelText ?? t('shared.confirmDialog.cancel')
  return (
    <Dialog open={open} onClose={onCancel} className="relative z-50">
      <div className="fixed inset-0 bg-neutral-15/80" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-md bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          {title && (
            <DialogTitle className="text-sm font-bold uppercase text-neutral-1">
              {title}
            </DialogTitle>
          )}
          <div className="text-sm text-neutral-4">{message}</div>
          <div className="flex gap-2 justify-end">
            {!hideCancel && (
              <Button
                size="md"
                kind="secondary"
                onClick={onCancel}
                disabled={busy}
              >
                {cancelLabel}
              </Button>
            )}
            <Button
              size="md"
              kind="primary"
              variant={destructive ? 'destructive' : 'normal'}
              onClick={onConfirm}
              disabled={busy}
            >
              {confirmLabel}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}
