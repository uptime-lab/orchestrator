// Action constants mirrored from the backend authz enum. Kept as string
// literals so the frontend never re-implements the role→action matrix (the
// server computes the map); these are only the action names to look up.
//
// They live in their own module, apart from the hooks, for the reason can.ts
// states about the predicates: a module that only needs the NAME of an action
// should not pull the effector store graph — and everything it touches at
// import time — along with it. hooks.ts re-exports them, so every existing
// `import { Actions } from '@/entities/permissions/hooks'` keeps working.
export const Actions = {
  NodeReboot: 'node.reboot',
  NodeStatusCheck: 'node.status_check',
  NodeSetHostname: 'node.set_hostname',
  NodeShutdown: 'node.shutdown',
  NodeUpdate: 'node.update',
  NodeInstallOS: 'node.install_os',
  NodeDelete: 'node.delete',
  NodeApprove: 'node.approve',
  NodeEEPROMFlash: 'node.eeprom_flash',
  NodeRebootRecovery: 'node.reboot_recovery',
  NodeFileWrite: 'node.file_write',
  NodeReplace: 'node.replace',
  NodeUpdateAgent: 'node.update_agent',
  NodeAttributes: 'node.attributes_write',
  NodeAssignOU: 'node.assign_ou',
  NodeCredentials: 'node.credentials_read',
  NodeTerminal: 'node.terminal',
  SecretRead: 'secret.read',
  SecretManage: 'secret.manage',
  ImageManage: 'image.manage',
  SSHKeyManage: 'sshkey.manage',
  AccessManage: 'access.manage',
  MasterTokenManage: 'master_token.manage',
  ProvisioningSD: 'provisioning.sd',
  AccountPolicy: 'account_policy.manage',
  AccountUpdate: 'account.update',
  AccountDelete: 'account.delete',
  // Whole-account data export. Super_admin only by construction: no lower role
  // lists it, so only super_admin's blanket grant satisfies it.
  AccountExport: 'account.export',
  NodeRead: 'node.read',
  OURead: 'ou.read',
  SSHKeyRead: 'sshkey.read',
  PlaybookRead: 'playbook.read',
  OUManage: 'ou.manage',
  PlaybookRun: 'playbook.run',
  PlaybookRunPrivileged: 'playbook.run_privileged',
  PlaybookAuthor: 'playbook.author',
  MemberManage: 'member.manage',
  // Held by no role below super_admin: the trail names every member who touched
  // anything privileged, which is oversight material rather than a per-OU read.
  AuditRead: 'audit.read',
  // Held by no role below super_admin: the device entitlement is a property
  // of the install, and applying a file changes what every tenant may admit.
  EntitlementManage: 'entitlement.manage',
  // Gates four surfaces that once shipped ungated for want of it: the
  // landing page's primary "Add Node" button, the SSH-adopt and SD-provisioning
  // entries beside it, and the privileged-playbook author gate.
  NodeCreate: 'node.create',
  NodeSSHAdopt: 'node.ssh_adopt',
  PlaybookAuthorPrivileged: 'playbook.author_privileged',
  UtilityRead: 'utility.read',
  UtilityManage: 'utility.manage',
} as const
