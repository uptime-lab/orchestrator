import { useTranslation } from 'react-i18next'

import {
  deviceCapNotice,
  type DeviceCapLine,
  type DeviceCapRefusal,
} from '../deviceCap'
import { CONTACT_MARK, ContactText } from './ContactText'

type Props = {
  refusal: DeviceCapRefusal
}

// DeviceCapNotice is the device-26 message, rendered wherever a device is
// added: the install is at its cap and the devices already admitted keep
// working. The operator also gets the install's numbers and what to do about
// them; any other account gets its own count and who acts. At the
// account's own limit it says so, with the account's numbers and the
// contact to write to.
export const DeviceCapNotice = ({ refusal }: Props) => {
  const { t } = useTranslation()
  const model = deviceCapNotice(refusal)
  const text = (line: DeviceCapLine) =>
    t(`entities.entitlement.deviceCap.${line.key}`, line.values ?? {})
  return (
    <div className="bg-amber-6/10 border border-amber-6/50 text-amber-4 px-4 py-3 rounded text-sm flex flex-col gap-1">
      <div className="font-bold">{text(model.title)}</div>
      {model.lines.map((line) => (
        <div key={line.key}>
          {line.key === 'accountRemedy' ? (
            <ContactText
              sentence={t('entities.entitlement.deviceCap.accountRemedy', {
                contact: CONTACT_MARK,
              })}
              contact={model.contact ?? ''}
              className="underline text-amber-3"
            />
          ) : (
            text(line)
          )}
          {line.key === 'remedy' && model.subscriptionUrl && (
            <>
              {' '}
              <a
                href={model.subscriptionUrl}
                target="_blank"
                rel="noreferrer"
                className="underline text-amber-3"
              >
                {t('entities.entitlement.deviceCap.subscriptionLink')}
              </a>
            </>
          )}
        </div>
      ))}
    </div>
  )
}
