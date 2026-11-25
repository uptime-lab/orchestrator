import { useStoreMap, useUnit } from 'effector-react'

import {
  $secretOUCounts,
  $secrets,
  $secretsById,
  $secretsLoadFailed,
  $secretsLoading,
  $secretsPage,
  $secretTags,
  fetchSecretFx,
  fetchSecretOUCountsFx,
  fetchSecretsFx,
  fetchSecretTagsFx,
} from './model'

export const useSecrets = () => {
  const secrets = useUnit($secrets)
  const page = useUnit($secretsPage)
  const loading = useUnit($secretsLoading)
  const loadFailed = useUnit($secretsLoadFailed)
  const fetchSecrets = useUnit(fetchSecretsFx)
  return { secrets, total: page.total, loading, loadFailed, fetchSecrets }
}

export const useSecret = ({ secretId }: { secretId: string }) => {
  const secret = useStoreMap({
    store: $secretsById,
    keys: [secretId],
    fn: (byId, [id]) => byId[id] ?? null,
  })
  const fetchSecret = useUnit(fetchSecretFx)
  return { secret, fetchSecret }
}

export const useSecretTags = () => {
  const tags = useUnit($secretTags)
  const fetchTags = useUnit(fetchSecretTagsFx)
  return { tags, fetchTags }
}

export const useSecretOUCounts = () => {
  const ouCounts = useUnit($secretOUCounts)
  const fetchOUCounts = useUnit(fetchSecretOUCountsFx)
  return { ouCounts, fetchOUCounts }
}
