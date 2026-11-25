import type { SecretAttachment } from '@/entities/secret'

// Naming a tag attachment's target.
//
// Pure, so a test can import it without the store graph — and because the
// fallback rule is the part worth pinning: an attachment whose target is not in
// the loaded list still renders, as the id. The Secrets page does not load the
// node list, so "unknown" is the ordinary case here, not an error — and an
// attachment that rendered as nothing would be one nobody could remove.
export type AttachmentTarget = { kind: 'unit' | 'node'; label: string }

export const describeAttachment = (
  attachment: SecretAttachment,
  ouNames: Record<string, string>,
  nodeNames: Record<string, string>,
): AttachmentTarget | null => {
  if (attachment.ou_id) {
    return {
      kind: 'unit',
      label: ouNames[attachment.ou_id] || attachment.ou_id,
    }
  }
  if (attachment.node_id) {
    return {
      kind: 'node',
      label: nodeNames[attachment.node_id] || attachment.node_id,
    }
  }
  // An attachment targeting neither is account-wide on the secret side, which a
  // TAG attachment cannot be: the API requires a unit or a node. Rendering
  // nothing is right, and returning null says so rather than inventing a word.
  return null
}
