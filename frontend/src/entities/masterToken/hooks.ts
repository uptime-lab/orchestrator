import { useStoreMap, useUnit } from 'effector-react'
import { useEffect } from 'react'

import {
  $masterTokens,
  $masterTokensById,
  $masterTokensLoading,
  fetchMasterTokensFx,
} from './model'

export const useMasterTokens = () => {
  const tokens = useUnit($masterTokens)
  const loading = useUnit($masterTokensLoading)
  return { tokens, loading }
}

export const useMasterToken = ({ tokenId }: { tokenId: string }) => {
  const token = useStoreMap({
    store: $masterTokensById,
    keys: [tokenId],
    fn: (byId, [id]) => byId[id] ?? null,
  })
  return { token }
}

// useLoadMasterTokens fetches the token list when the viewer may read it. The
// list route needs the same right as managing tokens, so a member without it
// is not sent a request that can only answer 403. Re-runs when the
// permission map arrives after the page mounted.
export const useLoadMasterTokens = (allowed: boolean) => {
  useEffect(() => {
    if (allowed) fetchMasterTokensFx().catch(() => {})
  }, [allowed])
}
