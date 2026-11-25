import type { EffectiveSecret, SecretAttachment } from '@/entities/secret'

// Where the panel's read of the scope stands. Every change is made against
// the scope's own attachment list, so nothing may be changed until that list
// has loaded: an edit made from an empty or failed read would act on a state
// the operator never saw.
export type LoadState = 'loading' | 'loaded' | 'failed'

export const mayEdit = (state: LoadState): boolean => state === 'loaded'

// A secret attached directly at this scope. `entry` is its effective-set
// entry, used for the name and kind; it is absent only if the two reads
// disagree (a secret deleted between them).
export type DirectSecretRow = {
  attachmentId: string
  secretId: string
  entry?: EffectiveSecret
}

// A tag attached directly at this scope. A tag no secret carries yet is
// listed too: it delivers nothing today and every secret tagged later.
export type DirectTagRow = {
  attachmentId: string
  tag: string
  memberCount: number
}

// The scope's own attachments, from its attachment list. The effective set
// only supplies names: it cannot stand in for the list, because it omits a
// tag no secret carries and folds a tag's members into their direct entries.
export const directAttachments = (
  attachments: SecretAttachment[],
  effective: EffectiveSecret[],
  isDirect: (entry: EffectiveSecret) => boolean,
  tagCounts: ReadonlyMap<string, number>,
): { secrets: DirectSecretRow[]; tags: DirectTagRow[] } => {
  const directEntries = new Map<string, EffectiveSecret>()
  const deliveredViaTag = new Map<string, number>()
  for (const entry of effective) {
    if (!isDirect(entry)) continue
    if (entry.source.via === 'direct') directEntries.set(entry.id, entry)
    else if (entry.source.tag)
      deliveredViaTag.set(
        entry.source.tag,
        (deliveredViaTag.get(entry.source.tag) ?? 0) + 1,
      )
  }
  const secrets: DirectSecretRow[] = []
  const tags: DirectTagRow[] = []
  for (const att of attachments) {
    if (att.secret_id) {
      secrets.push({
        attachmentId: att.id,
        secretId: att.secret_id,
        entry: directEntries.get(att.secret_id),
      })
    } else if (att.tag) {
      tags.push({
        attachmentId: att.id,
        tag: att.tag,
        memberCount:
          tagCounts.get(att.tag) ?? deliveredViaTag.get(att.tag) ?? 0,
      })
    }
  }
  return { secrets, tags }
}
