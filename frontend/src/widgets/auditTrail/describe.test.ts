import { describe, expect, it } from 'vitest'

// The words come from the locale file now, so the bundle has to be initialised
// before the first t() call — otherwise every assertion reads a key back.
import '@/i18n'

import { describeAuditEvent } from './describe'

const event = (action: string, target_type: string, detail = '') => ({
  id: 'e1',
  actor_kind: 'user',
  action,
  target_type,
  target_id: 't1',
  detail,
  created_at: '2026-08-19T00:00:00Z',
})

// The stored action strings follow the TABLE names, not the UI's words, because
// rows already written cannot be renamed with a rebrand. The read side is where
// that gap is closed.
describe('describeAuditEvent', () => {
  it('uses the product word for a target the table names differently', () => {
    expect(
      describeAuditEvent(event('playbook.update', 'workflow_template')).summary,
    ).toBe('update playbook')
  })

  it('unpacks a device command slug', () => {
    expect(
      describeAuditEvent(event('node_command.erase_drive', 'node')).summary,
    ).toBe('dispatched erase drive to a node')
  })

  it('unpacks a member status change', () => {
    expect(
      describeAuditEvent(event('member.status_disabled', 'user')).summary,
    ).toBe('set a member to disabled')
  })

  it('reads the secret-value access as a sentence', () => {
    expect(
      describeAuditEvent(event('secret.values_read', 'secret')).summary,
    ).toBe('read secret values on secret')
  })

  // Never invent: an unknown action is quoted, because a trail that paraphrases
  // what it does not understand is worse than one that quotes it.
  it('falls back to the stored action rather than guessing', () => {
    expect(
      describeAuditEvent(event('something.brand_new', 'mystery_target'))
        .summary,
    ).toBe('brand new mystery_target')
  })

  it('gives the awkward generated names a sentence of their own', () => {
    expect(
      describeAuditEvent(
        event('utility.assignment_created', 'utility_assignment'),
      ).summary,
    ).toBe('created a utility assignment')
    expect(
      describeAuditEvent(event('role_setting.upsert', 'account')).summary,
    ).toBe('changed a tunable action on the account')
  })

  // Every action the trail records has words; none of these may fall back to
  // the quoted stored string.
  it.each([
    ['playbook.run_start', 'workflow_execution', 'started a run'],
    ['playbook.run_cancel', 'workflow_execution', 'cancelled a run'],
    ['node.approve', 'node', 'approved a node'],
    ['node.reject', 'node', 'rejected a node'],
    ['node.assign_ou', 'node', 'changed the unit of a node'],
    ['node.policy_update', 'node', 'changed the boot or SSH policy of a node'],
    ['entitlement.apply', 'entitlement', 'applied an entitlement'],
    [
      'entitlement.device_cap_reached',
      'entitlement',
      'reached the device cap of the entitlement',
    ],
  ])('words %s', (action, target, summary) => {
    expect(describeAuditEvent(event(action, target)).summary).toBe(summary)
  })

  it('carries the detail through untouched', () => {
    expect(
      describeAuditEvent(event('secret.update', 'secret', 'renamed')).detail,
    ).toBe('renamed')
  })
})
