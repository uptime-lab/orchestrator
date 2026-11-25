// The Apply button's state in the overclock sub-view. "Nothing chosen yet" is
// its own state: with no preset selected and nothing applied, both are null,
// and comparing them read as "already sent".
export type OverclockButtonState =
  'choose' | 'sending' | 'sent' | 'apply' | 'nodeBusy'

export const overclockButtonState = ({
  selected,
  applied,
  sending,
  nodeBusy,
}: {
  selected: string | null
  applied: string | null
  sending: boolean
  // A command is running on the node (its status is a transient one such as
  // maintenance): nothing new is sent until it settles.
  nodeBusy: boolean
}): OverclockButtonState => {
  if (sending) return 'sending'
  if (selected === null) return 'choose'
  if (applied === selected) return 'sent'
  if (nodeBusy) return 'nodeBusy'
  return 'apply'
}

// Node statuses during which a command is in flight or the device is in
// transition, so no new configuration is sent from this panel.
const BUSY_NODE_STATUSES = new Set([
  'maintenance',
  'flashing',
  'restarting',
  'adopting',
])

export const nodeIsBusy = (status: string | undefined): boolean =>
  !!status && BUSY_NODE_STATUSES.has(status)
