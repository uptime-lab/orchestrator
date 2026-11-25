import { FC } from 'react'
import { useTranslation } from 'react-i18next'

// SourceOfferNote is the one-sentence GPL notice shown wherever the recovery
// image leaves the platform: the SD bundle download and the reboot into
// recovery. The image aggregates GPL software, so whoever receives it is owed
// its source; every install serves that offer at the tokenless /source/ route
// (a 404 there means the install publishes none). The path is a root route on
// the same host, so a relative href reaches it through the reverse proxy.
export const SourceOfferNote: FC<{ subject: 'bundle' | 'image' }> = ({
  subject,
}) => {
  const { t } = useTranslation()
  return (
    <div className="text-xs text-neutral-4">
      {subject === 'bundle'
        ? t('shared.sourceOfferNote.bundleIntro')
        : t('shared.sourceOfferNote.imageIntro')}{' '}
      <a href="/source/" target="_blank" rel="noreferrer" className="underline">
        {t('shared.sourceOfferNote.link')}
      </a>
      {t('shared.sourceOfferNote.outro')}
    </div>
  )
}
