import { SidebarAccessUserDetails } from '@/widgets/sidebars/accessUserDetails'
import { SidebarAddNode } from '@/widgets/sidebars/addNode'
import { SidebarCreateAccessUser } from '@/widgets/sidebars/createAccessUser'
import { SidebarCreateCACert } from '@/widgets/sidebars/createCACert'
import { SidebarCreateMasterToken } from '@/widgets/sidebars/createMasterToken'
import { SidebarCreateSecret } from '@/widgets/sidebars/createSecret'
import { SidebarCreateSSHKey } from '@/widgets/sidebars/createSSHKey'
import { SidebarDeletedNode } from '@/widgets/sidebars/deletedNode'
import { SidebarEditSSHKey } from '@/widgets/sidebars/editSSHKey'
import { SidebarMasterTokenDetails } from '@/widgets/sidebars/masterTokenDetails'
import { SidebarNode } from '@/widgets/sidebars/node'
import { SidebarOSImageDetails } from '@/widgets/sidebars/osImageDetails'
import { SidebarSecretDetails } from '@/widgets/sidebars/secretDetails'
import { SidebarSSHKeyDetails } from '@/widgets/sidebars/sshKeyDetails'
import { SidebarUploadOSImage } from '@/widgets/sidebars/uploadOSImage'
import { SidebarUtilityAssignment } from '@/widgets/sidebars/utilityAssignment'
import { SidebarWorkflowExecutionDetails } from '@/widgets/sidebars/workflowExecutionDetails'

import { Actions, useCan } from '@/entities/permissions/hooks'

import { exhaustiveMatchingGuard } from '@/shared/utils/exhaustiveSwitchCase.ts'

import { SidebarEnum, SidebarWidgetParams } from './types'

type SidebarRendererProps = {
  params: SidebarWidgetParams
  onClose: () => unknown
}

// The right a sidebar needs before it will render.
//
// This renderer builds purely from the URL, which means every one of these
// panels was reachable by typing its name into the address bar — most visibly
// `?sidebar=accountPolicy`, the super-admin editor for the account-wide
// provisioning defaults, which opened for anyone with a session. The buttons
// that lead here are gated on their own pages; the URL was the unguarded door.
//
// Only the WRITE panels are listed. A details panel is a read of something the
// list already showed, and its own actions gate themselves — adding an action
// here would deny the read as well.
const SIDEBAR_REQUIRED_ACTION: Partial<
  Record<SidebarEnum, (typeof Actions)[keyof typeof Actions]>
> = {
  [SidebarEnum.CreateNode]: Actions.NodeCreate,
  // A details panel, but of a record only node.delete may READ: the tombstone
  // list is served on that action, so without it the panel could only show
  // "not found" for a row the list never offered.
  [SidebarEnum.DeletedNode]: Actions.NodeDelete,
  // The same for two more reads: a secret and a device login user are served
  // only on these actions, so a typed URL would only meet a refusal.
  [SidebarEnum.SecretDetails]: Actions.SecretRead,
  [SidebarEnum.AccessUserDetails]: Actions.AccessManage,
  [SidebarEnum.UploadOSImage]: Actions.ImageManage,
  [SidebarEnum.CreateSSHKey]: Actions.SSHKeyManage,
  [SidebarEnum.EditSSHKey]: Actions.SSHKeyManage,
  [SidebarEnum.CreateMasterToken]: Actions.MasterTokenManage,
  [SidebarEnum.CreateSecret]: Actions.SecretManage,
  [SidebarEnum.CreateAccessUser]: Actions.AccessManage,
  [SidebarEnum.CreateCACert]: Actions.AccessManage,
}

export const SideBarRenderer = ({ params, onClose }: SidebarRendererProps) => {
  const can = useCan()

  if (!params.sidebar) {
    return null
  }

  // Render nothing rather than an explanation. A denied panel here is not a
  // user who tried something and deserves feedback — it is a URL that should
  // not have opened anything, and the backend refuses the write regardless.
  const required = SIDEBAR_REQUIRED_ACTION[params.sidebar]
  if (required && !can(required)) {
    return null
  }

  switch (params.sidebar) {
    case SidebarEnum.Node:
      // Keyed on the node, so switching selection REMOUNTS the panel instead of
      // handing the next node the previous one's local state (without the key, after
      // updating one node's bootloader, selecting another shows that node as
      // mid-update — closing and reopening the panel "fixes" it, which is the
      // signature of state that outlived its subject).
      //
      // Keyed here rather than on the nine sections inside: every one of them
      // takes nodeId and holds its own in-flight flags, so keying them
      // individually is nine chances to forget the tenth. The panel's identity
      // IS the node.
      return (
        <SidebarNode
          key={params.nodeId}
          nodeId={params.nodeId}
          onClose={onClose}
        />
      )
    case SidebarEnum.DeletedNode:
      return (
        <SidebarDeletedNode
          key={params.nodeId}
          nodeId={params.nodeId}
          onClose={onClose}
        />
      )
    case SidebarEnum.CreateNode:
      return <SidebarAddNode onClose={onClose} />
    case SidebarEnum.OSImageDetails:
      return (
        <SidebarOSImageDetails imageId={params.imageId} onClose={onClose} />
      )
    case SidebarEnum.UploadOSImage:
      return <SidebarUploadOSImage onClose={onClose} />
    case SidebarEnum.SSHKeyDetails:
      return <SidebarSSHKeyDetails keyId={params.keyId} onClose={onClose} />
    case SidebarEnum.CreateSSHKey:
      return <SidebarCreateSSHKey onClose={onClose} />
    case SidebarEnum.EditSSHKey:
      return <SidebarEditSSHKey keyId={params.keyId} onClose={onClose} />
    case SidebarEnum.WorkflowExecutionDetails:
      return (
        <SidebarWorkflowExecutionDetails
          executionId={params.executionId}
          onClose={onClose}
        />
      )
    case SidebarEnum.CreateMasterToken:
      return <SidebarCreateMasterToken onClose={onClose} />
    case SidebarEnum.MasterTokenDetails:
      return (
        <SidebarMasterTokenDetails tokenId={params.tokenId} onClose={onClose} />
      )
    case SidebarEnum.CreateSecret:
      return <SidebarCreateSecret onClose={onClose} />
    case SidebarEnum.SecretDetails:
      return (
        <SidebarSecretDetails secretId={params.secretId} onClose={onClose} />
      )
    case SidebarEnum.CreateAccessUser:
      return <SidebarCreateAccessUser onClose={onClose} />
    case SidebarEnum.CreateCACert:
      return <SidebarCreateCACert onClose={onClose} />
    case SidebarEnum.AccessUserDetails:
      return (
        <SidebarAccessUserDetails entryId={params.entryId} onClose={onClose} />
      )
    case SidebarEnum.UtilityAssignmentDetails:
      // A details panel like the others: reads gate on nothing (the list
      // already showed the row), its own Save/Apply actions gate themselves
      // on utility.manage.
      return (
        <SidebarUtilityAssignment
          key={params.assignmentId}
          assignmentId={params.assignmentId}
          onClose={onClose}
        />
      )
    default:
      return exhaustiveMatchingGuard(params)
  }
}
