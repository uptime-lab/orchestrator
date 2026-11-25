import { useStoreMap, useUnit } from 'effector-react'

import { $sshKeys, $sshKeysById } from './model'

export const useSSHKeys = () => {
  const keys = useUnit($sshKeys)
  return { keys }
}

export const useSSHKey = ({ keyId }: { keyId: string }) => {
  const key = useStoreMap({
    store: $sshKeysById,
    keys: [keyId],
    fn: (keysById, [id]) => keysById[id] ?? null,
  })
  return { key }
}
