import { apiClient } from '@/shared/utils/apiConfig'

export type UnitDeviceProfileApplyReport = {
  dispatched: number
  queued: number
  skipped: { node_id: string; name: string; reason: string }[]
}

// The deliberate fleet button: runs the per-node device-profile apply over
// every node directly assigned to the unit. Per-node refusals come back in
// `skipped` with the platform's own wording; they never abort the sweep.
export const applyDeviceProfileToUnit = async (ouId: string) => {
  const { data } = await apiClient.post<{
    data: UnitDeviceProfileApplyReport
  }>(`/api/v1/organizational-units/${ouId}/device-profile/apply`)
  return data.data
}
