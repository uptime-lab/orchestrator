import { describe, expect, it } from 'vitest'

import { describeAttachment } from './attachmentLabel'

const attachment = (over: Record<string, unknown>) => ({
  id: 'a1',
  tag: 'prod',
  ...over,
})

describe('describeAttachment', () => {
  it('names a unit and a node from the loaded lists', () => {
    expect(
      describeAttachment(
        attachment({ ou_id: 'ou-1' }),
        { 'ou-1': 'Lab A' },
        {},
      ),
    ).toEqual({ kind: 'unit', label: 'Lab A' })
    expect(
      describeAttachment(
        attachment({ node_id: 'n-1' }),
        {},
        { 'n-1': 'blade-3' },
      ),
    ).toEqual({ kind: 'node', label: 'blade-3' })
  })

  // The Secrets page does not load the node list, so an unnamed target is the
  // ordinary case. Falling back to the id keeps the row — and its Detach
  // button — present; an attachment that rendered as nothing would be one
  // nobody could remove.
  it('falls back to the id rather than dropping the row', () => {
    expect(describeAttachment(attachment({ node_id: 'n-9' }), {}, {})).toEqual({
      kind: 'node',
      label: 'n-9',
    })
  })

  it('says nothing about an attachment targeting neither', () => {
    expect(describeAttachment(attachment({}), {}, {})).toBeNull()
  })
})
