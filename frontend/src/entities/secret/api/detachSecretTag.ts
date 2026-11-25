import { apiClient } from '@/shared/utils/apiConfig'

// Detach is per ATTACHMENT, on the item path.
//
// Nothing is registered on the collection path: a DELETE to
// /secret-tags/{tag}/attachments answers 404, which reads as "detaching a tag is
// not supported" when it is the call that is wrong.
export const detachSecretTag = async (
  tag: string,
  attachmentId: string,
): Promise<void> => {
  await apiClient.delete(
    `/api/v1/secret-tags/${encodeURIComponent(tag)}/attachments/${attachmentId}`,
  )
}
