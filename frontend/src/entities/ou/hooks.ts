import { useStoreMap, useUnit } from 'effector-react'

import { $ous, $ousById, $ousLoading } from './model'

export const useOUs = () => {
  const ous = useUnit($ous)
  const loading = useUnit($ousLoading)
  return { ous, loading }
}

export const useOU = ({ ouId }: { ouId: string }) => {
  const ou = useStoreMap({
    store: $ousById,
    keys: [ouId],
    fn: (byId, [id]) => byId[id] ?? null,
  })
  return { ou }
}
