import { useUnit } from 'effector-react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/shared/ui/confirmDialog'

import { $sessionSwitchedElsewhere } from '../model.ts'

// SessionSwitchedNotice covers the app once another tab has replaced this
// tab's session with a different user or account. Everything on screen
// belongs to the old one and nothing more is sent, so the only way on is a
// reload, which starts over as whoever is signed in now.
export const SessionSwitchedNotice = () => {
  const { t } = useTranslation()
  const switched = useUnit($sessionSwitchedElsewhere)
  const reload = () => window.location.reload()
  return (
    <ConfirmDialog
      open={switched}
      title={t('entities.auth.sessionSwitched.title')}
      message={t('entities.auth.sessionSwitched.message')}
      confirmText={t('entities.auth.sessionSwitched.reload')}
      hideCancel
      onConfirm={reload}
      // Not dismissable: behind it is a screen that can no longer act.
      onCancel={() => {}}
    />
  )
}
