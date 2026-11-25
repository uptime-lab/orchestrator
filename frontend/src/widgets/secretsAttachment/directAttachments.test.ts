import { describe, expect, it } from 'vitest'

import type { EffectiveSecret, SecretAttachment } from '@/entities/secret'

import { directAttachments, mayEdit } from './directAttachments'

const node = 'n1'
const isDirect = (e: EffectiveSecret) => e.source.level === 'node'

const effective = (
  id: string,
  source: EffectiveSecret['source'],
): EffectiveSecret => ({
  id,
  name: `secret-${id}`,
  kind: 'env',
  key: 'K',
  sensitive: true,
  source,
})

describe('directAttachments', () => {
  it('lists a tag attached here that no secret carries yet', () => {
    const attachments: SecretAttachment[] = [
      { id: 'a1', tag: 'db', node_id: node },
      { id: 'a2', secret_id: 's1', node_id: node },
    ]
    const view = directAttachments(
      attachments,
      [effective('s1', { level: 'node', via: 'direct' })],
      isDirect,
      new Map(),
    )
    expect(view.tags.map((t) => [t.tag, t.attachmentId])).toEqual([
      ['db', 'a1'],
    ])
    expect(view.secrets.map((s) => [s.secretId, s.attachmentId])).toEqual([
      ['s1', 'a2'],
    ])
  })

  it('lists a tag whose every member is also attached directly', () => {
    const attachments: SecretAttachment[] = [
      { id: 'a1', tag: 'db', node_id: node },
      { id: 'a2', secret_id: 's1', node_id: node },
    ]
    const view = directAttachments(
      attachments,
      // The effective set is deduplicated by secret: the direct entry wins.
      [effective('s1', { level: 'node', via: 'direct' })],
      isDirect,
      new Map([['db', 1]]),
    )
    expect(view.tags).toEqual([
      { attachmentId: 'a1', tag: 'db', memberCount: 1 },
    ])
  })

  it('never lists an inherited entry as this scope’s own', () => {
    const view = directAttachments(
      [],
      [
        effective('s2', { level: 'ou', via: 'direct', ou_id: 'ou1' }),
        effective('s3', {
          level: 'ou',
          via: 'tag',
          tag: 'fleet',
          ou_id: 'ou1',
        }),
      ],
      isDirect,
      new Map(),
    )
    expect(view.secrets).toEqual([])
    expect(view.tags).toEqual([])
  })
})

describe('mayEdit', () => {
  it('allows changes only once the current attachments have loaded', () => {
    expect(mayEdit('loading')).toBe(false)
    expect(mayEdit('failed')).toBe(false)
    expect(mayEdit('loaded')).toBe(true)
  })
})
