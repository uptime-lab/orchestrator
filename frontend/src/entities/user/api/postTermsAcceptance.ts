import { apiClient } from '@/shared/utils/apiConfig'

// postTermsAcceptance records that the signed-in user accepts the operator's
// terms of use. The backend stores it with a timestamp and writes the audit
// row; the path keeps the name the endpoint was introduced under.
export const postTermsAcceptance = async (): Promise<void> => {
  await apiClient.post('/api/v1/users/me/beta-acknowledgement')
}
