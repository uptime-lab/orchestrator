import { describe, expect, it } from 'vitest'

import { deliverySentence } from './secretTagDelivery'

// Adding a tag to a secret is a DELIVERY decision: tag attachments have dynamic
// membership, so tagging a secret with #infra reaches every unit and node #infra
// is attached to, immediately. The only place that was visible was the Tags tab
// — on the tag, not at the moment a secret acquires one.
describe('deliverySentence', () => {
  it('names both target kinds', () => {
    expect(deliverySentence({ tags: ['infra'], ous: 3, nodes: 2 })).toBe(
      '#infra is already attached to 3 units and 2 nodes. Saving delivers this secret there.',
    )
  })

  it('omits a target kind with no members rather than saying zero', () => {
    expect(deliverySentence({ tags: ['infra'], ous: 0, nodes: 4 })).toBe(
      '#infra is already attached to 4 nodes. Saving delivers this secret there.',
    )
  })

  it('agrees in number, so one unit does not read as "1 units"', () => {
    expect(deliverySentence({ tags: ['infra'], ous: 1, nodes: 1 })).toBe(
      '#infra is already attached to 1 unit and 1 node. Saving delivers this secret there.',
    )
  })

  it('lists every delivering tag', () => {
    expect(deliverySentence({ tags: ['infra', 'ci'], ous: 2, nodes: 0 })).toBe(
      '#infra, #ci are already attached to 2 units. Saving delivers this secret there.',
    )
  })
})
