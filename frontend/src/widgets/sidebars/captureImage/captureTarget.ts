// Which disk Create backup captures.
//
// The disk always travels with the command: left out, the agent falls back
// to a fixed path, which on a node that runs from SD or USB is a different
// disk or none at all.

// The disk to preselect: the one the node reports running from, when it is
// among the disks it lists. Otherwise nothing, and the operator chooses —
// in recovery the node runs from RAM and cannot say which disk holds its OS.
export const defaultCaptureTarget = (
  devices: Record<string, unknown> | undefined,
  rootDevice?: string,
): string => (rootDevice && devices && rootDevice in devices ? rootDevice : '')

// The capture_image params, or null while no disk is chosen.
export const buildCaptureParams = (
  title: string,
  generalize: boolean,
  targetDevice: string,
): Record<string, unknown> | null =>
  targetDevice
    ? { title: title.trim(), generalize, target_device: targetDevice }
    : null
