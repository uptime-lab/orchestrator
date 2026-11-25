import { describe, expect, it } from 'vitest'

import { buildCaptureParams, defaultCaptureTarget } from './captureTarget'

const devices = { '/dev/mmcblk0': {}, '/dev/nvme0n1': {} }

describe('capture target', () => {
  // The agent fills in a fixed disk when none is sent, which is the wrong
  // disk on a node that runs from SD or USB. The disk always travels.
  it('never sends a capture without a disk', () => {
    expect(buildCaptureParams('b', false, '')).toBeNull()
    expect(buildCaptureParams(' b ', true, '/dev/mmcblk0')).toEqual({
      title: 'b',
      generalize: true,
      target_device: '/dev/mmcblk0',
    })
  })

  it('preselects the disk the node reports running from', () => {
    expect(defaultCaptureTarget(devices, '/dev/mmcblk0')).toBe('/dev/mmcblk0')
  })

  it('preselects nothing when the node cannot say', () => {
    // Recovery runs from RAM and reports no root disk.
    expect(defaultCaptureTarget(devices, undefined)).toBe('')
    // A root disk the node does not list is not something to offer.
    expect(defaultCaptureTarget(devices, '/dev/sda')).toBe('')
  })
})
