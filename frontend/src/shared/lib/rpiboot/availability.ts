// Whether this page can flash over WebUSB, and if not, why.
//
// WebUSB exists only in a secure context: on a page served over plain HTTP the
// browser removes `navigator.usb` entirely, so a missing `navigator.usb` alone
// cannot tell "wrong browser" from "wrong address". The secure-context flag is
// asked first, and it is the browser's own verdict rather than a URL test:
// http://localhost counts as secure, a LAN address over http does not.
export type WebUSBAvailability =
  'available' | 'insecure-context' | 'unsupported'

export type WebUSBEnvironment = {
  secureContext: boolean
  hasUsb: boolean
}

export const webusbAvailability = (
  env: WebUSBEnvironment,
): WebUSBAvailability => {
  if (!env.secureContext) return 'insecure-context'
  if (!env.hasUsb) return 'unsupported'
  return 'available'
}

export const currentWebUSBEnvironment = (): WebUSBEnvironment => ({
  secureContext: typeof window !== 'undefined' && window.isSecureContext,
  hasUsb: typeof navigator !== 'undefined' && !!navigator.usb,
})
