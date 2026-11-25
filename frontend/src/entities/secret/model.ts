import { createEffect, createEvent, createStore } from 'effector'

import * as api from './api'
import type {
  Secret,
  SecretListResult,
  SecretOUCountsResult,
  SecretTagCount,
} from './api/types'

// Effects
export const fetchSecretsFx = createEffect(
  async (params?: api.SecretListParams) => {
    return await api.getSecrets(params)
  },
)

export const fetchSecretFx = createEffect(async (secretId: string) => {
  return await api.getSecret(secretId)
})

export const createSecretFx = createEffect(
  async (data: api.CreateSecretRequest) => {
    return await api.postCreateSecret(data)
  },
)

export const updateSecretFx = createEffect(
  async ({
    secretId,
    data,
  }: {
    secretId: string
    data: api.UpdateSecretRequest
  }) => {
    return await api.putUpdateSecret(secretId, data)
  },
)

export const deleteSecretFx = createEffect(async (secretId: string) => {
  await api.deleteSecret(secretId)
  return secretId
})

// Stores. The list is server-paginated: $secretsPage holds the current page
// with the filter's total; mutations refetch through the page's own params.
const emptyPage: SecretListResult = {
  secrets: [],
  count: 0,
  total: 0,
  page: 1,
  per_page: 50,
}

export const $secretsPage = createStore<SecretListResult>(emptyPage).on(
  fetchSecretsFx.doneData,
  (_, page) => page,
)

export const $secrets = $secretsPage.map((page) => page.secrets)

// $secretsLoadFailed separates "this account has no secrets" from "we were not
// allowed to ask, or could not". Both leave the page empty, and the table then
// renders "click Create secret to add your first secret" — shown to a role that
// holds no secret.read, that is an invitation to an action whose own button is
// hidden, and shown after a transient failure it reads as data loss. This is
// exactly what $nodesLoadFailed was written for on the node list; the lesson
// had not been carried over.
export const $secretsLoadFailed = createStore(false)

$secretsLoadFailed
  .on(fetchSecretsFx.fail, () => true)
  .on(fetchSecretsFx.doneData, () => false)

// Id-indexed cache fed by both the paged list and single-secret fetches, so
// detail sidebars work for secrets outside the current page.
export const $secretsById = createStore<Record<string, Secret>>({})
  .on(fetchSecretsFx.doneData, (byId, page) => {
    const next = { ...byId }
    for (const secret of page.secrets) next[secret.id] = secret
    return next
  })
  .on([fetchSecretFx.doneData, createSecretFx.doneData], (byId, secret) => ({
    ...byId,
    [secret.id]: secret,
  }))
  .on(updateSecretFx.doneData, (byId, secret) => ({
    ...byId,
    [secret.id]: secret,
  }))
  .on(deleteSecretFx.doneData, (byId, deletedId) => {
    if (!(deletedId in byId)) return byId
    const next = { ...byId }
    delete next[deletedId]
    return next
  })

export const $secretsLoading = fetchSecretsFx.pending

// Fired by widgets after attach/detach operations that bypass the effects
// above (drag-and-drop, pickers) so list pages can refetch.
export const attachmentsChanged = createEvent()

// Facets for the filter rail.
export const fetchSecretTagsFx = createEffect(async () => {
  const result = await api.getSecretTags()
  return result.tags
})

export const $secretTags = createStore<SecretTagCount[]>([]).on(
  fetchSecretTagsFx.doneData,
  (_, tags) => tags,
)

export const fetchSecretOUCountsFx = createEffect(async () => {
  return await api.getSecretOUCounts()
})

export const $secretOUCounts = createStore<SecretOUCountsResult>({
  ous: [],
  unattached: 0,
  total: 0,
}).on(fetchSecretOUCountsFx.doneData, (_, counts) => counts)
