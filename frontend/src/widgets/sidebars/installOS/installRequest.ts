import type { InstallOSRequest } from '@/entities/node/api/installOSCommand'

// The request the Install OS panel sends once the operator has confirmed that
// the target device is erased.
//
// wipe: the confirmation IS the consent to overwrite a used disk, and without
// it the agent refuses any disk that already has partitions — which is every
// reinstall. The agent still refuses a mounted disk.
//
// reboot: never. A freshly imaged disk has no agent or credentials yet, so
// rebooting into it would strand the device. It stays on recovery until an
// explicit reboot once it is provisioned.
export const installRequestFromPanel = (
  imageId: string,
  targetDevice: string,
): InstallOSRequest => ({
  image_id: imageId,
  target_device: targetDevice,
  reboot: false,
  wipe: true,
})
