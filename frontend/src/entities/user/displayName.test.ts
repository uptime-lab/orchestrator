import { describe, expect, it } from 'vitest'

import { displayNameProblem, MAX_DISPLAY_NAME_LENGTH } from './displayName'

describe('displayNameProblem', () => {
  it('accepts an ordinary name', () => {
    expect(displayNameProblem('Ada Lovelace')).toBeNull()
  })

  it('refuses a blank name', () => {
    expect(displayNameProblem('')).toBe('required')
    expect(displayNameProblem('   ')).toBe('required')
  })

  it('refuses a name longer than the server stores', () => {
    expect(displayNameProblem('x'.repeat(MAX_DISPLAY_NAME_LENGTH))).toBeNull()
    expect(displayNameProblem('x'.repeat(MAX_DISPLAY_NAME_LENGTH + 1))).toBe(
      'tooLong',
    )
  })
})
