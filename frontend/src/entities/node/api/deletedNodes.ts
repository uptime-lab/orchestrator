import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

/**
 * A node that was deleted and whose device may still be out there.
 *
 * Deleting a node removes the row and tells the DEVICE nothing: it keeps
 * retrying with an identity the platform no longer knows, and a network-first
 * one cannot even reach recovery. These records are the route back.
 *
 * `adoption_method` is what decides whether a restore actually brings the device
 * back, so it drives the wording rather than being decoration:
 *  - `eeprom` — the identity is baked into the device's EEPROM; a restore is
 *    enough and it rejoins on its own.
 *  - `file`   — the identity is a file on the device's own disk; a restore works
 *    while that file is still there (a reinstall since the delete removed it).
 *  - `unknown` — the node never reported hardware, so nothing can be promised.
 */
export interface DeletedNode {
  id: string
  name: string
  hostname: string
  hardware_serial: string
  ou_id?: string
  adoption_method: 'eeprom' | 'file' | 'unknown'
  /**
   * The platform itself flashed THIS node's identity into the EEPROM. Without
   * it, `eeprom` says only that the EEPROM points at a platform — a device
   * baked for a previous node and re-adopted by file reports the same — so
   * "it rejoins on its own" may be promised only when this is true. Optional:
   * an older backend omits it, and absent must read as not confirmed.
   */
  eeprom_bake_confirmed?: boolean
  /**
   * Whether the device's last reported boot order reaches HTTP boot before
   * any local disk. Absent when it never reported one. A netboot-first device
   * presents its EEPROM identity on every boot; a disk-first one boots
   * whatever its installed OS holds.
   */
  boot_order_netboots_first?: boolean
  /**
   * The platform asked this device to delete its own adoption file as it was
   * deleted (a forced delete of a connected node). REQUESTED, never confirmed —
   * the command's terminal status arrives after the node row is gone. It is what
   * keeps the restore offer honest: on a `file` node that took the release, a
   * restore returns the row and not the device.
   */
  identity_release_requested?: boolean
  /**
   * When the device REPORTED erasing its adoption file. Reported, not
   * verified. Absent until the report arrives.
   */
  identity_released_at?: string
  /**
   * When a device last presented this node's id and matching token and was
   * refused: the device is out there and still trying. REFUSED, never online.
   */
  last_refused_at?: string
  /**
   * This record's hardware serial is now carried by a node in ANOTHER account,
   * so the restore refuses it (409 `serial_bound_elsewhere`) and the device is
   * not reachable through this record.
   *
   * It reports a SERIAL binding, never a device movement: a serial can be bound
   * by an account that never held the hardware, so no copy may claim the device
   * moved. Optional because an older backend omits it, and `undefined` must read
   * as "not known to be bound" rather than as either answer.
   */
  serial_bound_elsewhere?: boolean
  /**
   * The live node of THIS account that now carries this record's hardware
   * serial (the board was adopted again as a new node). The restore refuses it
   * (409 `serial_bound_in_account`) and `restorable` is false; the remedy is to
   * delete that node first. Null or absent when there is none.
   */
  serial_bound_in_account?: { node_id: string; name: string } | null
  deleted_at: string
  deleted_by: string
  restored_at?: string
  restorable: boolean
}

interface DeletedNodesResponse {
  deleted_nodes: DeletedNode[] | null
}

export const getDeletedNodes = async (): Promise<DeletedNode[]> => {
  // Two levels, not one. Every handler answers through the standard
  // `{success, message, data, timestamp}` envelope, so the payload is at
  // `data.data`. Reading `data.deleted_nodes` off the envelope yielded
  // `undefined`, the `?? []` below turned that into an empty list, and the panel
  // renders nothing when the list is empty — so the only route back from a
  // deleted node was invisible in the UI, silently. Compare `getNodes`, which unwraps twice and works.
  const { data } = await apiClient.get<ApiResponse<DeletedNodesResponse>>(
    '/api/v1/nodes/deleted',
  )
  // An account that has never deleted a node is the normal case, and Go renders
  // an empty slice as null often enough that this must not be assumed away.
  return data.data.deleted_nodes ?? []
}

export const restoreDeletedNode = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/deleted/${nodeId}/restore`)
}

export const forgetDeletedNode = async (nodeId: string): Promise<void> => {
  await apiClient.delete(`/api/v1/nodes/deleted/${nodeId}`)
}
