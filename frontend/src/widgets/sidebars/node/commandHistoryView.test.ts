import { describe, expect, it } from 'vitest'

import {
  failedInstallStaysOnRecovery,
  isLongOutput,
  scriptTimeoutSeconds,
  showsReportedOutput,
} from './commandHistoryView'

describe('scriptTimeoutSeconds', () => {
  it('returns a positive number as is', () => {
    expect(scriptTimeoutSeconds(60)).toBe(60)
  })

  it('treats an absent, zero or non-numeric timeout as none', () => {
    expect(scriptTimeoutSeconds(undefined)).toBeNull()
    expect(scriptTimeoutSeconds(0)).toBeNull()
    expect(scriptTimeoutSeconds('60')).toBeNull()
    expect(scriptTimeoutSeconds(null)).toBeNull()
  })
})

describe('showsReportedOutput', () => {
  const result = { stdout: 'hello', exit_code: '0' }

  it('shows the output of a successful script', () => {
    expect(
      showsReportedOutput({ slug: 'run_script', status: 'success', result }),
    ).toBe(true)
  })

  it('shows what a failed command reported', () => {
    expect(
      showsReportedOutput({ slug: 'update_agent', status: 'failed', result }),
    ).toBe(true)
  })

  it('keeps other successful commands quiet', () => {
    expect(
      showsReportedOutput({ slug: 'read_file', status: 'success', result }),
    ).toBe(false)
  })

  it('shows nothing when nothing was reported', () => {
    expect(showsReportedOutput({ slug: 'run_script', status: 'success' })).toBe(
      false,
    )
    expect(
      showsReportedOutput({
        slug: 'run_script',
        status: 'success',
        result: {},
      }),
    ).toBe(false)
  })
})

describe('isLongOutput', () => {
  it('keeps a short output open', () => {
    expect(isLongOutput({ stdout: 'one\ntwo' })).toBe(false)
  })

  it('collapses many lines or many characters', () => {
    expect(isLongOutput({ stdout: 'x\n'.repeat(20) })).toBe(true)
    expect(isLongOutput({ stdout: 'x'.repeat(2000) })).toBe(true)
  })
})

describe('failedInstallStaysOnRecovery', () => {
  it('is true only for a failed install_os', () => {
    expect(
      failedInstallStaysOnRecovery({ slug: 'install_os', status: 'failed' }),
    ).toBe(true)
    expect(
      failedInstallStaysOnRecovery({ slug: 'install_os', status: 'success' }),
    ).toBe(false)
    expect(
      failedInstallStaysOnRecovery({ slug: 'reboot', status: 'failed' }),
    ).toBe(false)
  })
})
