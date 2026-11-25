import { describe, expect, it } from 'vitest'

import { installOSCommandBody } from '@/entities/node/api/installOSCommand'

import { installRequestFromPanel } from './installRequest'

describe('installRequestFromPanel', () => {
  const body = installOSCommandBody(
    installRequestFromPanel('image-1', '/dev/nvme0n1'),
  )

  it('lets the agent overwrite a disk that already has partitions', () => {
    expect(body.params.wipe).toBe(true)
  })

  it('never reboots into a freshly written disk', () => {
    expect(body.params.reboot).toBe(false)
  })

  it('sends the chosen image and device as an install_os command', () => {
    expect(body).toEqual({
      command: 'install_os',
      params: {
        image_id: 'image-1',
        target_device: '/dev/nvme0n1',
        reboot: false,
        wipe: true,
      },
    })
  })
})
