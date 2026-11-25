import { createEffect, createStore } from 'effector'

import * as api from './api'
import type { OU } from './api/types'

// Effects
export const fetchOUsFx = createEffect(async () => {
  const result = await api.getOUs()
  return result.units
})

export const createOUFx = createEffect(async (data: api.CreateOURequest) => {
  return await api.postCreateOU(data)
})

export const updateOUFx = createEffect(
  async ({ ouId, data }: { ouId: string; data: api.UpdateOURequest }) => {
    return await api.putUpdateOU(ouId, data)
  },
)

export const deleteOUFx = createEffect(async (ouId: string) => {
  await api.deleteOU(ouId)
  return ouId
})

// Stores
export const $ous = createStore<OU[]>([])
  .on(fetchOUsFx.doneData, (_, units) => units)
  .on(createOUFx.doneData, (units, newUnit) => [newUnit, ...units])
  .on(updateOUFx.doneData, (units, updatedUnit) =>
    units.map((u) => (u.id === updatedUnit.id ? updatedUnit : u)),
  )
  .on(deleteOUFx.doneData, (units, deletedId) =>
    units.filter((u) => u.id !== deletedId),
  )

export const $ousById = $ous.map((units) =>
  units.reduce(
    (acc, unit) => {
      acc[unit.id] = unit
      return acc
    },
    {} as Record<string, OU>,
  ),
)

export const $ousLoading = fetchOUsFx.pending
