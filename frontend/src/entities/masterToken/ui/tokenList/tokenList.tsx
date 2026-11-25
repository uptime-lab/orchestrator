import { createLocator, Locator, Mark } from 'create-locator'
import { useTranslation } from 'react-i18next'

import { useMasterTokens } from '@/entities/masterToken/hooks'

import { TokenCard, type TokenCardLocator } from './tokenCard'

export type TokenListLocator = Locator<{
  card: TokenCardLocator
}>

export type TokenListProps = {
  selectedTokenId?: string
  onTokenClick?: (id: string) => unknown
  // When set, only these tokens are rendered (rail OU/tag filtering).
  visibleIds?: string[]
} & Partial<Mark<TokenListLocator>>

export const TokenList = ({
  selectedTokenId,
  onTokenClick,
  visibleIds,
  ...rest
}: TokenListProps) => {
  const { t } = useTranslation()
  const { tokens: allTokens } = useMasterTokens()
  const tokens = visibleIds
    ? allTokens.filter((token) => visibleIds.includes(token.id))
    : allTokens
  const locator = createLocator(rest as Mark<TokenListLocator>)

  if (tokens.length === 0) {
    return (
      <div className="text-center text-neutral-4 py-12">
        {t('entities.masterToken.tokenList.empty')}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-2" {...locator()}>
      {tokens.map((token) => (
        <TokenCard
          key={token.id}
          id={token.id}
          selected={selectedTokenId === token.id}
          onClick={onTokenClick}
          {...locator.card()}
        />
      ))}
    </div>
  )
}
