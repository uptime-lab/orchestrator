import { t } from 'i18next'

import {
  deviceCapNotice,
  deviceCapRefusal,
  isDeviceCapUnverified,
  type DeviceCapLine,
} from './deviceCap'

const line = (l: DeviceCapLine) =>
  t(`entities.entitlement.deviceCap.${l.key}`, l.values ?? {})

// deviceCapReasonText is the one-line wording of a device-limit refusal for a
// surface that has no room for the notice: a per-node result line or a toast.
// A limit that could not be checked is never worded as the limit reached.
// Undefined for any other error, so the caller shows the server's message.
export const deviceCapReasonText = (error: unknown): string | undefined => {
  if (isDeviceCapUnverified(error)) {
    return t('entities.entitlement.deviceCap.unverified')
  }
  const refusal = deviceCapRefusal(error)
  if (!refusal) return undefined
  const model = deviceCapNotice(refusal)
  return [model.title, ...model.lines].map(line).join(' ')
}
