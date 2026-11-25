import { createEffect, createStore } from 'effector'

import * as api from './api'
import type { MasterTokenResult } from './api/types'

// Effects
export const fetchMasterTokensFx = createEffect(async () => {
  const result = await api.getMasterTokens()
  return result.tokens
})

export const createMasterTokenFx = createEffect(
  async (data: api.CreateMasterTokenRequest) => {
    return await api.postCreateMasterToken(data)
  },
)

export const updateMasterTokenFx = createEffect(
  async ({
    tokenId,
    data,
  }: {
    tokenId: string
    data: api.UpdateMasterTokenRequest
  }) => {
    return await api.putUpdateMasterToken(tokenId, data)
  },
)

export const deleteMasterTokenFx = createEffect(async (tokenId: string) => {
  await api.deleteMasterToken(tokenId)
  return tokenId
})

// Stores
export const $masterTokens = createStore<MasterTokenResult[]>([])
  .on(fetchMasterTokensFx.doneData, (_, tokens) => tokens)
  .on(createMasterTokenFx.doneData, (tokens, newToken) => [
    { ...newToken, token: undefined }, // Don't store the plain token
    ...tokens,
  ])
  .on(updateMasterTokenFx.doneData, (tokens, updatedToken) =>
    tokens.map((t) => (t.id === updatedToken.id ? updatedToken : t)),
  )
  .on(deleteMasterTokenFx.doneData, (tokens, deletedId) =>
    tokens.filter((t) => t.id !== deletedId),
  )

export const $masterTokensById = $masterTokens.map((tokens) =>
  tokens.reduce(
    (acc, token) => {
      acc[token.id] = token
      return acc
    },
    {} as Record<string, MasterTokenResult>,
  ),
)

export const $masterTokensLoading = fetchMasterTokensFx.pending
