import { useQueryClient } from '@tanstack/react-query'
import { Locator, Mark } from 'create-locator'
import { useUnit } from 'effector-react'
import { t } from 'i18next'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  ACCOUNT_NAME_MAX,
  ACCOUNT_NAME_MIN,
  apiAccount,
  type AccountDeletionRequest,
} from '@/entities/account/api'
import {
  $currentAccount,
  currentAccountId,
  fetchAccountsFx,
  renameAccountFx,
} from '@/entities/account/model'
import { apiMembers } from '@/entities/member/api'
import { Invite, Member, MemberRole } from '@/entities/member/api/types'
import {
  editMemberBindings,
  isMemberBindingsStale,
} from '@/entities/member/bindingEdits'
import { useOUs } from '@/entities/ou'
import {
  getRolePermissions,
  putRoleSetting,
  resetRoleSetting,
  RolePermissions,
} from '@/entities/permissions/api'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { refreshPermissionsFx } from '@/entities/permissions/model'
import { useServerCapabilities } from '@/entities/serverCapabilities'
import { useSignInOptions } from '@/entities/signInOptions'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { InfoHint } from '@/shared/ui/infoHint'
import { Input } from '@/shared/ui/input'
import { showError } from '@/shared/ui/toast'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { isEmailAddress } from '@/shared/utils/formRules'

import { escalationNoteKey } from './actionEscalation'
import {
  createdInviteHint,
  deletionRequestHint,
  deletionRequestTooltip,
  pendingInviteNotEmailed,
  resentInviteHint,
} from './deletionHint'
import {
  accountWideRole,
  hasAccountWideBinding,
  nextBindings,
  ouScopedBindings,
} from './memberBindings'

export type TeamPageLocator = Locator<Record<string, never>>

type TeamPageProps = Mark<TeamPageLocator>

const ROLES: MemberRole[] = ['viewer', 'manager', 'admin', 'super_admin']

type TabKey = 'members' | 'roles'

// TeamPage is a tabbed view: "Members" (list, roles, invites) and, for a
// super_admin, "Role permissions" (per-role tunable-permission editor). Member
// and role mutations are super_admin-gated server-side.
export const TeamPage = (_: TeamPageProps) => {
  const { t: tt } = useTranslation()
  const check = useCan()
  const canManage = check(Actions.MemberManage)
  // account.update is super_admin-only and not tunable, so this gate matches
  // exactly what the server will allow.
  const canRename = check(Actions.AccountUpdate)
  // account.export is likewise super_admin-only and not tunable, so this gate
  // matches exactly what the server will allow.
  const canExport = check(Actions.AccountExport)
  // account.delete is super_admin-only and not tunable.
  const canDelete = check(Actions.AccountDelete)
  const [tab, setTab] = useState<TabKey>('members')
  const [error, setError] = useState<string | null>(null)

  return (
    <div className="h-full w-full overflow-y-auto p-4 pt-8 md:p-8">
      <h1 className="text-lg uppercase font-bold mb-4">
        {tt('pages.team.title')}
      </h1>

      {/* Tab bar */}
      <div className="flex gap-1 border-b border-neutral-11 mb-6">
        <TabButton
          active={tab === 'members'}
          onClick={() => setTab('members')}
          label={tt('pages.team.tabs.members')}
        />
        {canManage && (
          <TabButton
            active={tab === 'roles'}
            onClick={() => setTab('roles')}
            label={tt('pages.team.tabs.roles')}
          />
        )}
      </div>

      {error && (
        <div className="mb-4 text-sm text-red-9 border border-red-9 rounded px-3 py-2">
          {error}
        </div>
      )}

      {tab === 'members' && (
        <>
          {canRename && <OrganizationSection onError={setError} />}
          {canRename && <TwoFactorPolicySection onError={setError} />}
          {canExport && <DataExportSection onError={setError} />}
          {canDelete && <DeleteAccountSection onError={setError} />}
          <MembersTab canManage={canManage} onError={setError} />
        </>
      )}
      {tab === 'roles' && canManage && (
        <RolePermissionsTab onError={setError} />
      )}
    </div>
  )
}

type TabButtonProps = {
  active: boolean
  onClick: () => void
  label: string
}

const TabButton = ({ active, onClick, label }: TabButtonProps) => (
  <button
    type="button"
    onClick={onClick}
    className={
      'px-4 py-2 text-sm uppercase tracking-wide border-b-2 -mb-px transition-colors ' +
      (active
        ? 'border-lime-6 text-lime-6'
        : 'border-transparent text-neutral-7 hover:text-neutral-5')
    }
  >
    {label}
  </button>
)

// ============= Security policy (require 2FA) =============

type TwoFactorPolicySectionProps = {
  onError: (msg: string) => void
}

// TwoFactorPolicySection toggles the account-wide second-factor requirement.
//
// Turning it on cannot lock existing members out: the server lets a member
// without a factor reach only the enrollment endpoints, so they are funnelled
// into setting one up rather than shut out.
const TwoFactorPolicySection = ({ onError }: TwoFactorPolicySectionProps) => {
  const { t: tt } = useTranslation()
  const account = useUnit($currentAccount)
  const [required, setRequired] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (account) setRequired(account.require_two_factor)
  }, [account?.id, account?.require_two_factor])

  const handleToggle = async (next: boolean) => {
    const accountId = currentAccountId()
    if (!accountId || !account || pending) return
    setRequired(next)
    setPending(true)
    try {
      await apiAccount.putAccountTwoFactorPolicy(accountId, next)
      await fetchAccountsFx()
    } catch (err: unknown) {
      setRequired(!next) // roll the switch back so it never lies about the server
      onError(extractApiErrorMessage(err, tt('pages.team.errors.updatePolicy')))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-3 border border-neutral-10 rounded p-4 mb-8">
      <div className="text-sm uppercase font-bold">
        {tt('pages.team.requireTwoFactor.label')}
      </div>
      <label className="flex items-start gap-3 text-sm text-neutral-5">
        <input
          type="checkbox"
          checked={required}
          disabled={pending}
          onChange={(e) => handleToggle(e.target.checked)}
        />
        <span className="text-xs text-neutral-7">
          {tt('pages.team.requireTwoFactor.hint')}
        </span>
      </label>
    </div>
  )
}

// ============= Data export =============

type DataExportSectionProps = {
  onError: (msg: string) => void
}

// DataExportSection downloads everything the account owns as one JSON document.
//
// It exists so a tenant can leave with their configuration rather than only
// their memory of it. The wording is deliberate about what the file does NOT
// contain: someone who assumes their secrets are in there and then cancels the
// account has lost them, and the endpoint cannot un-tell them afterwards.
const DataExportSection = ({ onError }: DataExportSectionProps) => {
  const { t: tt } = useTranslation()
  const [pending, setPending] = useState(false)

  const handleExport = async () => {
    setPending(true)
    try {
      const { blobUrl, fileName, revoke } = await apiAccount.getAccountExport()
      const a = document.createElement('a')
      a.href = blobUrl
      a.download = fileName
      document.body.appendChild(a)
      a.click()
      a.remove()
      // Freeing the object URL immediately would race the download on some
      // browsers; one tick after the click is enough and leaks nothing.
      setTimeout(revoke, 0)
    } catch (e) {
      onError(extractApiErrorMessage(e, tt('pages.team.dataExport.error')))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="mb-8 max-w-xl">
      <h2 className="text-sm uppercase tracking-wide text-neutral-7 mb-2">
        {tt('pages.team.dataExport.title')}
      </h2>
      <p className="text-xs text-neutral-7 mb-3">
        {tt('pages.team.dataExport.hint')}
        <span className="whitespace-nowrap">
          &nbsp;
          <InfoHint text={tt('pages.team.dataExport.hintTooltip')} />
        </span>
      </p>
      <Button size="lg" onClick={handleExport} disabled={pending}>
        {pending
          ? tt('pages.team.dataExport.pending')
          : tt('pages.team.dataExport.action')}
      </Button>
    </div>
  )
}

// ============= Delete this account =============

type DeleteAccountSectionProps = {
  onError: (msg: string) => void
}

// DeleteAccountSection files a request to have the account erased.
//
// It does NOT delete anything, and the copy says so twice — here and again in
// the server's own `notice`, which is rendered verbatim. The failure mode this
// guards against is a customer who believes their data is already gone, stops
// caring about it, and only later discovers it is not (or that they should have
// exported it first, which they still can).
const DeleteAccountSection = ({ onError }: DeleteAccountSectionProps) => {
  const { t: tt } = useTranslation()
  const account = useUnit($currentAccount)
  // Whether an operator console performs the erasure (the hosted install);
  // a self-hosted install only records the request, and says so.
  const { account_erasure: accountErasure } = useServerCapabilities()
  const [request, setRequest] = useState<AccountDeletionRequest | null>(null)
  const [open, setOpen] = useState(false)
  const [typedName, setTypedName] = useState('')
  const [reason, setReason] = useState('')
  const [pending, setPending] = useState(false)

  useEffect(() => {
    apiAccount
      .getAccountDeletionRequest()
      .then(setRequest)
      .catch(() => {})
  }, [])

  const handleRequest = async () => {
    setPending(true)
    try {
      setRequest(
        await apiAccount.postAccountDeletionRequest(
          typedName,
          reason.trim() || undefined,
        ),
      )
      setOpen(false)
      setTypedName('')
      setReason('')
    } catch (e) {
      onError(extractApiErrorMessage(e, tt('pages.team.deleteAccount.error')))
    } finally {
      setPending(false)
    }
  }

  const handleCancel = async () => {
    setPending(true)
    try {
      await apiAccount.deleteAccountDeletionRequest()
      setRequest(null)
    } catch (e) {
      onError(
        extractApiErrorMessage(e, tt('pages.team.deleteAccount.cancelError')),
      )
    } finally {
      setPending(false)
    }
  }

  if (request) {
    return (
      <div className="mb-8 max-w-xl border border-red-9 rounded px-4 py-3">
        <h2 className="text-sm uppercase tracking-wide text-red-6 mb-2">
          {tt('pages.team.deleteAccount.pendingTitle')}
        </h2>
        {/* The server's wording, not ours: it is the authority on what has and
            has not happened, and restating it here would let the two drift. */}
        <p className="text-xs text-neutral-7 mb-3">{request.notice}</p>
        <Button size="lg" onClick={handleCancel} disabled={pending}>
          {tt('pages.team.deleteAccount.cancel')}
        </Button>
      </div>
    )
  }

  return (
    <div className="mb-8 max-w-xl">
      <h2 className="text-sm uppercase tracking-wide text-neutral-7 mb-2">
        {tt('pages.team.deleteAccount.title')}
      </h2>
      <p className="text-xs text-neutral-7 mb-3">
        {deletionRequestHint(accountErasure)}
        <span className="whitespace-nowrap">
          &nbsp;
          <InfoHint text={deletionRequestTooltip(accountErasure)} />
        </span>
      </p>
      {!open ? (
        <Button size="lg" variant="destructive" onClick={() => setOpen(true)}>
          {tt('pages.team.deleteAccount.action')}
        </Button>
      ) : (
        <div className="flex flex-col gap-3">
          <label className="text-xs text-neutral-7">
            {tt('pages.team.deleteAccount.confirmLabel', {
              name: account?.name ?? '',
            })}
            <Input
              value={typedName}
              onChange={(e) => setTypedName(e.target.value)}
              placeholder={account?.name ?? ''}
            />
          </label>
          <label className="text-xs text-neutral-7">
            {tt('pages.team.deleteAccount.reasonLabel')}
            <Input value={reason} onChange={(e) => setReason(e.target.value)} />
          </label>
          <div className="flex gap-3">
            <Button
              size="lg"
              variant="destructive"
              onClick={handleRequest}
              disabled={pending || !typedName.trim()}
            >
              {tt('pages.team.deleteAccount.submit')}
            </Button>
            <Button size="lg" onClick={() => setOpen(false)} disabled={pending}>
              {tt('pages.team.deleteAccount.back')}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

// ============= Organization (rename) =============

type OrganizationSectionProps = {
  onError: (msg: string) => void
}

// OrganizationSection renames the account (team/company). Rendered only for a
// super_admin; the backend rejects everyone else regardless.
const OrganizationSection = ({ onError }: OrganizationSectionProps) => {
  const { t: tt } = useTranslation()
  const account = useUnit($currentAccount)
  const [name, setName] = useState('')
  const [pending, setPending] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    fetchAccountsFx().catch(() => {})
  }, [])

  // Seed the field once the account name arrives, without clobbering edits.
  useEffect(() => {
    if (account) setName(account.name)
  }, [account?.id, account?.name])

  const trimmed = name.trim()
  const unchanged = !account || trimmed === account.name
  const tooShort = trimmed.length < ACCOUNT_NAME_MIN
  const tooLong = trimmed.length > ACCOUNT_NAME_MAX
  const invalid = tooShort || tooLong

  const handleSubmit = async (e: React.SyntheticEvent) => {
    e.preventDefault()
    const accountId = currentAccountId()
    if (!accountId || invalid || unchanged || pending) return
    setPending(true)
    setSaved(false)
    try {
      await renameAccountFx({ accountId, name: trimmed })
      setSaved(true)
    } catch (err: unknown) {
      onError(
        extractApiErrorMessage(err, tt('pages.team.errors.renameOrganization')),
      )
    } finally {
      setPending(false)
    }
  }

  return (
    <form
      onSubmit={handleSubmit}
      className="flex flex-col gap-3 border border-neutral-10 rounded p-4 mb-8"
    >
      <div className="text-sm uppercase font-bold">
        {tt('pages.team.rename.title')}
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex-1 min-w-[220px]">
          <Input
            id="account-name"
            name="account-name"
            label={tt('pages.team.rename.label')}
            value={name}
            error={name.length > 0 && invalid}
            maxLength={ACCOUNT_NAME_MAX}
            onChange={(e) => {
              setName(e.target.value)
              setSaved(false)
            }}
            required
          />
        </div>
        <Button
          size="lg"
          type="submit"
          disabled={pending || invalid || unchanged}
        >
          {tt('pages.team.rename.submit')}
        </Button>
      </div>
      {name.length > 0 && tooShort && (
        <div className="text-xs text-red-9">
          {tt('pages.team.rename.tooShort')}
        </div>
      )}
      {tooLong && (
        <div className="text-xs text-red-9">
          {tt('pages.team.rename.tooLong')}
        </div>
      )}
      {saved && (
        <div className="text-xs text-lime-6">
          {tt('pages.team.rename.saved')}
        </div>
      )}
    </form>
  )
}

// ============= Members tab =============

type MembersTabProps = {
  canManage: boolean
  onError: (msg: string) => void
}

const MembersTab = ({ canManage, onError }: MembersTabProps) => {
  const { t: tt } = useTranslation()
  // Names for the units a member's scoped bindings point at. A binding whose
  // unit is not in the list (deleted, or outside this caller's AllowedOUs)
  // still shows — as "a unit" rather than vanishing, because a permission that
  // renders as nothing is one nobody removes.
  const { ous } = useOUs()
  const ouNames = useMemo(() => {
    const map: Record<string, string> = {}
    ous.forEach((ou) => {
      map[ou.id] = ou.name
    })
    return map
  }, [ous])
  const [members, setMembers] = useState<Member[]>([])
  const [invites, setInvites] = useState<Invite[]>([])
  const [loading, setLoading] = useState(true)
  // The member a Remove click is asking about; removal waits for the confirm.
  const [removing, setRemoving] = useState<Member | null>(null)
  const [removePending, setRemovePending] = useState(false)
  // A role change and a disable/enable act on someone else's access, so each
  // asks first, like Remove does. The selector keeps showing the current
  // role until the change is confirmed.
  const [pendingChange, setPendingChange] = useState<
    | { kind: 'role'; member: Member; role: MemberRole }
    | { kind: 'status'; member: Member }
    | null
  >(null)
  const [changePending, setChangePending] = useState(false)

  const queryClient = useQueryClient()
  const refresh = useCallback(async () => {
    try {
      // Invites are listed only for a member who may manage them: the route
      // answers anyone else with 403, and the section is hidden for them.
      const [m, i] = await Promise.all([
        apiMembers.listMembers(),
        canManage
          ? apiMembers.listInvites().catch(() => [] as Invite[])
          : Promise.resolve([] as Invite[]),
      ])
      setMembers(m)
      setInvites(i)
      // The units' Platform access pages read the same list from the query
      // cache; keep it in step with what this page just changed. A change
      // here can be to the caller's own roles, so the permission map is
      // re-read too.
      queryClient.setQueryData(['members'], m)
      void refreshPermissionsFx().catch(() => undefined)
    } catch (e: unknown) {
      onError(extractApiErrorMessage(e, tt('pages.team.errors.loadMembers')))
    } finally {
      setLoading(false)
    }
  }, [canManage, onError, tt, queryClient])

  useEffect(() => {
    refresh()
  }, [refresh])

  // The bindings route is a full REPLACE, so the selector has to re-send every
  // binding it does not represent. Sending only its own value deleted each
  // OU-scoped grant the member held, silently.
  // Built from the member as listed now, and refused (nothing written) when
  // their bindings are no longer the ones this list showed: another page (a
  // unit's Platform access) may have changed them since.
  const handleRoleChange = async (member: Member, role: MemberRole) => {
    try {
      await editMemberBindings(
        member,
        (current) => nextBindings(current, role),
        apiMembers,
      )
      await refresh()
    } catch (e: unknown) {
      if (isMemberBindingsStale(e)) {
        onError(tt('pages.team.errors.bindingsStale'))
        await refresh()
        return
      }
      onError(extractApiErrorMessage(e, tt('pages.team.errors.updateRole')))
    }
  }

  const handleToggleStatus = async (member: Member) => {
    try {
      await apiMembers.updateStatus(
        member.user_id,
        member.status === 'active' ? 'disabled' : 'active',
      )
      await refresh()
    } catch (e: unknown) {
      onError(extractApiErrorMessage(e, tt('pages.team.errors.updateStatus')))
    }
  }

  const confirmPendingChange = async () => {
    if (!pendingChange) return
    setChangePending(true)
    try {
      if (pendingChange.kind === 'role') {
        await handleRoleChange(pendingChange.member, pendingChange.role)
      } else {
        await handleToggleStatus(pendingChange.member)
      }
    } finally {
      setChangePending(false)
      setPendingChange(null)
    }
  }

  const handleRemove = async (userId: string) => {
    setRemovePending(true)
    try {
      await apiMembers.removeMember(userId)
      await refresh()
    } catch (e: unknown) {
      onError(extractApiErrorMessage(e, tt('pages.team.errors.removeMember')))
    } finally {
      setRemovePending(false)
      setRemoving(null)
    }
  }

  return (
    <>
      {canManage && <InviteForm onCreated={refresh} onError={onError} />}
      {canManage && invites.length > 0 && (
        <PendingInvites invites={invites} onChanged={refresh} />
      )}

      <h2 className="text-sm uppercase font-bold mt-8 mb-3">
        {tt('pages.team.membersTitle')}
      </h2>
      {loading ? (
        <div className="text-sm text-neutral-7">{tt('pages.team.loading')}</div>
      ) : (
        <div className="flex flex-col gap-2">
          {members.map((member) => (
            <div
              key={member.user_id}
              className="flex items-center gap-4 border border-neutral-10 rounded px-3 py-2"
            >
              <div className="min-w-0 flex-1">
                <div className="text-sm text-neutral-4 truncate">
                  {member.name || member.email}
                </div>
                <div className="text-xs text-neutral-7 truncate">
                  {member.email}
                </div>
                {/* The selector shows one role. Anything scoped to a unit is
                    named here, because a permission an operator cannot see is
                    one they cannot audit — and until this shipped, changing the
                    selector deleted these without saying so. */}
                {ouScopedBindings(member).length > 0 && (
                  <div className="text-xs text-neutral-6 truncate">
                    {ouScopedBindings(member)
                      .map((b) =>
                        tt('pages.team.roleInUnit', {
                          role: tt(`pages.team.roles.${b.role}`),
                          unit:
                            ouNames[b.ou_id ?? ''] ?? tt('pages.team.aUnit'),
                        }),
                      )
                      .join(', ')}
                    {!hasAccountWideBinding(member) &&
                      ` — ${tt('pages.team.noAccountWideRole')}`}
                  </div>
                )}
              </div>
              {member.status === 'disabled' && (
                <span className="text-xxs uppercase tracking-widest text-amber-9">
                  {tt('pages.team.disabled')}
                </span>
              )}
              {canManage ? (
                <select
                  className="bg-neutral-11 text-neutral-4 text-xs rounded px-2 py-1 border border-neutral-9"
                  value={accountWideRole(member)}
                  onChange={(e) =>
                    setPendingChange({
                      kind: 'role',
                      member,
                      role: e.target.value as MemberRole,
                    })
                  }
                >
                  {ROLES.map((role) => (
                    <option key={role} value={role}>
                      {tt(`pages.team.roles.${role}`)}
                    </option>
                  ))}
                </select>
              ) : (
                <span className="text-xs text-neutral-6">
                  {tt(`pages.team.roles.${accountWideRole(member)}`)}
                </span>
              )}
              {canManage && (
                <>
                  <Button
                    size="sm"
                    kind="secondary"
                    onClick={() => setPendingChange({ kind: 'status', member })}
                  >
                    {member.status === 'active'
                      ? tt('pages.team.disable')
                      : tt('pages.team.enable')}
                  </Button>
                  <Button
                    size="sm"
                    variant="destructive"
                    onClick={() => setRemoving(member)}
                  >
                    {tt('pages.team.remove')}
                  </Button>
                </>
              )}
            </div>
          ))}
        </div>
      )}
      <ConfirmDialog
        open={removing !== null}
        title={tt('pages.team.removeConfirm.title')}
        message={tt('pages.team.removeConfirm.message', {
          name: removing?.name || removing?.email || '',
        })}
        confirmText={tt('pages.team.remove')}
        destructive
        busy={removePending}
        onConfirm={() => {
          if (removing) void handleRemove(removing.user_id)
        }}
        onCancel={() => setRemoving(null)}
      />
      <ConfirmDialog
        open={pendingChange !== null}
        title={
          pendingChange?.kind === 'role'
            ? tt('pages.team.changeConfirm.roleTitle')
            : pendingChange?.member.status === 'active'
              ? tt('pages.team.changeConfirm.disableTitle')
              : tt('pages.team.changeConfirm.enableTitle')
        }
        message={
          pendingChange === null
            ? ''
            : pendingChange.kind === 'role'
              ? tt('pages.team.changeConfirm.roleMessage', {
                  name: pendingChange.member.name || pendingChange.member.email,
                  role: tt(`pages.team.roles.${pendingChange.role}`),
                })
              : pendingChange.member.status === 'active'
                ? tt('pages.team.changeConfirm.disableMessage', {
                    name:
                      pendingChange.member.name || pendingChange.member.email,
                  })
                : tt('pages.team.changeConfirm.enableMessage', {
                    name:
                      pendingChange.member.name || pendingChange.member.email,
                  })
        }
        confirmText={
          pendingChange?.kind === 'role'
            ? tt('pages.team.changeConfirm.roleConfirm')
            : pendingChange?.member.status === 'active'
              ? tt('pages.team.disable')
              : tt('pages.team.enable')
        }
        destructive={
          pendingChange?.kind === 'status' &&
          pendingChange.member.status === 'active'
        }
        busy={changePending}
        onConfirm={() => void confirmPendingChange()}
        onCancel={() => setPendingChange(null)}
      />
    </>
  )
}

type InviteFormProps = {
  onCreated: () => void
  onError: (msg: string) => void
}

const InviteForm = ({ onCreated, onError }: InviteFormProps) => {
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<MemberRole>('viewer')
  const [token, setToken] = useState<string | null>(null)
  const [delivered, setDelivered] = useState(false)
  const [sentTo, setSentTo] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [emailInvalid, setEmailInvalid] = useState(false)

  const handleSubmit = async (e: React.SyntheticEvent) => {
    e.preventDefault()
    // The browser's email check accepts an address with no domain dot, which
    // can never receive the invitation; the server refuses it.
    if (!isEmailAddress(email)) {
      setEmailInvalid(true)
      return
    }
    setEmailInvalid(false)
    setPending(true)
    setToken(null)
    try {
      const created = await apiMembers.createInvite({
        email: email.trim(),
        role,
      })
      setToken(created.token)
      setDelivered(created.delivered)
      setSentTo(created.invite.email)
      setEmail('')
      onCreated()
    } catch (err: unknown) {
      onError(extractApiErrorMessage(err, t('pages.team.errors.createInvite')))
    } finally {
      setPending(false)
    }
  }

  const inviteLink = token
    ? `${window.location.origin}/invite?token=${token}`
    : null

  return (
    <form
      onSubmit={handleSubmit}
      className="flex flex-col gap-3 border border-neutral-10 rounded p-4 mb-4"
    >
      <div className="text-sm uppercase font-bold">
        {t('pages.team.invite.title')}
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <div className="flex-1 min-w-[220px]">
          <Input
            id="invite-email"
            name="email"
            type="email"
            label={t('pages.team.invite.email')}
            value={email}
            onChange={(e) => {
              setEmail(e.target.value)
              setEmailInvalid(false)
            }}
            error={emailInvalid}
            required
          />
          {emailInvalid && (
            <div className="text-xs text-red-6 mt-1">
              {t('shared.form.emailInvalid')}
            </div>
          )}
        </div>
        <select
          className="bg-neutral-11 text-neutral-4 text-xs rounded px-2 py-2 border border-neutral-9"
          value={role}
          onChange={(e) => setRole(e.target.value as MemberRole)}
        >
          {ROLES.map((r) => (
            <option key={r} value={r}>
              {t(`pages.team.roles.${r}`)}
            </option>
          ))}
        </select>
        <Button size="lg" type="submit" disabled={pending}>
          {t('pages.team.invite.submit')}
        </Button>
      </div>
      {inviteLink && (
        <div className="text-xs text-neutral-6 bg-neutral-11 rounded p-3">
          {/* The link is shown even after a successful send: it is the fallback
              for mail that lands in a spam folder, and the only channel at all
              on installs with no SMTP. */}
          <div className="mb-1 text-neutral-7">
            {createdInviteHint(delivered, sentTo)}
          </div>
          <code className="break-all text-lime-6">{inviteLink}</code>
        </div>
      )}
    </form>
  )
}

type PendingInvitesProps = {
  invites: Invite[]
  onChanged: () => void
}

const PendingInvites = ({ invites, onChanged }: PendingInvitesProps) => {
  const notEmailed = pendingInviteNotEmailed(
    useSignInOptions().options?.email_delivery,
  )
  const [resent, setResent] = useState<
    Record<string, { link: string; delivered: boolean }>
  >({})

  // Revoking asks first: the link already sent stops working.
  const [revoking, setRevoking] = useState<Invite | null>(null)
  const [revokePending, setRevokePending] = useState(false)

  // The server's reason (a resend cooldown with its wait, the daily cap, a
  // full seat count, an invite already gone) is the whole diagnostic, so it
  // reaches the operator rather than being dropped.
  const handleRevoke = async (id: string) => {
    setRevokePending(true)
    try {
      await apiMembers.revokeInvite(id)
    } catch (err) {
      showError(t('pages.team.invite.revokeError'), err)
    } finally {
      setRevokePending(false)
      setRevoking(null)
      onChanged()
    }
  }

  // Resending mints a new token, so the row's id changes. Key the confirmation
  // by the EMAIL rather than the invite id, which no longer exists after the
  // refresh, and keep the fresh link visible for installs without SMTP.
  const handleResend = async (invite: Invite) => {
    try {
      const created = await apiMembers.resendInvite(invite.id)
      setResent((prev) => ({
        ...prev,
        [invite.email]: {
          link: `${window.location.origin}/invite?token=${created.token}`,
          delivered: created.delivered,
        },
      }))
      onChanged()
    } catch (err) {
      showError(t('pages.team.invite.resendError'), err)
    }
  }

  return (
    <div className="mb-4">
      <h2 className="text-sm uppercase font-bold mb-3">
        {t('pages.team.pendingInvites')}
      </h2>
      <div className="flex flex-col gap-2">
        {invites.map((invite) => (
          <div
            key={invite.id}
            className="flex flex-col gap-2 border border-neutral-10 rounded px-3 py-2"
          >
            <div className="flex items-center gap-4">
              <div className="min-w-0 flex-1 text-sm text-neutral-5 truncate">
                {invite.email}
              </div>
              <span className="text-xs text-neutral-7">
                {t(`pages.team.roles.${invite.role}`)}
              </span>
              {notEmailed && (
                <span className="whitespace-nowrap text-xs text-neutral-7">
                  {t('pages.team.invite.notEmailed')}&nbsp;
                  <InfoHint text={t('pages.team.invite.notEmailedTooltip')} />
                </span>
              )}
              <span
                className="whitespace-nowrap text-xs text-neutral-7"
                title={new Date(invite.expires_at).toLocaleString()}
              >
                {t('pages.team.invite.expires', {
                  date: new Date(invite.expires_at).toLocaleDateString(),
                })}
              </span>
              <Button size="sm" onClick={() => handleResend(invite)}>
                {t('pages.team.invite.resend')}
              </Button>
              <Button
                size="sm"
                variant="destructive"
                onClick={() => setRevoking(invite)}
              >
                {t('pages.team.invite.revoke')}
              </Button>
            </div>
            {resent[invite.email] && (
              <div className="text-xs text-neutral-6 bg-neutral-11 rounded p-2">
                <div className="mb-1 text-neutral-7">
                  {resentInviteHint(resent[invite.email].delivered)}
                </div>
                <code className="break-all text-lime-6">
                  {resent[invite.email].link}
                </code>
              </div>
            )}
          </div>
        ))}
      </div>
      <ConfirmDialog
        open={revoking !== null}
        title={t('pages.team.invite.revokeTitle')}
        message={t('pages.team.invite.revokeMessage', {
          email: revoking?.email ?? '',
        })}
        confirmText={t('pages.team.invite.revoke')}
        destructive
        busy={revokePending}
        onConfirm={() => {
          if (revoking) void handleRevoke(revoking.id)
        }}
        onCancel={() => setRevoking(null)}
      />
    </div>
  )
}

// ============= Role permissions tab =============

type RolePermissionsTabProps = {
  onError: (msg: string) => void
}

// actionLabel renders a tunable action key as a readable label (falls back to
// the raw key). Roles/actions come from the backend, so this only prettifies.
const actionLabel = (action: string): string => {
  const key = `pages.team.actions.${action}`
  // Dynamic key: cast past i18next's typed-key check, fall back to the raw key.
  const translated = t(key as never) as string
  return translated === key ? action : translated
}

// roleLabel renders a role key (dynamic string) as its localized name.
const roleLabel = (role: string): string =>
  t(`pages.team.roles.${role}` as never) as string

const RolePermissionsTab = ({ onError }: RolePermissionsTabProps) => {
  const { t: tt } = useTranslation()
  const [roles, setRoles] = useState<RolePermissions[]>([])
  const [openRole, setOpenRole] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    try {
      setRoles(await getRolePermissions())
    } catch (e: unknown) {
      onError(
        extractApiErrorMessage(e, tt('pages.team.errors.loadRolePermissions')),
      )
    } finally {
      setLoading(false)
    }
  }, [onError, tt])

  useEffect(() => {
    load()
  }, [load])

  const handleToggle = async (
    role: string,
    action: string,
    allowed: boolean,
  ) => {
    try {
      await putRoleSetting(role, action, allowed)
      await load()
      // The change may be to the caller's own role.
      void refreshPermissionsFx().catch(() => undefined)
    } catch (e: unknown) {
      onError(
        extractApiErrorMessage(e, tt('pages.team.errors.updatePermission')),
      )
    }
  }

  const handleResetRole = async (role: string) => {
    try {
      await resetRoleSetting(role)
      await load()
      void refreshPermissionsFx().catch(() => undefined)
    } catch (e: unknown) {
      onError(extractApiErrorMessage(e, tt('pages.team.errors.resetRole')))
    }
  }

  if (loading) {
    return (
      <div className="text-sm text-neutral-7">{tt('pages.team.loading')}</div>
    )
  }

  return (
    <div className="flex flex-col gap-2 max-w-2xl">
      <p className="text-xs text-neutral-7 mb-2">
        {tt('pages.team.roleSettings.hint')}
        <span className="whitespace-nowrap">
          &nbsp;
          <InfoHint text={tt('pages.team.roleSettings.hintTooltip')} />
        </span>
      </p>
      {roles.map((rp) => {
        const isOpen = openRole === rp.role
        const overriddenCount = rp.permissions.filter(
          (p) => p.overridden,
        ).length
        return (
          <div key={rp.role} className="border border-neutral-10 rounded">
            <button
              type="button"
              onClick={() => setOpenRole(isOpen ? null : rp.role)}
              className="w-full flex items-center justify-between px-4 py-3 text-left"
            >
              <span className="text-sm font-bold text-neutral-4">
                {roleLabel(rp.role)}
              </span>
              <span className="flex items-center gap-3">
                {overriddenCount > 0 && (
                  <span className="text-xxs uppercase tracking-widest text-amber-9">
                    {tt('pages.team.roleSettings.customized', {
                      count: overriddenCount,
                    })}
                  </span>
                )}
                <span className="text-neutral-7 text-xs">
                  {isOpen ? '▲' : '▼'}
                </span>
              </span>
            </button>
            {isOpen && (
              <div className="border-t border-neutral-11 px-4 py-3 flex flex-col gap-2">
                {rp.permissions.map((p) => {
                  const noteKey = escalationNoteKey(p.action)
                  return (
                    <label
                      key={p.action}
                      className="flex items-start gap-3 text-sm text-neutral-5"
                    >
                      <input
                        className="mt-1"
                        type="checkbox"
                        checked={p.allowed}
                        onChange={(e) =>
                          handleToggle(rp.role, p.action, e.target.checked)
                        }
                      />
                      <span className="flex-1">
                        {actionLabel(p.action)}
                        {/* Two of these permissions do more than their label
                            says, and both are granted by ticking a box. Shown
                            in place rather than behind a hover: this page is
                            visited rarely and the consequence is the decision. */}
                        {noteKey && (
                          <span className="block text-xs text-amber-9/80">
                            {tt(noteKey as never)}
                          </span>
                        )}
                      </span>
                      {p.overridden ? (
                        <span className="mt-0.5 text-xxs uppercase tracking-widest text-amber-9">
                          {tt('pages.team.roleSettings.overridden')}
                        </span>
                      ) : (
                        <span className="mt-0.5 text-xxs uppercase tracking-widest text-neutral-8">
                          {tt('pages.team.roleSettings.default')}
                        </span>
                      )}
                    </label>
                  )
                })}
                {overriddenCount > 0 && (
                  <div className="pt-2">
                    <Button
                      size="sm"
                      kind="secondary"
                      onClick={() => handleResetRole(rp.role)}
                    >
                      {tt('pages.team.roleSettings.resetRole')}
                    </Button>
                  </div>
                )}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
