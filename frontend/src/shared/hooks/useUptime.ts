import { t } from 'i18next'
import { useEffect, useState } from 'react'

import type { NodeStatus } from '@/entities/node/api/types'

type UseUptimeParams = {
  bootedAt?: number // Unix timestamp (seconds since epoch); server-authoritative, clock-independent
  lastSeenAt?: number // Unix timestamp (seconds since epoch)
  status: NodeStatus
}

/**
 * Formats an uptime duration compactly using its two largest non-zero units,
 * e.g. "83d 19h", "3h 05m", "5m 12s", "42s". Seconds-precision is only shown
 * under a minute — a ticking seconds counter on a multi-day uptime is noise.
 */
function formatUptime(seconds: number): string {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const secs = seconds % 60

  const pad = (n: number) => String(n).padStart(2, '0')

  if (days > 0) {
    return t('shared.hooks.uptime.daysHours', { days, hours: pad(hours) })
  }
  if (hours > 0) {
    return t('shared.hooks.uptime.hoursMinutes', {
      hours,
      minutes: pad(minutes),
    })
  }
  if (minutes > 0) {
    return t('shared.hooks.uptime.minutesSeconds', {
      minutes,
      seconds: pad(secs),
    })
  }
  return t('shared.hooks.uptime.seconds', { seconds: secs })
}

/**
 * Formats seconds into largest unit with "ago" suffix for offline nodes
 * Examples: "1s ago", "5m ago", "2h ago", "3d ago"
 */
function formatAgo(seconds: number): string {
  if (seconds < 60) {
    return t('shared.hooks.uptime.secondsAgo', { seconds })
  }

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) {
    return t('shared.hooks.uptime.minutesAgo', { minutes })
  }

  const hours = Math.floor(seconds / 3600)
  if (hours < 24) {
    return t('shared.hooks.uptime.hoursAgo', { hours })
  }

  const days = Math.floor(seconds / 86400)
  return t('shared.hooks.uptime.daysAgo', { days })
}

// A device that is UP has a meaningful uptime: it is running and reporting.
// "maintenance" belongs here — it means a command is executing and the device
// heartbeats throughout.
const UP_STATUSES: NodeStatus[] = ['online', 'maintenance']

// A device that is DOWN has no uptime; the honest answer is when it was last
// heard from. Same grouping as nodeDisplayState's "offline".
const DOWN_STATUSES: NodeStatus[] = ['offline', 'unreachable', 'shutdown']

/**
 * Custom hook that calculates and displays uptime or countdown from last seen
 * - For a device that is up (online or in maintenance): uptime from booted_at (ticking)
 * - For a device that is down: static "ago" format from last_seen_at (no ticking)
 * - For everything in between (restarting, installing, flashing, adopting…): a
 *   dash, because neither number means anything there.
 *
 * The third case is the whole point. It used to fall into the
 * uptime arithmetic with `last_seen_at` as its origin, so a node that was still
 * heartbeating rendered "uptime: 1s" — time since the last heartbeat, labelled
 * as uptime. Updating a bootloader pins the node to `restarting` at dispatch,
 * so a machine with weeks of uptime would report one second.
 *
 * The node list did not show it because it substituted a dash itself before
 * passing the value on. That workaround is now gone: one rule about what uptime
 * means, in the place that computes it, rather than a correction at each caller
 * that happens to remember.
 */
export function useUptime({
  bootedAt,
  lastSeenAt,
  status,
}: UseUptimeParams): string {
  const [uptime, setUptime] = useState<string>(() =>
    t('shared.hooks.uptime.blank'),
  )

  useEffect(() => {
    // If the device is up, use booted_at; otherwise use last_seen_at.
    // booted_at is resolved server-side from the device uptime, so it stays
    // accurate even on RTC-less/netbooted devices with an unsynced clock.
    //
    // "maintenance" counts as up: it means a command is running (or, until the
    // send-path fix, that one failed), and the device keeps heartbeating
    // throughout. Reading last_seen_at there made the uptime restart from zero
    // on every heartbeat and every page load — the machine looked like it had
    // just rebooted because a command had been dispatched to it.
    const deviceIsUp = UP_STATUSES.includes(status)
    const deviceIsDown = DOWN_STATUSES.includes(status)

    // Transitional: restarting, shutting_down, installing, flashing, adopting,
    // standby… The device is mid-something, so its boot time is stale (it is
    // about to change) and its last heartbeat is not an uptime. Neither number
    // is an answer, so do not invent one.
    if (!deviceIsUp && !deviceIsDown) {
      setUptime(t('shared.hooks.uptime.blank'))
      return
    }

    const timestamp = deviceIsUp ? bootedAt : lastSeenAt

    // No timestamp: a node that is down but was once seen says "never", and a
    // node that has never connected (ready_for_adoption, awaiting first boot)
    // has no uptime at all — a dash rather than a misleading "0s".
    if (!timestamp) {
      setUptime(
        deviceIsDown
          ? t('shared.hooks.uptime.never')
          : t('shared.hooks.uptime.blank'),
      )
      return
    }

    // For a device that is down, calculate once and show a static "ago".
    if (deviceIsDown) {
      const now = Math.floor(Date.now() / 1000) // Current time in seconds
      const diff = now - timestamp

      if (diff < 0) {
        // Edge case: timestamp is in the future
        setUptime(t('shared.hooks.uptime.never'))
        return
      }

      setUptime(formatAgo(diff))
      return // No interval for offline nodes
    }

    // For a device that is up, calculate uptime and update every second
    const calculateUptime = () => {
      const now = Math.floor(Date.now() / 1000) // Current time in seconds
      const diff = now - timestamp

      if (diff < 0) {
        // Edge case: timestamp is in the future
        setUptime(t('shared.hooks.uptime.seconds', { seconds: 0 }))
        return
      }

      setUptime(formatUptime(diff))
    }

    // Calculate immediately
    calculateUptime()

    // Update every second for online nodes
    const interval = setInterval(calculateUptime, 1000)

    return () => clearInterval(interval)
  }, [bootedAt, lastSeenAt, status])

  return uptime
}

/**
 * What to CALL the value `useUptime` returns for this status.
 *
 * It lives here because only this module knows which of the three answers it
 * gave. A caller that hardcodes "uptime:" prints "uptime: 5m ago" for a node
 * that is down — the value is a last-seen time and the label contradicts it.
 */
export const uptimeLabel = (status: NodeStatus | string): string =>
  DOWN_STATUSES.includes(status as NodeStatus)
    ? t('shared.hooks.uptime.labelLastSeen')
    : t('shared.hooks.uptime.labelUptime')
