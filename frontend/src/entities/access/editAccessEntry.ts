import { writeFromCurrent } from '@/shared/utils/writeFromCurrent'

import { getAccessEntry, updateAccessEntry } from './api'
import type { AccessEntry, UpdateAccessEntryRequest } from './api/types'

// Re-reads the entry, then writes the change `build` computes from it, so a
// list the page cached cannot undo a change saved since (scopeEdits).
export const editAccessEntry = (
  entryId: string,
  build: (current: AccessEntry) => UpdateAccessEntryRequest | null,
) =>
  writeFromCurrent(
    () => getAccessEntry(entryId),
    build,
    (request) => updateAccessEntry(entryId, request),
  )
