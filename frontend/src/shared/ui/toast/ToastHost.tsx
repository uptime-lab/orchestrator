import clsx from 'clsx'
import { useUnit } from 'effector-react'
import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import { $toasts, Toast, toastDismissed, ToastTone } from './model'

const toneClasses: Record<ToastTone, string> = {
  success: 'border-green-8 text-green-5',
  error: 'border-red-7 text-red-5',
  warning: 'border-orange-5 text-orange-5',
  info: 'border-lightBlue-8 text-lightBlue-5',
}

const ToastCard: FC<{ toast: Toast }> = ({ toast }) => {
  const { t } = useTranslation()
  const toneLabel: Record<ToastTone, string> = {
    success: t('shared.toast.toneSuccess'),
    error: t('shared.toast.toneError'),
    warning: t('shared.toast.toneWarning'),
    info: t('shared.toast.toneInfo'),
  }

  return (
    <div
      role={toast.tone === 'error' ? 'alert' : 'status'}
      className={clsx(
        'pointer-events-auto w-80 border bg-neutral-15 px-3 py-2 shadow-lg',
        toneClasses[toast.tone],
      )}
    >
      <div className="flex items-start gap-2">
        <span className="mt-[2px] shrink-0 text-[10px] tracking-wider">
          {toneLabel[toast.tone]}
        </span>
        <div className="min-w-0 flex-1">
          <p className="break-words text-sm text-neutral-3">{toast.message}</p>
          {toast.detail && (
            <p className="mt-1 break-words text-xs text-neutral-6">
              {toast.detail}
            </p>
          )}
        </div>
        <button
          type="button"
          aria-label={t('shared.toast.dismiss')}
          onClick={() => toastDismissed(toast.id)}
          className="shrink-0 px-1 text-neutral-6 hover:text-neutral-3"
        >
          ×
        </button>
      </div>
    </div>
  )
}

/**
 * Renders the toast queue. Mounted once in the main layout.
 *
 * Bottom-right and pointer-events-none on the container so it never blocks the
 * node panel, which is the surface most of these messages are about; only the
 * cards themselves take clicks, because an error toast has to be dismissible
 * (it does not auto-dismiss on purpose).
 */
export const ToastHost: FC = () => {
  const toasts = useUnit($toasts)
  if (toasts.length === 0) return null

  return (
    <div className="pointer-events-none fixed bottom-4 right-4 z-[100] flex flex-col gap-2">
      {toasts.map((toast) => (
        <ToastCard key={toast.id} toast={toast} />
      ))}
    </div>
  )
}
