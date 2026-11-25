import { apiClient } from '@/shared/utils/apiConfig'

// Generates a signed per-device EEPROM package (always from the signer's
// latest firmware base) and commands the agent to flash it. bootOrder
// overrides the baked BOOT_ORDER; omitted = network-first default.
export const provisionEEPROM = async (
  nodeId: string,
  bootOrder?: string[],
): Promise<void> => {
  await apiClient.post(
    `/api/v1/nodes/${nodeId}/provision-eeprom`,
    bootOrder && bootOrder.length > 0 ? { boot_order: bootOrder } : {},
  )
}
