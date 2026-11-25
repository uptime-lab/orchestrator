// The sentence shown when a secret's tags will deliver it somewhere.
//
// Kept apart from the component so a test can import it without the store
// graph, which reaches `window` at import time and cannot load in the DOM-less
// test environment. Same reasoning as entities/permissions/can.ts.
//
// The copy lives in the locale file; the i18n side-effect import initialises
// the bundle so the test (which imports this module directly, without the app
// entry) reads real sentences instead of raw keys. In the app it is a no-op —
// the module is already loaded.
import '@/i18n'

import { t } from 'i18next'

// deliverySentence states what saving will do, in one plain sentence.
//
// Counts, not names: a tag can reach dozens of units, and the decision an
// operator makes here is "does this secret belong on that many machines", which
// a number answers and a list buries.
export const deliverySentence = (delivery: {
  tags: string[]
  ous: number
  nodes: number
}) => {
  const targets: string[] = []
  if (delivery.ous > 0) {
    targets.push(
      t('widgets.sidebars.secretTagDelivery.unitCount', {
        count: delivery.ous,
      }),
    )
  }
  if (delivery.nodes > 0) {
    targets.push(
      t('widgets.sidebars.secretTagDelivery.nodeCount', {
        count: delivery.nodes,
      }),
    )
  }
  const tagList = delivery.tags.map((tag) => `#${tag}`).join(', ')
  return t('widgets.sidebars.secretTagDelivery.sentence', {
    count: delivery.tags.length,
    tagList,
    targets: targets.join(t('widgets.sidebars.secretTagDelivery.joiner')),
  })
}
