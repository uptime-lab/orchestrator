import { t } from 'i18next'

import { Button } from '@/shared/ui/button/button'
import {
  ButtonSize,
  ButtonStyle,
  ButtonVariant,
} from '@/shared/ui/button/types'

export type * from '@/shared/ui/button/types'

const buttonTypes: { kind: ButtonStyle; label: string }[] = [
  { kind: 'primary', label: 'Button' },
  { kind: 'secondary', label: 'Button' },
  { kind: 'tertiary', label: 'Button' },
]

const buttonsOfSize = (size: ButtonSize, variant: ButtonVariant = 'normal') => {
  return (
    <>
      {buttonTypes.map(({ kind, label }) => (
        <div className="border-b border-neutral-10 p-2" key={kind}>
          <div className="mb-1">
            <Button disabled={false} kind={kind} variant={variant} size={size}>
              {label}
            </Button>
          </div>
          <div className="mb-1">
            <Button kind={kind} variant="destructive" size={size}>
              {t('pages.sample.buttons.destructive')} - {label}
            </Button>
          </div>
          <div className="mb-1">
            <Button disabled={true} kind={kind} variant="normal" size={size}>
              {t('pages.sample.buttons.disabled')} - {label}
            </Button>
          </div>
        </div>
      ))}
      <div className="border-b border-neutral-10 p-2">
        <div className="mb-1">
          <Button kind="primary" variant="normal" grow={true} size={size}>
            {t('pages.sample.buttons.primary')}
          </Button>
        </div>
        <div className="mb-1">
          <Button kind="secondary" variant="normal" grow={true} size={size}>
            {t('pages.sample.buttons.secondary')}
          </Button>
        </div>
        <div className="mb-1">
          <Button kind="tertiary" variant="normal" grow={true} size={size}>
            {t('pages.sample.buttons.tertiary')}
          </Button>
        </div>
        <div className="mb-1">
          <Button variant="destructive" grow={true} size={size}>
            {t('pages.sample.buttons.destructive')}
          </Button>
        </div>
        <div className="mb-1">
          <Button disabled={true} variant="normal" grow={true} size={size}>
            {t('pages.sample.buttons.disabled')}
          </Button>
        </div>
      </div>
    </>
  )
}

export const ButtonSample = () => {
  return (
    <div>
      <h1 className="font-bold py-2">{t('pages.sample.buttons.label')}</h1>
      <hr className="py-2" />
      <div className="p-6">
        <div className="grid grid-cols-5 text-lg font-semibold">
          <div className="p-2"></div>
          <div className="border-b border-neutral-10 p-2 text-left">
            {t('pages.sample.buttons.primary')}
          </div>
          <div className="border-b border-neutral-10 p-2 text-left">
            {t('pages.sample.buttons.secondary')}
          </div>
          <div className="border-b border-neutral-10 p-2 text-left">
            {t('pages.sample.buttons.tertiary')}
          </div>
          <div className="border-b border-neutral-10 p-2 text-left">
            {t('pages.sample.buttons.full-width')}
          </div>
        </div>
        <div className="grid grid-cols-5 border-neutral-10">
          <div className="border-b border-neutral-10 p-2 text-right pr-10 font-semibold">
            {t('pages.sample.buttons.lg-section')}
          </div>
          {buttonsOfSize('lg')}
        </div>
        <div className="grid grid-cols-5 border-neutral-10">
          <div className="border-b border-neutral-10 p-2 text-right pr-10 font-semibold">
            {t('pages.sample.buttons.md-section')}
          </div>
          {buttonsOfSize('md')}
        </div>
        <div className="grid grid-cols-5 border-neutral-10">
          <div className="border-b border-neutral-10 p-2 text-right pr-10 font-semibold">
            {t('pages.sample.buttons.sm-section')}
          </div>
          {buttonsOfSize('sm')}
        </div>
      </div>
    </div>
  )
}
