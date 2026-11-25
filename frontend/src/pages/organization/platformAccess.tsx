import { useQuery, useQueryClient } from '@tanstack/react-query'
import clsx from 'clsx'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiMembers } from '@/entities/member/api'
import type { Member, MemberRole } from '@/entities/member/api/types'
import {
  editMemberBindings,
  isMemberBindingsStale,
  withoutUnitGrant,
  withUnitGrant,
} from '@/entities/member/bindingEdits'
import { useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showError } from '@/shared/ui/toast'

const selectClassName =
  'bg-neutral-13 rounded px-2.5 py-2 text-sm text-white outline-none transition-colors disabled:opacity-50'

// Roles grantable on a unit. super_admin is deliberately absent: it is
// account-wide by nature and granted on the Team page, never per unit.
const GRANTABLE_ROLES: MemberRole[] = ['viewer', 'manager', 'admin']

type UnitBinding = {
  member: Member
  bindingId: string
  role: MemberRole
  via: 'unit' | 'parent'
}

// Platform access for one unit: who holds a role scoped here (or inherits one
// from the parent), and — for member managers — granting and revoking
// unit-scoped roles. Uses the same members API as the Team page; the rows here
// are the OU-perspective view of the same bindings.
export const PlatformAccessSection = ({
  ouId,
  parentId,
}: {
  ouId: string
  parentId?: string | null
}) => {
  const { t } = useTranslation()
  const can = useCan()
  const canManage = can(Actions.MemberManage)
  const queryClient = useQueryClient()
  const { ous } = useOUs()

  const { data: members, isLoading } = useQuery({
    queryKey: ['members'],
    queryFn: apiMembers.listMembers,
    staleTime: 30 * 1000,
  })

  const [grantUser, setGrantUser] = useState('')
  const [grantRole, setGrantRole] = useState<MemberRole>('viewer')
  const [granting, setGranting] = useState(false)
  // Per-row so revoking one binding does not grey out every other Revoke.
  const [revokingId, setRevokingId] = useState<string | null>(null)
  // A revoke removes someone's role on this unit, so it asks first.
  const [confirmRevoke, setConfirmRevoke] = useState<UnitBinding | null>(null)

  const unitBindings = useMemo<UnitBinding[]>(() => {
    const rows: UnitBinding[] = []
    for (const member of members ?? []) {
      for (const b of member.bindings) {
        if (b.ou_id === ouId) {
          rows.push({ member, bindingId: b.id, role: b.role, via: 'unit' })
        } else if (parentId && b.ou_id === parentId) {
          rows.push({ member, bindingId: b.id, role: b.role, via: 'parent' })
        }
      }
    }
    return rows
  }, [members, ouId, parentId])

  const accountWideCount = useMemo(
    () =>
      (members ?? []).filter((m) => m.bindings.some((b) => !b.ou_id)).length,
    [members],
  )

  // Members with no binding on this unit yet — the grant candidates.
  const candidates = useMemo(
    () =>
      (members ?? []).filter(
        (m) =>
          m.status === 'active' && !m.bindings.some((b) => b.ou_id === ouId),
      ),
    [members, ouId],
  )

  const refresh = useCallback(
    () => queryClient.invalidateQueries({ queryKey: ['members'] }),
    [queryClient],
  )

  // The bindings route replaces the member's whole set, so each edit is built
  // from the member as listed now and carries the binding ids this page showed:
  // if their roles changed in the meantime (on the Team page, say), nothing is
  // written and the operator is told.
  const reportError = useCallback(
    (err: unknown, fallbackKey: 'grantError' | 'revokeError') =>
      isMemberBindingsStale(err)
        ? showError(t('pages.team.errors.bindingsStale'))
        : showError(t(`pages.organization.platformAccess.${fallbackKey}`), err),
    [t],
  )

  const handleGrant = useCallback(async () => {
    const member = (members ?? []).find((m) => m.user_id === grantUser)
    if (!member) return
    setGranting(true)
    try {
      await editMemberBindings(
        member,
        (current) => withUnitGrant(current, grantRole, ouId),
        apiMembers,
      )
      setGrantUser('')
    } catch (err) {
      reportError(err, 'grantError')
    } finally {
      await refresh()
      setGranting(false)
    }
  }, [members, grantUser, grantRole, ouId, refresh, reportError])

  const handleRevoke = useCallback(
    async (row: UnitBinding) => {
      setRevokingId(row.bindingId)
      try {
        await editMemberBindings(
          row.member,
          (current) => withoutUnitGrant(current, row.role, ouId),
          apiMembers,
        )
      } catch (err) {
        reportError(err, 'revokeError')
      } finally {
        await refresh()
        setRevokingId(null)
      }
    },
    [ouId, refresh, reportError],
  )

  const parentName = parentId
    ? (ous.find((u) => u.id === parentId)?.name ??
      t('pages.organization.platformAccess.parentFallback'))
    : null

  if (isLoading) {
    return (
      <div className="text-sm text-neutral-4">
        {t('pages.organization.platformAccess.loading')}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      {unitBindings.length === 0 ? (
        <div className="text-sm text-neutral-6">
          {t('pages.organization.platformAccess.empty')}
        </div>
      ) : (
        <div className="flex flex-col divide-y divide-neutral-12">
          {unitBindings.map((row) => (
            <div
              key={row.bindingId}
              className="flex items-center gap-3 py-2 text-sm"
            >
              <span className="text-neutral-2 truncate">
                {row.member.name || row.member.email}
              </span>
              <span className="text-xs text-neutral-6 capitalize">
                {row.role.replace('_', ' ')}
              </span>
              {row.via === 'parent' && (
                <span className="text-xs text-neutral-6">
                  {t('pages.organization.platformAccess.via', {
                    name: parentName,
                  })}
                </span>
              )}
              {canManage && row.via === 'unit' && (
                <button
                  type="button"
                  onClick={() => setConfirmRevoke(row)}
                  disabled={revokingId === row.bindingId}
                  className="ml-auto text-xs text-red-6 hover:text-red-5 uppercase disabled:opacity-40"
                >
                  {t('pages.organization.platformAccess.revoke')}
                </button>
              )}
            </div>
          ))}
        </div>
      )}
      <div className="text-xs text-neutral-6">
        {t('pages.organization.platformAccess.accountWide', {
          count: accountWideCount,
        })}
      </div>
      {canManage && (
        <div className="flex flex-wrap items-center gap-2 pt-1">
          <select
            value={grantUser}
            onChange={(e) => setGrantUser(e.target.value)}
            disabled={granting}
            className={selectClassName}
            aria-label={t('pages.organization.platformAccess.grantUserLabel')}
          >
            <option value="">
              {t('pages.organization.platformAccess.grantUserPlaceholder')}
            </option>
            {candidates.map((m) => (
              <option key={m.user_id} value={m.user_id}>
                {m.name || m.email}
              </option>
            ))}
          </select>
          <select
            value={grantRole}
            onChange={(e) => setGrantRole(e.target.value as MemberRole)}
            disabled={granting}
            className={clsx(selectClassName, 'capitalize')}
            aria-label={t('pages.organization.platformAccess.grantRoleLabel')}
          >
            {GRANTABLE_ROLES.map((role) => (
              <option key={role} value={role}>
                {role}
              </option>
            ))}
          </select>
          <Button
            size="md"
            onClick={handleGrant}
            disabled={granting || !grantUser}
          >
            {t('pages.organization.platformAccess.grant')}
          </Button>
        </div>
      )}
      <ConfirmDialog
        open={confirmRevoke !== null}
        title={t('pages.organization.platformAccess.revokeTitle')}
        message={t('pages.organization.platformAccess.revokeMessage', {
          name: confirmRevoke?.member.name || confirmRevoke?.member.email || '',
          role: confirmRevoke
            ? t(`pages.team.roles.${confirmRevoke.role}`)
            : '',
        })}
        confirmText={t('pages.organization.platformAccess.revoke')}
        destructive
        onConfirm={() => {
          const row = confirmRevoke
          setConfirmRevoke(null)
          if (row) void handleRevoke(row)
        }}
        onCancel={() => setConfirmRevoke(null)}
      />
    </div>
  )
}
