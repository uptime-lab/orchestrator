import { createEffect, createStore } from 'effector'

import { SSHKeyResult } from '@/entities/sshKey/api/types.ts'

import { deriveById } from '@/shared/utils/mergeUniqueEntities'

import { apiSSHKeys } from './api'

// ============= Store =============

export const $sshKeys = createStore<SSHKeyResult[]>([])
export const $sshKeysById = $sshKeys.map(deriveById)

// ============= Effects =============

export const fetchSSHKeysFx = createEffect(() => apiSSHKeys.getSSHKeys())
export const fetchSSHKeyFx = createEffect((id: string) =>
  apiSSHKeys.getSSHKey(id),
)
export const createSSHKeyFx = createEffect(
  (data: Parameters<typeof apiSSHKeys.postCreateSSHKey>[0]) =>
    apiSSHKeys.postCreateSSHKey(data),
)
export const updateSSHKeyFx = createEffect(
  (params: {
    id: string
    data: Parameters<typeof apiSSHKeys.putUpdateSSHKey>[1]
  }) => apiSSHKeys.putUpdateSSHKey(params.id, params.data),
)
export const deleteSSHKeyFx = createEffect((id: string) =>
  apiSSHKeys.deleteSSHKey(id),
)

// ============= Side effects =============

$sshKeys
  .on(fetchSSHKeysFx.doneData, (_, keys) => [...keys])
  .on(fetchSSHKeysFx.fail, (keys) => {
    // On failure, keep existing keys (fail safely)
    return keys
  })
  .on(fetchSSHKeyFx.doneData, (keys, newKey) => {
    if (!newKey) {
      // If key is null (not found), don't update
      return keys
    }
    const existingIndex = keys.findIndex((key) => key.id === newKey.id)
    if (existingIndex >= 0) {
      const updated = [...keys]
      updated[existingIndex] = newKey
      return updated
    }
    return [...keys, newKey]
  })
  .on(fetchSSHKeyFx.fail, (keys) => {
    // On failure, keep existing keys (fail safely)
    return keys
  })
  .on(createSSHKeyFx.doneData, (keys, newKey) => [...keys, newKey])
  .on(updateSSHKeyFx.doneData, (keys, updatedKey) => {
    return keys.map((key) => (key.id === updatedKey.id ? updatedKey : key))
  })
  .on(deleteSSHKeyFx.done, (keys, { params: id }) => {
    return keys.filter((key) => key.id !== id)
  })

// Export as sshKeysModel for consistency
export const sshKeysModel = {
  fetchSSHKeysFx,
  fetchSSHKeyFx,
  createSSHKeyFx,
  updateSSHKeyFx,
  deleteSSHKeyFx,
}
