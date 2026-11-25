import { useQuery } from '@tanstack/react-query'

import { getFleetActivity, getFleetAttention, type ActivityParams } from './api'
import { undismissedTotal } from './dismissals'
import { useDismissals } from './dismissalsStore'

// One place for the query keys so the page and anything that later refreshes
// after an action invalidate the same caches.
export const fleetKeys = {
  attention: (ouId?: string) => ['fleet-attention', ouId ?? 'all'] as const,
  activity: (params: ActivityParams) => ['fleet-activity', params] as const,
}

/**
 * @param settling true while a remedy this page dispatched has not yet shown
 * up in the answer. A condition does not clear the moment a command is
 * accepted — an agent update takes until the agent reinstalls and reconnects
 * on the new version — so the row would otherwise sit unchanged and read as a
 * button that did nothing. While settling, ask again on a short interval.
 */
export const useFleetAttention = (ouId?: string, settling = false) =>
  useQuery({
    queryKey: fleetKeys.attention(ouId),
    queryFn: () => getFleetAttention(ouId),
    // The list is derived from live node state; a minute-stale answer would
    // have an operator acting on a condition already cleared.
    staleTime: settling ? 0 : 30 * 1000,
    refetchInterval: settling ? 5000 : false,
    // The page stops polling on its own by clearing its marks; this is the
    // backstop for a condition that will not clear on its own timescale. A
    // flashed bootloader stays pending until somebody reboots the node, which
    // may be tomorrow, and a tab left open must not poll until then.
    refetchIntervalInBackground: false,
  })

export const useFleetActivity = (params: ActivityParams, enabled = true) =>
  useQuery({
    queryKey: fleetKeys.activity(params),
    queryFn: () => getFleetActivity(params),
    enabled,
    // Keeps the table on screen while a filter change refetches, instead of
    // collapsing to the empty state and back.
    placeholderData: (previous) => previous,
  })

/**
 * How many conditions the fleet is carrying, for the nav badge.
 *
 * Deliberately the SAME query key as the unfiltered Status page, so the two
 * share one answer: navigating to Status shows the count's own data, and the
 * page's refetch updates the badge. A separate count endpoint would be a
 * second resolve of the same fleet on every page.
 *
 * `enabled` is the caller's permission gate — the header already knows whether
 * this section exists for the member, and asking for a list they may not read
 * would spend a 403 on every page load.
 */
export const useAttentionCount = (enabled: boolean) => {
  // Dismissals are per browser and live outside the query, so the badge
  // subtracts them here rather than the server pretending they do not exist.
  const dismissals = useDismissals()
  const { data } = useQuery({
    queryKey: fleetKeys.attention(undefined),
    queryFn: () => getFleetAttention(undefined),
    enabled,
    // Longer than the page's: the badge is an at-a-glance number, and this
    // resolve reads the node set, the unit tree and the utility ladder.
    staleTime: 2 * 60 * 1000,
  })
  if (!data) {
    return 0
  }
  return undismissedTotal(data.nodes, dismissals)
}
