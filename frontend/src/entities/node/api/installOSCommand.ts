export type InstallOSRequest = {
  image_id: string
  target_device: string
  reboot?: boolean
  // Lets the agent write over a disk that already carries partitions. A
  // mounted disk is refused whatever this says.
  wipe?: boolean
}

export const installOSCommandBody = (request: InstallOSRequest) => ({
  command: 'install_os',
  params: {
    image_id: request.image_id,
    target_device: request.target_device,
    reboot: request.reboot,
    wipe: request.wipe,
  },
})
