import { describe, expect, it } from 'vitest'

import { installBlocker, preselectedImageId } from './installable'

const ready = {
  id: 'ready',
  file_storage_info: { storage_type: 'local', file_path: 'images/a.img.xz' },
  checksum: 'abc',
}

describe('installBlocker', () => {
  it('accepts an uploaded, checksummed, idle image', () => {
    expect(installBlocker(ready)).toBeNull()
  })

  it('refuses an image with no uploaded file', () => {
    expect(installBlocker({ checksum: 'abc' })).toBe('noFile')
    expect(
      installBlocker({
        file_storage_info: { storage_type: 'local', file_path: '' },
      }),
    ).toBe('noFile')
  })

  it('refuses an image whose checksum is not known yet', () => {
    expect(installBlocker({ ...ready, checksum: '' })).toBe('noChecksum')
  })

  it('refuses an image that is being processed', () => {
    expect(
      installBlocker({ ...ready, compression_status: 'in_progress' }),
    ).toBe('busy')
    expect(installBlocker({ ...ready, compression_status: 'uploading' })).toBe(
      'busy',
    )
  })

  it('does not treat a failed compression as busy', () => {
    expect(
      installBlocker({ ...ready, compression_status: 'failed' }),
    ).toBeNull()
  })
})

describe('preselectedImageId', () => {
  const empty = { id: 'empty' }

  it('preselects the effective default when it is installable', () => {
    expect(preselectedImageId([empty, ready], 'ready')).toBe('ready')
  })

  it('preselects nothing for a default with no file', () => {
    expect(preselectedImageId([empty, ready], 'empty')).toBe('')
  })

  it('preselects nothing when no default is set or it is not listed', () => {
    expect(preselectedImageId([ready], null)).toBe('')
    expect(preselectedImageId([ready], 'gone')).toBe('')
  })
})
