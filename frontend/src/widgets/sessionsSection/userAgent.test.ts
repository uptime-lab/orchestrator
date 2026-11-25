import { describe, expect, it } from 'vitest'

import { describeUserAgent } from './userAgent'

describe('describeUserAgent', () => {
  it('names Chrome on macOS', () => {
    expect(
      describeUserAgent(
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.86 Safari/537.36',
      ),
    ).toEqual({ browser: 'Chrome', os: 'macOS' })
  })

  it('names Edge rather than the Chrome it also mentions', () => {
    expect(
      describeUserAgent(
        'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.86 Safari/537.36 Edg/131.0.2903.70',
      ),
    ).toEqual({ browser: 'Edge', os: 'Windows' })
  })

  it('names Firefox on Linux and Safari on iOS', () => {
    expect(
      describeUserAgent(
        'Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0',
      ),
    ).toEqual({ browser: 'Firefox', os: 'Linux' })
    expect(
      describeUserAgent(
        'Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1',
      ),
    ).toEqual({ browser: 'Safari', os: 'iOS' })
  })

  it('does not recognise a non-browser client or an empty value', () => {
    expect(describeUserAgent('curl/8.7.1')).toBeNull()
    expect(describeUserAgent('python-requests/2.32.3')).toBeNull()
    expect(describeUserAgent('')).toBeNull()
    expect(describeUserAgent(undefined)).toBeNull()
  })
})
