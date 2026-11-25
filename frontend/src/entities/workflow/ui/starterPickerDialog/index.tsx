import { Dialog, DialogPanel, DialogTitle } from '@headlessui/react'
import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/shared/ui/button/button.tsx'

import type { WorkflowStarter } from '../../api/types'

export type StarterPickerDialogProps = {
  open: boolean
  starters: WorkflowStarter[]
  loading: boolean
  busy: boolean
  error?: string
  onSelect: (starter: WorkflowStarter) => void
  onClose: () => void
}

// "Create from template" picker: the built-in starter catalog as a clickable
// list. Selecting a starter imports its document as a draft playbook and the
// caller navigates to the editor.
export const StarterPickerDialog: FC<StarterPickerDialogProps> = ({
  open,
  starters,
  loading,
  busy,
  error,
  onSelect,
  onClose,
}) => {
  const { t } = useTranslation()

  return (
    <Dialog open={open} onClose={onClose} className="relative z-50">
      <div className="fixed inset-0 bg-neutral-15/80" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-lg bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase text-neutral-1">
            {t('entities.workflow.starterPickerDialog.title')}
          </DialogTitle>
          <div className="text-sm text-neutral-4">
            {t('entities.workflow.starterPickerDialog.description')}
          </div>

          {error && (
            <div className="text-sm text-red-5 border border-red-8 rounded px-3 py-2">
              {error}
            </div>
          )}

          <div className="flex flex-col gap-2 max-h-96 overflow-y-auto">
            {loading && (
              <div className="text-sm text-neutral-5 py-6 text-center">
                {t('entities.workflow.starterPickerDialog.loading')}
              </div>
            )}
            {!loading &&
              starters.map((starter) => (
                <button
                  key={starter.id}
                  type="button"
                  disabled={busy}
                  onClick={() => onSelect(starter)}
                  className="text-left rounded border border-neutral-11 bg-neutral-13 hover:border-lime-7 transition-colors px-4 py-3 disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  <div className="flex items-center justify-between gap-3">
                    <span className="font-mono font-bold text-sm text-neutral-1">
                      {starter.name}
                    </span>
                    <span className="text-xxs uppercase text-neutral-6 border border-neutral-10 rounded-[3px] px-1.5 py-px flex-shrink-0">
                      {starter.category}
                    </span>
                  </div>
                  <div className="text-xs text-neutral-5 mt-1">
                    {starter.description}
                  </div>
                  <div className="text-xxs text-neutral-7 mt-1.5 font-mono">
                    {starter.block_types
                      .filter((blockType) => !blockType.startsWith('trigger.'))
                      .map((blockType) =>
                        blockType.split('.').slice(1).join('.'),
                      )
                      .join(' · ')}
                  </div>
                </button>
              ))}
            {!loading && starters.length === 0 && !error && (
              <div className="text-sm text-neutral-5 py-6 text-center">
                {t('entities.workflow.starterPickerDialog.empty')}
              </div>
            )}
          </div>

          <div className="flex justify-end">
            <Button
              size="md"
              kind="secondary"
              onClick={onClose}
              disabled={busy}
            >
              {t('entities.workflow.starterPickerDialog.cancel')}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}
