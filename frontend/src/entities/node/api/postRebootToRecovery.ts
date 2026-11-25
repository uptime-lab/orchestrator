import { apiClient } from '@/shared/utils/apiConfig'

// One-shot netboot: the backend forces the boot target to the network
// bootstrap, reboots the node and restores the previous target once the
// node reconnects netbooted.
export const postRebootToRecovery = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/reboot-to-recovery`)
}
