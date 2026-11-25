import clsx from 'clsx'
import { createLocator, Locator, Mark } from 'create-locator'
import { useCallback } from 'react'

import { useSSHKey } from '@/entities/sshKey/hooks'

// dataTransfer key for dragging a key onto the OU filter tree.
export const SSHKEY_DND_TYPE = 'text/sshkey-id'

export type KeyCardLocator = Locator<{
  alias: { alias: string }
  fingerprint: { fingerprint: string }
}>

type KeyCardProps = {
  id: string
  selected: boolean
  onClick?: (id: string) => unknown
} & Mark<KeyCardLocator>

export const KeyCard = ({ id, selected, onClick, ...rest }: KeyCardProps) => {
  const { key } = useSSHKey({ keyId: id })
  const locator = createLocator(rest)

  const handleClick = useCallback(() => {
    onClick?.(id)
  }, [onClick, id])

  if (!key) {
    return null
  }

  return (
    <div
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData(SSHKEY_DND_TYPE, id)
        e.dataTransfer.effectAllowed = 'move'
      }}
      className={clsx(
        'flex items-center gap-4 pl-0 pr-4 py-3',
        'select-none bg-neutral-14 hover:bg-neutral-13 rounded',
        'transition-colors cursor-grab active:cursor-grabbing',
        selected && 'bg-[#3C4B12]',
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
              d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z"
            />
          </svg>
        </div>
        <div className="flex-1 flex items-center gap-6 min-w-0">
          <div className="flex items-center gap-2 min-w-0">
            <div
              className="text-sm font-medium text-neutral-1 truncate"
              {...locator.alias({ alias: key.alias })}
            >
              {key.alias}
            </div>
            {/* No DEFAULT badge: the "default key" was the single-key-per-OU
                model. Keys now attach to named users, so nothing is a default. */}
            <span className="text-xs bg-purple-5/20 text-purple-4 px-2 py-0.5 rounded">
              {key.key_type === 'ca' ? 'CA' : 'SSH'}
            </span>
          </div>
          <div
            className="text-sm text-neutral-4 font-mono truncate"
            {...locator.fingerprint({ fingerprint: key.fingerprint })}
          >
            {key.fingerprint}
          </div>
          {key.tags.length > 0 && (
            <div className="flex flex-wrap gap-1 flex-shrink-0">
              {key.tags.map((tag) => (
                <span
                  key={tag}
                  className="text-xxs font-mono px-1.5 py-px rounded bg-neutral-12 text-neutral-4"
                >
                  #{tag}
                </span>
              ))}
            </div>
          )}
          <div className="text-sm text-neutral-5 font-mono truncate flex-shrink-0 hidden md:block">
            Created {new Date(key.created_at).toLocaleDateString()}
          </div>
        </div>
      </button>
    </div>
  )
}
