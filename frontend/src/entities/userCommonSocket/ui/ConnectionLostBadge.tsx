import clsx from 'clsx'
import { t } from 'i18next'
import { useEffect, useState, type FC } from 'react'

import { useUserSocket } from '@/entities/userCommonSocket/model.ts'

import { getConfigValue } from '@/shared/utils/config'

/**
 * How long the socket must stay closed before the badge appears.
 *
 * This is almost certainly why the component was `return null` before its own
 * logic: the reconnect backoff cycles the status through `closed` on every
 * attempt, so a badge bound directly to `status === 'closed'` flashes during
 * ordinary reconnects — and a warning that appears for a second while nothing is
 * wrong is a warning nobody reads. The first backoff delay is 1s, so five seconds
 * clears a few normal retries while still surfacing a real outage promptly.
 */
const DOWN_GRACE_MS = 5_000

/**
 * ConnectionLostBadge is the app's only "live updates are down" indicator, and it
 * was dead code — `return null` sat above all of it.
 *
 * It matters because the whole authenticated UI is push-driven: node status,
 * command progress and workflow runs arrive over this socket. With it dead the
 * page keeps rendering the last thing it knew, which in this product means an
 * operator can watch a node that is no longer online, or miss a command that
 * failed, with nothing on screen saying the view is stale.
 */
export const ConnectionLostBadge: FC = () => {
  const { status } = useUserSocket()
  const [downLongEnough, setDownLongEnough] = useState(false)

  useEffect(() => {
    if (status !== 'closed') {
      setDownLongEnough(false)
      return
    }
    const timer = setTimeout(() => setDownLongEnough(true), DOWN_GRACE_MS)
    return () => clearTimeout(timer)
  }, [status])

  // Essentials mode runs against mocks with no socket at all, so the badge would
  // otherwise be permanently on.
  if (getConfigValue('VITE_ESSENTIALS_API_ONLY') === 'on') {
    return null
  }

  return (
    <div
      className={clsx(
        'overflow-hidden transition-all duration-300 ease-out',
        downLongEnough ? 'h-8' : 'h-0',
      )}
    >
      <div className="w-screen flex-none truncate bg-red-950 px-2 py-2 text-center text-xs text-white">
        <span className="font-bold">
          {t('entities.userCommonWebSocket.connectionLost.title')}
        </span>{' '}
        {t('entities.userCommonWebSocket.connectionLost.subTitle')}
      </div>
    </div>
  )
}
