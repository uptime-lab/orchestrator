import { t } from 'i18next'

import { Input } from './input'

export { Input } from './input'

export const InputSample = () => {
  return (
    <div>
      <h1 className="font-bold py-2">{t('pages.sample.input.label')}</h1>
      <hr className="py-2" />
      <div className="p-6">
        <div className="grid grid-cols-5 border-b border-neutral-10">
          <div className="p-2 text-right pr-10 font-semibold">
            {t('pages.sample.input.with-label')}
          </div>
          <div className="col-span-4 py-2">
            <Input
              label="Input Label"
              placeholder={t('pages.sample.input.placeholder')}
            />
          </div>
        </div>
        <div className="grid grid-cols-5 border-b border-neutral-10">
          <div className="p-2 text-right pr-10 font-semibold">
            {t('pages.sample.input.without-label')}
          </div>
          <div className="col-span-4 py-2">
            <Input placeholder={t('pages.sample.input.placeholder')} />
          </div>
        </div>
      </div>
    </div>
  )
}
