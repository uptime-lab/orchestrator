import { createLocator, Locator, Mark } from 'create-locator'
import { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { useSSHKeys } from '@/entities/sshKey/hooks'

import { KeyCard, type KeyCardLocator } from './keyCard'

export type KeyListLocator = Locator<{
  card: KeyCardLocator
}>

export type KeyListProps = {
  selectedKeyId?: string
  onKeyClick?: (id: string) => unknown
  // When set, only these keys are rendered (rail OU/tag filtering).
  visibleIds?: string[]
  // Page-specific empty state (the caller knows its own "Add" button label);
  // falls back to a generic translated message.
  emptyState?: ReactNode
} & Partial<Mark<KeyListLocator>>

export const KeyList = ({
  selectedKeyId,
  onKeyClick,
  visibleIds,
  emptyState,
  ...rest
}: KeyListProps) => {
  const { t } = useTranslation()
  const { keys: allKeys } = useSSHKeys()
  const keys = visibleIds
    ? allKeys.filter((key) => visibleIds.includes(key.id))
    : allKeys
  const locator = createLocator(rest as Mark<KeyListLocator>)

  if (keys.length === 0) {
    return (
      <div className="text-center text-neutral-4 py-12">
        {emptyState ?? t('entities.sshKey.list.empty')}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-2" {...locator()}>
      {keys.map((key) => (
        <KeyCard
          key={key.id}
          id={key.id}
          selected={selectedKeyId === key.id}
          onClick={onKeyClick}
          {...locator.card()}
        />
      ))}
    </div>
  )
}
