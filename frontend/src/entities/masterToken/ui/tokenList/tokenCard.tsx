import clsx from 'clsx'
import { createLocator, Locator, Mark } from 'create-locator'
import { useCallback } from 'react'
import { useTranslation } from 'react-i18next'

import { useMasterToken } from '@/entities/masterToken/hooks'
import { useOU } from '@/entities/ou'

// dataTransfer key for dragging a token onto the OU filter tree.
export const TOKEN_DND_TYPE = 'text/token-id'

export type TokenCardLocator = Locator<{
  name: { name: string }
  status: { status: string }
}>

type TokenCardProps = {
  id: string
  selected: boolean
  onClick?: (id: string) => unknown
} & Mark<TokenCardLocator>

export const TokenCard = ({
  id,
  selected,
  onClick,
  ...rest
}: TokenCardProps) => {
  const { t } = useTranslation()
  const { token } = useMasterToken({ tokenId: id })
  const { ou: adoptionOU } = useOU({ ouId: token?.ou_id ?? '' })
  const locator = createLocator(rest)

  const handleClick = useCallback(() => {
    onClick?.(id)
  }, [onClick, id])

  if (!token) {
    return null
  }

  // created_at is an RFC3339 string;
  // expires_at stays epoch seconds.
  const formatDate = (timestamp: number | string) => {
    const d =
      typeof timestamp === 'number'
        ? new Date(timestamp * 1000)
        : new Date(timestamp)
    return d.toLocaleDateString()
  }

  const isExpired =
    token.expires_at !== undefined &&
    token.expires_at > 0 &&
    token.expires_at * 1000 < Date.now()

  return (
    <div
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData(TOKEN_DND_TYPE, id)
        e.dataTransfer.effectAllowed = 'move'
      }}
      className={clsx(
        'flex items-center gap-4 pl-0 pr-4 py-3',
        'select-none bg-neutral-14 hover:bg-neutral-13 rounded',
        'transition-colors cursor-grab active:cursor-grabbing',
        selected && 'bg-[#3C4B12]',
        (!token.is_active || isExpired) && 'opacity-60',
      )}
      {...locator()}
    >
      <button
        type="button"
        className="flex items-center gap-4 flex-1 min-w-0"
        onClick={handleClick}
      >
        <div className="w-10 h-10 rounded bg-white flex items-center justify-center flex-shrink-0">
          <svg
            className="w-6 h-6 text-neutral-8"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"
            />
          </svg>
        </div>
        <div className="flex-1 flex items-center gap-6 min-w-0">
          <div className="flex items-center gap-2 min-w-0">
            <div
              className="text-sm font-medium text-neutral-1 truncate"
              {...locator.name({ name: token.name })}
            >
              {token.name}
            </div>
            <span
              className={clsx(
                'text-xs px-2 py-0.5 rounded',
                isExpired
                  ? 'bg-red-6/20 text-red-5'
                  : token.is_active
                    ? 'bg-lime-6 text-neutral-14'
                    : 'bg-neutral-8 text-neutral-2',
              )}
              {...locator.status({
                status: isExpired
                  ? 'expired'
                  : token.is_active
                    ? 'active'
                    : 'inactive',
              })}
            >
              {isExpired
                ? t('entities.masterToken.tokenCard.statusExpired')
                : token.is_active
                  ? t('entities.masterToken.tokenCard.statusActive')
                  : t('entities.masterToken.tokenCard.statusInactive')}
            </span>
          </div>
          <div className="text-sm text-neutral-4 font-mono truncate">
            {t('entities.masterToken.tokenCard.created', {
              date: formatDate(token.created_at),
            })}
            {' · '}
            {token.expires_at && token.expires_at > 0
              ? t('entities.masterToken.tokenCard.expires', {
                  date: formatDate(token.expires_at),
                })
              : t('entities.masterToken.tokenCard.neverExpires')}
            {' · '}
            {token.last_used_at && token.last_used_at > 0
              ? t('entities.masterToken.tokenCard.lastUsed', {
                  date: formatDate(token.last_used_at),
                })
              : t('entities.masterToken.tokenCard.neverUsed')}
          </div>
          {token.ou_id && (
            <span className="text-xs px-2 py-0.5 rounded bg-neutral-12 text-neutral-3 font-mono flex-shrink-0">
              {t('entities.masterToken.tokenCard.ouBadge', {
                name: adoptionOU?.name ?? '…',
              })}
            </span>
          )}
          {token.tags.length > 0 && (
            <div className="flex flex-wrap gap-1 flex-shrink-0">
              {token.tags.map((tag) => (
                <span
                  key={tag}
                  className="text-xxs font-mono px-1.5 py-px rounded bg-neutral-12 text-neutral-4"
                >
                  #{tag}
                </span>
              ))}
            </div>
          )}
        </div>
      </button>
    </div>
  )
}
