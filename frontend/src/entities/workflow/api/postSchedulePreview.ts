import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

export type SchedulePreview = {
  valid: boolean
  // Present when valid is false — the same refusal a save would give.
  error?: string
  // One sentence, e.g. "At 03:00 UTC on Monday".
  description?: string
  // UTC instants, soonest first.
  next_runs: string[]
  // Valid, and yet fires never: '0 0 30 2 *' saves cleanly and does nothing
  // forever. Invisible in the expression, so the server says it outright.
  never_fires?: boolean
}

// The description and the run list come from the server on purpose: they are
// produced by the same code that decides whether a playbook fires. Computing
// either in the browser would be a second implementation of the cron dialect,
// free to tell the operator something the scheduler disagrees with.
export const postSchedulePreview = async (
  cron: string,
  count = 5,
): Promise<SchedulePreview> => {
  const response = await apiClient.post<ApiResponse<SchedulePreview>>(
    '/api/v1/schedule/preview',
    { cron, count },
  )
  return response.data.data
}
