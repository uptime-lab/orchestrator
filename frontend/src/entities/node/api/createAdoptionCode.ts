import { apiClient } from '@/shared/utils/apiConfig'

type ApiResponse<T> = { data: T }

export type AdoptionCodeResult = {
  code: string
  expires_at: string
  install_command: string
}

// Mints a fresh one-time adoption code for the node and returns the ready
// install command. The code is single-use and short-lived; re-minting revokes
// the previous one, so call this each time the command is shown.
export const createAdoptionCode = async (
  nodeId: string,
): Promise<AdoptionCodeResult> => {
  const response = await apiClient.post<ApiResponse<AdoptionCodeResult>>(
    `/api/v1/nodes/${nodeId}/adoption-code`,
  )
  return response.data.data
}
