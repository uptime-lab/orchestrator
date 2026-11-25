import { describe, expect, it } from 'vitest'

import { webusbAvailability } from './availability'

describe('webusbAvailability', () => {
  it('is available on a secure page with WebUSB', () => {
    expect(webusbAvailability({ secureContext: true, hasUsb: true })).toBe(
      'available',
    )
  })

  it('blames the address, not the browser, on a plain-HTTP page', () => {
    // A Chromium browser on an http:// LAN address has no navigator.usb.
    expect(webusbAvailability({ secureContext: false, hasUsb: false })).toBe(
      'insecure-context',
    )
    expect(webusbAvailability({ secureContext: false, hasUsb: true })).toBe(
      'insecure-context',
    )
  })

  it('blames the browser on a secure page without WebUSB', () => {
    expect(webusbAvailability({ secureContext: true, hasUsb: false })).toBe(
      'unsupported',
    )
  })
})
