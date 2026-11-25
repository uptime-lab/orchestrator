// Which tunable permissions do more than their label says.
//
// The role-permissions tab renders every tunable action as one checkbox with a
// short label. Two of them are not peers of "Reboot nodes": `provisioning.sd`
// mints adoption credentials, and `ou.manage` can reparent a unit — which
// resolves access entries as a UNION across the new chain and pushes
// `apply_access_config` to every affected node. Neither consequence is visible
// in "Onboard nodes" or "Manage organizational units", and both are granted by
// ticking a box.
//
// Kept as a pure module so a test can import it without the store graph, and so
// the decision "does this permission escalate" lives in one place rather than
// inside JSX.

// The i18n key holding the escalation note, per action. An action that is not
// here grants exactly what its label says.
const ESCALATION_NOTES: Record<string, string> = {
  'provisioning.sd': 'pages.team.actionNotes.provisioningSd',
  'ou.manage': 'pages.team.actionNotes.ouManage',
}

export const escalationNoteKey = (action: string): string | null =>
  ESCALATION_NOTES[action] ?? null

export const actionEscalates = (action: string): boolean =>
  action in ESCALATION_NOTES
