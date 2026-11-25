export enum SidebarEnum {
  Node = 'node',
  // A deleted node's tombstone: what it was, what a restore will achieve, and
  // the restore / forget actions. nodeId is the DELETED node's id (a restore
  // reuses it).
  DeletedNode = 'deletedNode',
  CreateNode = 'createNode',
  OSImageDetails = 'osImageDetails',
  UploadOSImage = 'uploadOSImage',
  SSHKeyDetails = 'sshKeyDetails',
  CreateSSHKey = 'createSSHKey',
  EditSSHKey = 'editSSHKey',
  WorkflowExecutionDetails = 'workflowExecutionDetails',
  // Master token sidebars
  CreateMasterToken = 'createMasterToken',
  MasterTokenDetails = 'masterTokenDetails',
  // Organizational unit sidebars
  // Secret sidebars
  CreateSecret = 'createSecret',
  SecretDetails = 'secretDetails',
  // Access (credentials) sidebars
  CreateAccessUser = 'createAccessUser',
  AccessUserDetails = 'accessUserDetails',
  CreateCACert = 'createCACert',
  // Utility assignment config sidebar
  UtilityAssignmentDetails = 'utilityAssignmentDetails',
}

export type SidebarWidgetParams =
  | {
      sidebar: SidebarEnum.Node
      nodeId: string
    }
  | {
      sidebar: SidebarEnum.DeletedNode
      nodeId: string
    }
  | {
      sidebar: SidebarEnum.OSImageDetails
      imageId: string
    }
  | {
      sidebar: SidebarEnum.SSHKeyDetails
      keyId: string
    }
  | {
      sidebar: SidebarEnum.EditSSHKey
      keyId: string
    }
  | {
      sidebar: SidebarEnum.WorkflowExecutionDetails
      executionId: string
    }
  | {
      sidebar: SidebarEnum.MasterTokenDetails
      tokenId: string
    }
  | {
      sidebar: SidebarEnum.SecretDetails
      secretId: string
    }
  | {
      sidebar: SidebarEnum.AccessUserDetails
      entryId: string
    }
  | {
      sidebar: SidebarEnum.UtilityAssignmentDetails
      assignmentId: string
    }
  | {
      sidebar: Exclude<
        SidebarEnum,
        | SidebarEnum.Node
        | SidebarEnum.DeletedNode
        | SidebarEnum.OSImageDetails
        | SidebarEnum.SSHKeyDetails
        | SidebarEnum.EditSSHKey
        | SidebarEnum.WorkflowExecutionDetails
        | SidebarEnum.MasterTokenDetails
        | SidebarEnum.SecretDetails
        | SidebarEnum.AccessUserDetails
        | SidebarEnum.UtilityAssignmentDetails
      >
    }
  | { sidebar: undefined }

export type SidebarProps = {
  onClose: () => unknown
}

const sideBarKeysSet = new Set(Object.values(SidebarEnum))
export const isSidebar = (value: unknown): value is SidebarEnum => {
  return (
    !!value &&
    typeof value === 'string' &&
    sideBarKeysSet.has(value as SidebarEnum)
  )
}
