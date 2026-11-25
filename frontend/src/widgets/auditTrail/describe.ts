import { t } from 'i18next'

import type { AuditEvent } from '@/entities/auditEvent'

// Turning a stored action into a sentence an operator can read.
//
// The action strings follow the TABLE names, not the UI's words —
// `workflow_template` is what the UI calls a playbook, `node_command.<slug>` is
// a device command — because rows already written cannot be renamed with a
// rebrand. This is where that gap is closed, on the READ side, so the stored
// value stays stable.
//
// Kept apart from the component so a test can import it without the store graph,
// which reaches `window` at import time.

export type DescribedEvent = { summary: string; detail?: string }

// Target types, in the words the product uses.
const targetWords = (): Record<string, string> => ({
  user: t('widgets.auditTrail.describe.target.user'),
  invite: t('widgets.auditTrail.describe.target.invite'),
  account: t('widgets.auditTrail.describe.target.account'),
  role: t('widgets.auditTrail.describe.target.role'),
  access_entry: t('widgets.auditTrail.describe.target.accessEntry'),
  ssh_key: t('widgets.auditTrail.describe.target.sshKey'),
  secret: t('widgets.auditTrail.describe.target.secret'),
  secret_tag: t('widgets.auditTrail.describe.target.secretTag'),
  node: t('widgets.auditTrail.describe.target.node'),
  organizational_unit: t(
    'widgets.auditTrail.describe.target.organizationalUnit',
  ),
  master_token: t('widgets.auditTrail.describe.target.masterToken'),
  external_identity: t('widgets.auditTrail.describe.target.externalIdentity'),
  utility_assignment: t('widgets.auditTrail.describe.target.utilityAssignment'),
  workflow_template: t('widgets.auditTrail.describe.target.workflowTemplate'),
  workflow_execution: t('widgets.auditTrail.describe.target.workflowExecution'),
  node_adoption_job: t('widgets.auditTrail.describe.target.nodeAdoptionJob'),
  ssh_host: t('widgets.auditTrail.describe.target.sshHost'),
  entitlement: t('widgets.auditTrail.describe.target.entitlement'),
})

// Verbs for the actions whose own name does not read as one. Anything absent
// falls back to the action's own suffix, which is already a verb for most of
// them ("secret.create" -> "create").
const actionWords = (): Record<string, string> => ({
  'secret.values_read': t(
    'widgets.auditTrail.describe.action.secretValuesRead',
  ),
  'account.export': t('widgets.auditTrail.describe.action.accountExport'),
  'image.download': t('widgets.auditTrail.describe.action.imageDownload'),
  'node.terminal_open': t(
    'widgets.auditTrail.describe.action.nodeTerminalOpen',
  ),
  'node.terminal_close': t(
    'widgets.auditTrail.describe.action.nodeTerminalClose',
  ),
  'node.forget': t('widgets.auditTrail.describe.action.nodeForget'),
  'node.ssh_preflight': t(
    'widgets.auditTrail.describe.action.nodeSshPreflight',
  ),
  'node.ssh_adopt.start': t(
    'widgets.auditTrail.describe.action.nodeSshAdoptStart',
  ),
  'node.ssh_adopt.complete': t(
    'widgets.auditTrail.describe.action.nodeSshAdoptComplete',
  ),
  'node.ssh_adopt.fail': t(
    'widgets.auditTrail.describe.action.nodeSshAdoptFail',
  ),
  'node.identity_released': t(
    'widgets.auditTrail.describe.action.nodeIdentityReleased',
  ),
  'node.restore': t('widgets.auditTrail.describe.action.nodeRestore'),
  'node.serial_lease_released': t(
    'widgets.auditTrail.describe.action.nodeSerialLeaseReleased',
  ),
  'playbook.retarget_ou': t(
    'widgets.auditTrail.describe.action.playbookRetargetOu',
  ),
  'member.bindings_replace': t(
    'widgets.auditTrail.describe.action.memberBindingsReplace',
  ),
  'member.remove': t('widgets.auditTrail.describe.action.memberRemove'),
  // The stored action string predates the terms-of-use wording; it is a wire
  // constant and stays as written in existing rows.
  'beta_notice.acknowledge': t(
    'widgets.auditTrail.describe.action.termsAccept',
  ),
  'account.deletion_requested': t(
    'widgets.auditTrail.describe.action.accountDeletionRequested',
  ),
  'account.deletion_cancelled': t(
    'widgets.auditTrail.describe.action.accountDeletionCancelled',
  ),
  'utility.assignment_created': t(
    'widgets.auditTrail.describe.action.utilityAssignmentCreated',
  ),
  'utility.assignment_updated': t(
    'widgets.auditTrail.describe.action.utilityAssignmentUpdated',
  ),
  'utility.assignment_deleted': t(
    'widgets.auditTrail.describe.action.utilityAssignmentDeleted',
  ),
  'access_entry.create_user': t(
    'widgets.auditTrail.describe.action.accessEntryCreateUser',
  ),
  'access_entry.create_ca': t(
    'widgets.auditTrail.describe.action.accessEntryCreateCa',
  ),
  'access_entry.key_attach': t(
    'widgets.auditTrail.describe.action.accessEntryKeyAttach',
  ),
  'access_entry.key_detach': t(
    'widgets.auditTrail.describe.action.accessEntryKeyDetach',
  ),
  'access_entry.apply_offline': t(
    'widgets.auditTrail.describe.action.accessEntryApplyOffline',
  ),
  'access_entry.reapply_node': t(
    'widgets.auditTrail.describe.action.accessEntryReapplyNode',
  ),
  'node_attribute.set': t(
    'widgets.auditTrail.describe.action.nodeAttributeSet',
  ),
  'node_attribute.delete': t(
    'widgets.auditTrail.describe.action.nodeAttributeDelete',
  ),
  'node.device_profile_assign': t(
    'widgets.auditTrail.describe.action.nodeDeviceProfileAssign',
  ),
  'node.policy_reset': t('widgets.auditTrail.describe.action.nodePolicyReset'),
  'account_policy.update': t(
    'widgets.auditTrail.describe.action.accountPolicyUpdate',
  ),
  'role_setting.upsert': t(
    'widgets.auditTrail.describe.action.roleSettingUpsert',
  ),
  'role_setting.reset_role': t(
    'widgets.auditTrail.describe.action.roleSettingResetRole',
  ),
  'role_setting.reset': t(
    'widgets.auditTrail.describe.action.roleSettingReset',
  ),
  'external_identity.link_start': t(
    'widgets.auditTrail.describe.action.externalIdentityLinkStart',
  ),
  'external_identity.unlink': t(
    'widgets.auditTrail.describe.action.externalIdentityUnlink',
  ),
  'user.password_reset_by_operator': t(
    'widgets.auditTrail.describe.action.userPasswordResetByOperator',
  ),
  'user.two_factor_reset_by_operator': t(
    'widgets.auditTrail.describe.action.userTwoFactorResetByOperator',
  ),
  'secret.attachments_replace': t(
    'widgets.auditTrail.describe.action.secretAttachmentsReplace',
  ),
  'secret.tag_attach': t('widgets.auditTrail.describe.action.secretTagAttach'),
  'playbook.import': t('widgets.auditTrail.describe.action.playbookImport'),
  'playbook.run_start': t(
    'widgets.auditTrail.describe.action.playbookRunStart',
  ),
  'playbook.run_cancel': t(
    'widgets.auditTrail.describe.action.playbookRunCancel',
  ),
  'node.approve': t('widgets.auditTrail.describe.action.nodeApprove'),
  'node.reject': t('widgets.auditTrail.describe.action.nodeReject'),
  'node.assign_ou': t('widgets.auditTrail.describe.action.nodeAssignOu'),
  'node.policy_update': t(
    'widgets.auditTrail.describe.action.nodePolicyUpdate',
  ),
  'entitlement.apply': t('widgets.auditTrail.describe.action.entitlementApply'),
  'entitlement.device_cap_reached': t(
    'widgets.auditTrail.describe.action.entitlementDeviceCapReached',
  ),
  'entitlement.account_device_limit_reached': t(
    'widgets.auditTrail.describe.action.entitlementAccountDeviceLimitReached',
  ),
})

// describeAuditEvent renders one row.
//
// It never invents: an action it does not recognise is shown as the stored
// string, because a trail that paraphrases what it does not understand is worse
// than one that quotes it.
export const describeAuditEvent = (event: AuditEvent): DescribedEvent => {
  const target = targetWords()[event.target_type] || event.target_type

  // Device commands carry the slug in the action: node_command.<slug>.
  if (event.action.startsWith('node_command.')) {
    const slug = event.action.slice('node_command.'.length)
    return {
      summary: t('widgets.auditTrail.describe.dispatched', {
        command: slug.replace(/_/g, ' '),
        target,
      }),
      detail: event.detail || undefined,
    }
  }
  // Member status changes carry the status: member.status_<status>.
  if (event.action.startsWith('member.status_')) {
    const status = event.action.slice('member.status_'.length)
    return {
      summary: t('widgets.auditTrail.describe.memberStatus', { status }),
      detail: event.detail || undefined,
    }
  }

  const known = actionWords()[event.action]
  if (known) {
    return {
      summary: `${known} ${target}`.trim(),
      detail: event.detail || undefined,
    }
  }

  const verb = event.action.includes('.')
    ? event.action.slice(event.action.indexOf('.') + 1).replace(/_/g, ' ')
    : event.action
  return {
    summary: `${verb} ${target}`.trim(),
    detail: event.detail || undefined,
  }
}
