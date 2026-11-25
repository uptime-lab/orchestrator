import { postNodeCommand, type CommandAccepted } from './postNodeCommand.ts'

// Changes the hostname of a managed (disk-booted) node. The agent applies it
// via hostnamectl over the live OS. Not meaningful on the netboot bootstrap.
// The command is queueable, so the answer says whether it was queued.
export const setHostname = async (
  nodeId: string,
  hostname: string,
): Promise<CommandAccepted> =>
  postNodeCommand(nodeId, {
    command: 'set_hostname',
    params: { hostname },
  })
