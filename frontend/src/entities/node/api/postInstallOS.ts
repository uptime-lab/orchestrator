import { apiClient } from '@/shared/utils/apiConfig'

import { installOSCommandBody, InstallOSRequest } from './installOSCommand'

export type { InstallOSRequest } from './installOSCommand'

export const postInstallOS = async (
  nodeId: string,
  request: InstallOSRequest,
): Promise<void> => {
  await apiClient.post(
    `/api/v1/nodes/${nodeId}/command`,
    installOSCommandBody(request),
  )
}
