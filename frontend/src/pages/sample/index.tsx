import { useTranslation } from 'react-i18next'

import { ButtonSample } from '@/shared/ui/button'
import { InputSample } from '@/shared/ui/input'

// eslint-disable-next-line @typescript-eslint/no-empty-object-type
type SamplePageProps = {}

export const SamplePage = ({ ...rest }: SamplePageProps) => {
  const { t } = useTranslation()

  return (
    <div className="h-full w-full overflow-y-auto p-4 pt-8 md:p-8" {...rest}>
      <h1 className="text-lg uppercase font-bold mb-6">
        {t('pages.sample.title')}
      </h1>
      <ButtonSample />
      <InputSample />
    </div>
  )
}
