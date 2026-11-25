import { describe, expect, it } from 'vitest'

import { actionEscalates, escalationNoteKey } from './actionEscalation'

describe('actionEscalation', () => {
  // The two that escalate: neither consequence is visible in its label, and
  // both are granted by ticking a box.
  it('marks the two permissions that do more than their label says', () => {
    expect(actionEscalates('provisioning.sd')).toBe(true)
    expect(actionEscalates('ou.manage')).toBe(true)
    expect(escalationNoteKey('ou.manage')).toBe(
      'pages.team.actionNotes.ouManage',
    )
  })

  // node.terminal already says "(root shell)" in its own label, and
  // node.reboot / playbook.author grant exactly what they read as — a note on
  // every row would make the two that matter invisible again.
  it('leaves the honest labels alone', () => {
    expect(actionEscalates('node.reboot')).toBe(false)
    expect(actionEscalates('node.terminal')).toBe(false)
    expect(actionEscalates('playbook.author')).toBe(false)
    expect(escalationNoteKey('node.reboot')).toBeNull()
  })

  it('is silent about an action it has never seen', () => {
    expect(escalationNoteKey('something.new')).toBeNull()
  })
})
