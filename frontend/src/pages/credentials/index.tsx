import { getRouteApi, useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { CredentialsTab } from '@/routes/_mainLayout/credentials'

import { AttachKeyToUserDialog } from '@/widgets/attachKeyToUser'
import { OU_FILTER_ALL } from '@/widgets/ouRail'
import { OURailAside } from '@/widgets/ouRail/OURailAside.tsx'
import { SidebarEnum } from '@/widgets/sidebars/types.ts'

import {
  ACCESS_ACCOUNT_WIDE_FILTER,
  ACCESS_NODE_FILTER,
  ACCESS_UNASSIGNED_FILTER,
  accessChanged,
  apiAccess,
  caPlacements,
  editAccessEntry,
  ouName,
  withUnitAdded,
  type AccessEntry,
  type AccessOUCountsResult,
  type AccessScopeSelector,
} from '@/entities/access'
import { useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { useSSHKeys } from '@/entities/sshKey'
import { SSHKEY_DND_TYPE } from '@/entities/sshKey/ui/keyList/keyCard'

import { Button } from '@/shared/ui/button/button.tsx'
import { Column, DataTable } from '@/shared/ui/dataTable'
import { InfoHint } from '@/shared/ui/infoHint'
import { showError, showInfo } from '@/shared/ui/toast'

const routeApi = getRouteApi('/_mainLayout/credentials')

// Rail selection sentinels (shared with the sidebars). OU_FILTER_ALL (the
// empty/undefined ou) is the "All" view and the default on every tab.
// ACCOUNT_WIDE selects the fleet-wide bucket, UNASSIGNED the applies-nowhere
// bucket; a real OU id selects that unit.
const ACCOUNT_WIDE_FILTER = ACCESS_ACCOUNT_WIDE_FILTER
const UNASSIGNED_FILTER = ACCESS_UNASSIGNED_FILTER
// NODE selects the "assigned to nodes" bucket (users bound to a single node).
const NODE_FILTER = ACCESS_NODE_FILTER

// dataTransfer type for dragging a user row onto the OU rail.
export const ACCESS_USER_DND_TYPE = 'text/accessuser-id'

// Loads every access entry of the account once (scope=all) and keeps it fresh
// on the shared accessChanged event. One source drives the rail badges, the
// key-list filters and the CA trust overview, so they can never disagree.
//
// The list route needs access.manage; without it nothing is requested and the
// page reads an empty set rather than waiting on a 403.
const NO_ENTRIES: AccessEntry[] = []
const useAllAccessEntries = (enabled: boolean) => {
  const [entries, setEntries] = useState<AccessEntry[] | null>(null)
  const refetch = useCallback(async () => {
    if (!enabled) return
    try {
      setEntries(await apiAccess.getAccessEntries({ kind: 'all' }))
    } catch {
      // Keep the last good set; a failed refresh degrades to stale data.
    }
  }, [enabled])
  useEffect(() => {
    void refetch()
  }, [refetch])
  useEffect(() => {
    const sub = accessChanged.watch(() => void refetch())
    return () => sub.unsubscribe()
  }, [refetch])
  return enabled ? entries : NO_ENTRIES
}

// Local hook mirroring useSecretOUCounts: per-OU access-entry counts for the rail.
const useAccessOUCounts = (enabled: boolean) => {
  const [counts, setCounts] = useState<AccessOUCountsResult>({
    ous: [],
    unassigned: { users: 0, cas: 0 },
    account_wide: { users: 0, cas: 0 },
    total: 0,
  })
  const refetch = useCallback(async () => {
    if (!enabled) return
    try {
      setCounts(await apiAccess.getAccessOUCounts())
    } catch {
      // Leave the last good counts; the rail degrades to stale numbers, never
      // to a broken page.
    }
  }, [enabled])
  useEffect(() => {
    void refetch()
  }, [refetch])
  // Keep the rail counts fresh after a create/delete in a sidebar — the
  // accessChanged event's contract names the rail counts as a subscriber.
  useEffect(() => {
    const sub = accessChanged.watch(() => void refetch())
    return () => sub.unsubscribe()
  }, [refetch])
  return { counts, refetch }
}

export const CredentialsPage = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = routeApi.useSearch()
  const tab = search.tab
  const check = useCan()
  const canManageKeys = check(Actions.SSHKeyManage)
  const canManageAccess = check(Actions.AccessManage)

  const { keys } = useSSHKeys()
  const { ous } = useOUs()
  const { counts, refetch: refetchCounts } = useAccessOUCounts(canManageAccess)
  const allEntries = useAllAccessEntries(canManageAccess)

  const selectedAccessOU =
    search.ou &&
    search.ou !== OU_FILTER_ALL &&
    search.ou !== ACCOUNT_WIDE_FILTER &&
    search.ou !== UNASSIGNED_FILTER &&
    search.ou !== NODE_FILTER
      ? ous.find((ou) => ou.id === search.ou)
      : undefined
  // The node bucket is filtered client-side (scope==='node'); the API scope
  // selector has no node kind, so pageScope stays 'all' and UsersTable narrows.
  const isNodeFilter = search.ou === NODE_FILTER

  // The scope the Users view loads and labels. A selected OU wins; otherwise
  // the pseudo-rows decide: "All nodes" → account-wide, "Unassigned" → the
  // applies-nowhere bucket, default → everything.
  const pageScope: AccessScopeSelector = selectedAccessOU
    ? { kind: 'ou', ouId: selectedAccessOU.id }
    : search.ou === ACCOUNT_WIDE_FILTER
      ? { kind: 'account' }
      : search.ou === UNASSIGNED_FILTER
        ? { kind: 'unassigned' }
        : { kind: 'all' }
  const pageScopeLabel =
    selectedAccessOU?.name ??
    (search.ou === ACCOUNT_WIDE_FILTER
      ? t('pages.credentials.accountWide')
      : search.ou === UNASSIGNED_FILTER
        ? t('pages.credentials.unassigned')
        : search.ou === NODE_FILTER
          ? t('pages.credentials.rail.nodes')
          : t('pages.credentials.allScopes'))

  const selectedKeyId =
    search.sidebar === 'sshKeyDetails' ? search.keyId : undefined

  const setTab = useCallback(
    (next: CredentialsTab) => {
      navigate({
        to: '.',
        search: (prev) => ({ ...prev, tab: next, sidebar: undefined }),
        replace: true,
      })
    },
    [navigate],
  )

  const handleKeyClick = useCallback(
    (keyId: string) => {
      navigate({
        to: '.',
        search: (prev) => ({
          ...prev,
          sidebar: SidebarEnum.SSHKeyDetails,
          keyId,
        }),
      })
    },
    [navigate],
  )

  const handleCreateKey = useCallback(() => {
    navigate({
      to: '.',
      search: (prev) => ({ ...prev, sidebar: SidebarEnum.CreateSSHKey }),
    })
  }, [navigate])

  const handleAddUser = useCallback(() => {
    navigate({
      to: '.',
      search: (prev) => ({ ...prev, sidebar: SidebarEnum.CreateAccessUser }),
    })
  }, [navigate])

  const handleAddCA = useCallback(() => {
    navigate({
      to: '.',
      search: (prev) => ({ ...prev, sidebar: SidebarEnum.CreateCACert }),
    })
  }, [navigate])

  const handleUserClick = useCallback(
    (entryId: string) => {
      navigate({
        to: '.',
        search: (prev) => ({
          ...prev,
          sidebar: SidebarEnum.AccessUserDetails,
          entryId,
        }),
      })
    },
    [navigate],
  )

  // Inventories: ssh keys vs CA keys are the same store, split by key_type,
  // then filtered by the rail selection: an OU shows only the keys attached to
  // entries living there, "All nodes" the fleet-wide ones, "Unassigned" the
  // keys not attached to any entry at all.
  const keyIdsFor = useCallback(
    (isCA: boolean): string[] => {
      const inventory = keys
        .filter((k) => (k.key_type === 'ca') === isCA)
        .map((k) => k.id)
      const filtered =
        selectedAccessOU !== undefined ||
        search.ou === ACCOUNT_WIDE_FILTER ||
        search.ou === UNASSIGNED_FILTER
      if (!filtered) {
        // "All" view: the whole inventory.
        return inventory
      }
      const entries = (allEntries ?? []).filter(
        (e) => (e.type === 'ca') === isCA,
      )
      if (selectedAccessOU) {
        const set = new Set(
          entries
            .filter((e) => e.ou_ids.includes(selectedAccessOU.id))
            .flatMap((e) => e.key_ids),
        )
        return inventory.filter((id) => set.has(id))
      }
      if (search.ou === ACCOUNT_WIDE_FILTER) {
        const set = new Set(
          entries
            .filter((e) => e.scope === 'account')
            .flatMap((e) => e.key_ids),
        )
        return inventory.filter((id) => set.has(id))
      }
      if (search.ou === UNASSIGNED_FILTER) {
        // Keys not referenced by ANY entry (of any type) — the true leftovers.
        const referenced = new Set((allEntries ?? []).flatMap((e) => e.key_ids))
        return inventory.filter((id) => !referenced.has(id))
      }
      return inventory
    },
    [keys, allEntries, selectedAccessOU, search.ou],
  )
  const sshKeyIds = useMemo(() => keyIdsFor(false), [keyIdsFor])
  const caKeyIds = useMemo(() => keyIdsFor(true), [keyIdsFor])

  const tabs: { id: CredentialsTab; label: string }[] = [
    { id: 'ssh-keys', label: t('pages.credentials.tabs.sshKeys') },
    { id: 'ca', label: t('pages.credentials.tabs.ca') },
    { id: 'users', label: t('pages.credentials.tabs.users') },
  ]

  return (
    <div className="h-full w-full flex">
      <CredentialsFilterRail
        tab={tab}
        counts={counts}
        allEntries={allEntries}
        canManageAccess={canManageAccess}
        onDropDone={refetchCounts}
      />

      <div className="flex-1 min-w-0 overflow-y-auto p-4 pt-8 md:p-8">
        <h1 className="text-lg uppercase font-bold mb-6">
          {t('pages.credentials.title')}
        </h1>

        <div className="flex gap-1 border-b border-neutral-12 mb-6">
          {tabs.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => setTab(item.id)}
              className={clsx(
                'font-mono text-sm px-3 py-2 border-b-2 -mb-px transition-colors',
                tab === item.id
                  ? 'text-lime-6 border-lime-6'
                  : 'text-neutral-4 hover:text-neutral-2 border-transparent',
              )}
            >
              {item.label}
            </button>
          ))}
        </div>

        {tab === 'ssh-keys' && (
          <>
            <div className="flex items-center justify-between gap-4 mb-6">
              <p className="text-sm text-neutral-5 max-w-3xl">
                {t('pages.credentials.hints.sshKeys')}
                <span className="whitespace-nowrap">
                  &nbsp;
                  <InfoHint
                    text={t('pages.credentials.hints.sshKeysTooltip')}
                  />
                </span>
              </p>
              {canManageKeys && (
                <Button size="md" onClick={handleCreateKey}>
                  {t('pages.credentials.createKey')}
                </Button>
              )}
            </div>
            <KeysTable
              visibleIds={sshKeyIds}
              allEntries={allEntries}
              selectedKeyId={selectedKeyId}
              onRowClick={handleKeyClick}
              emptyText={t('pages.credentials.emptyKeys')}
            />
          </>
        )}

        {tab === 'ca' && (
          <>
            <div className="flex items-center justify-between gap-4 mb-6">
              <p className="text-sm text-neutral-5 max-w-3xl">
                {t('pages.credentials.hints.ca')}
                <span className="whitespace-nowrap">
                  &nbsp;
                  <InfoHint text={t('pages.credentials.hints.caTooltip')} />
                </span>
              </p>
              {/* Adding a CA creates a key (POST /ssh-keys); trusting it is
                  the separate access step. */}
              {canManageKeys && (
                <Button size="md" onClick={handleAddCA}>
                  {t('pages.credentials.createCA')}
                </Button>
              )}
            </div>
            {/* Each CA with WHERE it is trusted (narrowed by the rail). Trust
                is created by dragging a CA onto a unit; principals live on
                users now, so they are not shown here. */}
            <CATable
              visibleIds={caKeyIds}
              allEntries={allEntries}
              selectedKeyId={selectedKeyId}
              onRowClick={handleKeyClick}
              scopeFilter={pageScope}
              emptyText={t('pages.credentials.emptyCAs')}
            />
          </>
        )}

        {tab === 'users' && (
          <div>
            <div className="flex items-center justify-between gap-4 mb-6">
              <p className="text-sm text-neutral-5 max-w-3xl">
                {t('pages.credentials.hints.users')}
                <span className="whitespace-nowrap">
                  &nbsp;
                  <InfoHint text={t('pages.credentials.hints.usersTooltip')} />
                </span>
              </p>
              {canManageAccess && (
                <Button size="md" onClick={handleAddUser}>
                  {t('pages.credentials.addUser')}
                </Button>
              )}
            </div>
            {!canManageAccess && (
              <p className="text-sm text-neutral-5">
                {t('pages.credentials.usersNoPermission')}
              </p>
            )}
            {canManageAccess && (
              <UsersTable
                scope={pageScope}
                scopeLabel={pageScopeLabel}
                nodeFilter={isNodeFilter}
                allEntries={allEntries}
                totalEntries={counts.total}
                onRowClick={handleUserClick}
                onAddUser={handleAddUser}
                canManage={canManageAccess}
              />
            )}
            <p className="mt-4 text-xs text-neutral-6">
              {t('pages.credentials.applyNote')}
            </p>
          </div>
        )}
      </div>
    </div>
  )
}

type RailProps = {
  tab: CredentialsTab
  counts: AccessOUCountsResult
  allEntries: AccessEntry[] | null
  canManageAccess: boolean
  onDropDone: () => void
}

const CredentialsFilterRail = ({
  tab,
  counts,
  allEntries,
  canManageAccess,
  onDropDone,
}: RailProps) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = routeApi.useSearch()
  const { ous } = useOUs()
  const { keys } = useSSHKeys()

  // Drop state for the "attach a key to which user?" dialog.
  const [dropKeyId, setDropKeyId] = useState<string | null>(null)
  const [dropOU, setDropOU] = useState<string | undefined>(undefined)
  const [dropAccountWide, setDropAccountWide] = useState(false)

  const selectedOU = search.ou ?? OU_FILTER_ALL

  // Rail badges count what the active tab actually lists: distinct KEYS per
  // unit on the ssh-keys tab, user entries on the Users tab, CA trusts on the
  // CA tab. A number that matches nothing on screen reads as a broken counter.
  const pick = (c: { users: number; cas: number }) =>
    tab === 'ca' ? c.cas : c.users
  const isCATab = tab === 'ca'
  // Distinct keys of the tab's type attached to entries living in each unit.
  const keyCountsByOU = useMemo(() => {
    const out: Record<string, Set<string>> = {}
    for (const e of allEntries ?? []) {
      if ((e.type === 'ca') !== isCATab) continue
      for (const ou of e.ou_ids) {
        out[ou] = out[ou] ?? new Set()
        for (const k of e.key_ids) out[ou].add(k)
      }
    }
    return out
  }, [allEntries, isCATab])
  const treeCounts = useMemo(() => {
    const out: Record<string, number> = {}
    if (tab === 'users') {
      for (const entry of counts.ous) out[entry.ou_id] = pick(entry.count)
    } else {
      for (const [ou, set] of Object.entries(keyCountsByOU)) out[ou] = set.size
    }
    return out
  }, [counts, tab, keyCountsByOU])

  const setFilterOU = (value: string) =>
    navigate({
      to: '.',
      search: (prev) => ({
        ...prev,
        ou: value === OU_FILTER_ALL ? undefined : value,
      }),
      replace: true,
    })

  // Drag onto OU: keys/CAs on their tabs, USER ROWS on the users tab — all
  // gated on the manage permission. A key drop opens a chooser (a key belongs
  // to a user); a user drop ADDS a unit membership (multi-OU, additive).
  const dropTypes = !canManageAccess
    ? []
    : tab === 'users'
      ? [ACCESS_USER_DND_TYPE]
      : [SSHKEY_DND_TYPE]

  const handleDrop = async (
    itemId: string,
    target: string | null,
    itemType: string,
  ) => {
    // Pseudo-row sentinels: "All nodes" = fleet-wide, "Unassigned" = park.
    const accountWide = target === ACCOUNT_WIDE_FILTER
    const unassign = target === UNASSIGNED_FILTER
    const ouId = target && !accountWide && !unassign ? target : undefined

    if (itemType === ACCESS_USER_DND_TYPE) {
      // Dropping a user ADDS the unit to its membership set (a user can live
      // in several units); "All nodes" switches it to the fleet-wide default,
      // "Unassigned" detaches it everywhere. A user already on every node is
      // left as it is: re-scoping it to the one unit would lock it on every
      // other node. The change is computed from the entry as it is now, not
      // from this list.
      const entry = (allEntries ?? []).find((e) => e.id === itemId)
      if (!entry || entry.scope === 'node') return
      if (ouId && (entry.account_wide || entry.scope === 'account')) {
        showInfo(
          t('pages.credentials.rail.userAlreadyEverywhere', {
            name: entry.username ?? '',
          }),
        )
        return
      }
      try {
        await editAccessEntry(entry.id, (current) =>
          accountWide
            ? current.account_wide
              ? null
              : { scope: { account_wide: true } }
            : unassign
              ? { scope: {} }
              : withUnitAdded(current, ouId!),
        )
        accessChanged()
        // Stay put: the row updates in place (its "Assigned to" reflects the
        // new unit). Jumping the rail to the drop target yanked the operator
        // out of whatever view they were organizing from.
        onDropDone()
      } catch (err) {
        showError(t('pages.credentials.rail.dropFailed'), err)
      }
      return
    }

    if (tab === 'ca') {
      // A CA is a pure trust anchor now (principals live on users), so a drop
      // just trusts it on the target — no dialog. Unassigned drop is a no-op.
      // Idempotent + merged: a CA has at most ONE unit-scoped entry (its OU set)
      // and ONE account-wide entry, so dropping on a new unit ADDS to the set
      // rather than creating a duplicate row.
      if (unassign) return
      const cas = (allEntries ?? []).filter(
        (e) => e.type === 'ca' && e.key_ids.includes(itemId),
      )
      try {
        if (accountWide) {
          if (!cas.some((e) => e.scope === 'account')) {
            await apiAccess.createAccessEntry({
              type: 'ca',
              account_wide: true,
              ca_key_id: itemId,
            })
          }
        } else {
          const unitEntry = cas.find(
            (e) => e.scope !== 'account' && e.scope !== 'node',
          )
          if (unitEntry) {
            await editAccessEntry(unitEntry.id, (current) =>
              withUnitAdded(current, ouId!),
            )
          } else {
            await apiAccess.createAccessEntry({
              type: 'ca',
              ou_ids: [ouId!],
              ca_key_id: itemId,
            })
          }
        }
        accessChanged()
        // Stay put — the CA's "Trusted on" cell updates in place.
        onDropDone()
      } catch (err) {
        showError(t('pages.credentials.rail.dropFailed'), err)
      }
      return
    }
    // SSH key: ask which user should receive it.
    setDropKeyId(itemId)
    setDropOU(ouId)
    setDropAccountWide(accountWide)
  }

  const dropHint =
    tab === 'users'
      ? t('pages.credentials.rail.dropHintUsers')
      : tab === 'ca'
        ? t('pages.credentials.rail.dropHintCA')
        : t('pages.credentials.rail.dropHintKeys')

  const droppedKey = keys.find((k) => k.id === dropKeyId)
  const droppedScopeName =
    dropOU !== undefined
      ? (ous.find((o) => o.id === dropOU)?.name ?? dropOU)
      : t('pages.credentials.accountWide')

  // Pseudo-row counts for the key inventories: the whole inventory, keys not
  // referenced by any entry, and keys delivered fleet-wide.
  const tabKeys = keys.filter((k) => (k.key_type === 'ca') === isCATab)
  const referencedKeyIds = new Set((allEntries ?? []).flatMap((e) => e.key_ids))
  const fleetKeyIds = new Set(
    (allEntries ?? [])
      .filter((e) => (e.type === 'ca') === isCATab && e.scope === 'account')
      .flatMap((e) => e.key_ids),
  )
  const userTotals = {
    // DISTINCT user entries: a user in several OUs is counted once here, so we
    // can't sum the per-OU badges (which correctly count it in each unit).
    all: (allEntries ?? []).filter((e) => e.type === 'user').length,
    unassigned: counts.unassigned.users,
    accountWide: counts.account_wide.users,
    // Node-bound entries are excluded from the counts API (node_id IS NULL),
    // so count them client-side from the account-wide `all` set.
    nodes: (allEntries ?? []).filter(
      (e) => e.type === 'user' && e.scope === 'node',
    ).length,
  }

  // Three pseudo-rows, per-tab semantics:
  //  - All (default): everything — all keys / all users / all CAs.
  //  - Unassigned/Unattached: keys referenced by nothing, or entries applied
  //    to no node. Drop target only for USERS (drop = park the user there);
  //    for keys there is nothing to attach to, so it exposes none.
  //  - All nodes: the fleet-wide bucket. Drop target on every tab, routed
  //    through the same confirm dialogs / additive scope update as a unit.
  // Pseudo-row drop targets are the sentinel strings themselves, so handleDrop
  // can tell them apart from real unit ids.
  const pseudoRows =
    tab === 'users'
      ? [
          {
            value: OU_FILTER_ALL,
            label: t('pages.credentials.rail.allUsers'),
            count: userTotals.all,
          },
          {
            value: UNASSIGNED_FILTER,
            label: t('pages.credentials.rail.unassigned'),
            count: userTotals.unassigned,
            dropTarget: UNASSIGNED_FILTER,
          },
          {
            value: ACCOUNT_WIDE_FILTER,
            label: t('pages.credentials.rail.accountWide'),
            count: userTotals.accountWide,
            dropTarget: ACCOUNT_WIDE_FILTER,
          },
          // Node-bound users. Not a drop target (a drop can't pick which node);
          // binding happens from the node's own Credentials panel.
          {
            value: NODE_FILTER,
            label: t('pages.credentials.rail.nodes'),
            count: userTotals.nodes,
          },
        ]
      : [
          {
            value: OU_FILTER_ALL,
            label: isCATab
              ? t('pages.credentials.rail.allCAs')
              : t('pages.credentials.rail.allKeys'),
            count: tabKeys.length,
          },
          {
            value: UNASSIGNED_FILTER,
            label: isCATab
              ? t('pages.credentials.rail.untrusted')
              : t('pages.credentials.rail.unattached'),
            count: tabKeys.filter((k) => !referencedKeyIds.has(k.id)).length,
          },
          {
            value: ACCOUNT_WIDE_FILTER,
            label: t('pages.credentials.rail.accountWide'),
            count: tabKeys.filter((k) => fleetKeyIds.has(k.id)).length,
            dropTarget: ACCOUNT_WIDE_FILTER,
          },
        ]

  return (
    <>
      <OURailAside
        storageKey="credentials-ou-panel-collapsed"
        counts={treeCounts}
        selected={selectedOU}
        onSelect={setFilterOU}
        pseudoRows={pseudoRows}
        dropTypes={dropTypes}
        onDropItem={handleDrop}
        dropHint={dropHint}
        // Accept drops without entering OU edit mode — dragging a key onto a
        // unit is the primary way to attach it here, not an edit-mode action.
        alwaysAcceptDrops={dropTypes.length > 0}
      />
      <AttachKeyToUserDialog
        open={dropKeyId !== null}
        ouId={dropOU}
        accountWide={dropAccountWide}
        keyId={dropKeyId ?? ''}
        keyLabel={droppedKey?.alias || droppedKey?.fingerprint || ''}
        scopeLabel={droppedScopeName}
        onClose={() => setDropKeyId(null)}
        onDone={() => {
          // Stay put — the dialog's own success step is the confirmation; the
          // rail should not jump the operator to the drop target.
          setDropKeyId(null)
          onDropDone()
        }}
      />
    </>
  )
}

type UsersTableProps = {
  scope: AccessScopeSelector
  scopeLabel: string
  // When set, narrows to node-bound entries (scope==='node') regardless of the
  // API scope selector, which has no node kind.
  nodeFilter?: boolean
  // The page-level scope=all set (null while loading). Filtered client-side —
  // the backend scope filters are pure direct-membership, so this is equivalent
  // and avoids a second fetch per scope switch.
  allEntries: AccessEntry[] | null
  totalEntries: number
  onRowClick: (entryId: string) => void
  onAddUser: () => void
  canManage: boolean
}

// Users tab table. Editing is in the details sidebar (row click); adding is in
// the create sidebar. Rows are draggable onto the rail to add a unit membership.
const UsersTable = ({
  scope,
  scopeLabel,
  nodeFilter = false,
  allEntries,
  totalEntries,
  onRowClick,
  onAddUser,
  canManage,
}: UsersTableProps) => {
  const { t } = useTranslation()
  const { ous } = useOUs()

  const assignedLabel = (u: AccessEntry) => {
    if (u.scope === 'ou' && u.ou_ids.length > 0) {
      return u.ou_ids.map((id) => ouName(ous, id)).join(', ')
    }
    if (u.scope === 'account') return t('pages.credentials.accountWide')
    if (u.scope === 'node') return t('pages.credentials.trustedCA.nodeScope')
    return t('pages.credentials.unassigned')
  }

  const users =
    allEntries === null
      ? null
      : allEntries.filter((e) => {
          if (e.type !== 'user') return false
          if (nodeFilter) return e.scope === 'node'
          switch (scope.kind) {
            case 'ou':
              return e.ou_ids.includes(scope.ouId)
            case 'account':
              return e.scope === 'account'
            case 'unassigned':
              return e.scope === 'unassigned'
            default:
              return true
          }
        })

  const columns: Column<AccessEntry>[] = [
    {
      key: 'user',
      header: t('pages.credentials.usersTable.user'),
      alwaysVisible: true,
      sortValue: (u) => u.username ?? '',
      render: (u) => <span className="text-neutral-2">{u.username}</span>,
    },
    {
      key: 'assignedTo',
      header: t('pages.credentials.usersTable.assignedTo'),
      sortValue: (u) => assignedLabel(u),
      render: (u) => <span className="text-neutral-4">{assignedLabel(u)}</span>,
    },
    {
      key: 'keys',
      header: t('pages.credentials.usersTable.keys'),
      sortValue: (u) => u.key_ids.length,
      render: (u) => <span className="text-neutral-4">{u.key_ids.length}</span>,
    },
    {
      key: 'principals',
      header: t('pages.credentials.usersTable.principals'),
      sortValue: (u) => (u.principals ?? []).join(','),
      render: (u) =>
        u.principals && u.principals.length > 0 ? (
          <span className="text-neutral-4">{u.principals.join(', ')}</span>
        ) : (
          <span className="text-neutral-7">—</span>
        ),
    },
    {
      key: 'sudo',
      header: t('pages.credentials.usersTable.sudo'),
      sortValue: (u) => (u.sudo ? 1 : 0),
      render: (u) => (
        <span className="text-neutral-4">
          {u.sudo
            ? t('pages.credentials.usersTable.yes')
            : t('pages.credentials.usersTable.no')}
        </span>
      ),
    },
    {
      key: 'password',
      header: t('pages.credentials.usersTable.password'),
      sortValue: (u) => (u.has_password ? 1 : 0),
      render: (u) => (
        <span className="text-neutral-4">
          {u.has_password ? t('pages.credentials.access.passwordSet') : '—'}
        </span>
      ),
    },
    {
      key: 'status',
      header: t('pages.credentials.usersTable.status'),
      sortValue: (u) => u.lifecycle ?? '',
      render: (u) => (
        <span
          className={
            u.lifecycle === 'locked' ? 'text-status-warning' : 'text-lime-6'
          }
        >
          {u.lifecycle === 'locked'
            ? t('pages.credentials.access.locked')
            : t('pages.credentials.access.active')}
        </span>
      ),
    },
    {
      key: 'created',
      header: t('pages.credentials.usersTable.created'),
      defaultHidden: true,
      sortValue: (u) => u.created_at,
      render: (u) => (
        <span className="whitespace-nowrap text-neutral-5">
          {new Date(u.created_at).toLocaleDateString()}
        </span>
      ),
    },
    {
      key: 'updated',
      header: t('pages.credentials.usersTable.updated'),
      defaultHidden: true,
      sortValue: (u) => u.updated_at,
      render: (u) => (
        <span className="whitespace-nowrap text-neutral-5">
          {new Date(u.updated_at).toLocaleDateString()}
        </span>
      ),
    },
  ]

  if (users === null) {
    return (
      <div className="text-sm text-neutral-6">
        {t('pages.credentials.attach.loading')}
      </div>
    )
  }

  return (
    <DataTable
      storageKey="credentials-users-table"
      columnsLabel={t('pages.credentials.columns')}
      columns={columns}
      rows={users}
      rowKey={(u) => u.id}
      onRowClick={(u) => onRowClick(u.id)}
      rowDraggable={(u) => u.scope !== 'node'}
      onRowDragStart={(u, e) => {
        e.dataTransfer.setData(ACCESS_USER_DND_TYPE, u.id)
        e.dataTransfer.effectAllowed = 'move'
      }}
      empty={
        <div>
          <p className="text-sm text-neutral-6">
            {t('pages.credentials.usersTable.empty', { scope: scopeLabel })}
          </p>
          {totalEntries > 0 ? (
            <p className="mt-2 text-xs text-neutral-7">
              {t('pages.credentials.usersTable.elsewhere')}
            </p>
          ) : null}
          {canManage ? (
            <Button size="sm" className="mt-4" onClick={onAddUser}>
              {t('pages.credentials.addUser')}
            </Button>
          ) : null}
        </div>
      }
    />
  )
}

type KeysTableProps = {
  visibleIds: string[]
  allEntries: AccessEntry[] | null
  selectedKeyId?: string
  onRowClick: (keyId: string) => void
  emptyText: string
}

// SSH key inventory as a table. Rows drag onto the rail to attach the key to a
// user in a unit.
const KeysTable = ({
  visibleIds,
  allEntries,
  selectedKeyId,
  onRowClick,
  emptyText,
}: KeysTableProps) => {
  const { t } = useTranslation()
  const { keys } = useSSHKeys()
  const rows = keys.filter((k) => visibleIds.includes(k.id))

  const userCount = (keyId: string) =>
    (allEntries ?? []).filter(
      (e) => e.type === 'user' && e.key_ids.includes(keyId),
    ).length

  const columns: Column<(typeof rows)[number]>[] = [
    {
      key: 'name',
      header: t('pages.credentials.keysTable.name'),
      alwaysVisible: true,
      sortValue: (k) => k.alias ?? '',
      render: (k) => <span className="text-neutral-2">{k.alias}</span>,
    },
    {
      key: 'fingerprint',
      header: t('pages.credentials.keysTable.fingerprint'),
      sortValue: (k) => k.fingerprint ?? '',
      className: 'font-mono text-xs max-w-[280px] truncate',
      render: (k) => <span className="text-neutral-4">{k.fingerprint}</span>,
    },
    {
      key: 'users',
      header: t('pages.credentials.keysTable.users'),
      sortValue: (k) => userCount(k.id),
      render: (k) => <span className="text-neutral-4">{userCount(k.id)}</span>,
    },
    {
      key: 'created',
      header: t('pages.credentials.keysTable.created'),
      defaultHidden: true,
      sortValue: (k) => k.created_at,
      render: (k) => (
        <span className="whitespace-nowrap text-neutral-5">
          {new Date(k.created_at).toLocaleDateString()}
        </span>
      ),
    },
  ]

  return (
    <DataTable
      storageKey="credentials-keys-table"
      columnsLabel={t('pages.credentials.columns')}
      columns={columns}
      rows={rows}
      rowKey={(k) => k.id}
      isSelected={(k) => k.id === selectedKeyId}
      onRowClick={(k) => onRowClick(k.id)}
      rowDraggable={() => true}
      onRowDragStart={(k, e) => {
        e.dataTransfer.setData(SSHKEY_DND_TYPE, k.id)
        e.dataTransfer.effectAllowed = 'move'
      }}
      empty={emptyText}
    />
  )
}

type CATableProps = KeysTableProps & {
  // The rail scope, so "Trusted on" narrows to the selected unit.
  scopeFilter: AccessScopeSelector
}

// CA inventory as a table: each CA with WHERE it is trusted (an "Assigned to"
// style column that the rail narrows). The "Trusted on" cell is display-only —
// removal (and adding, via drag) lives in the CA key's sidebar, mirroring how a
// user's unit memberships are edited in the user panel. Principals live on
// USERS, so they are not shown here.
const CATable = ({
  visibleIds,
  allEntries,
  selectedKeyId,
  onRowClick,
  scopeFilter,
  emptyText,
}: CATableProps) => {
  const { t } = useTranslation()
  const { keys } = useSSHKeys()
  const { ous } = useOUs()
  const rows = keys.filter((k) => visibleIds.includes(k.id))

  // Every CA trust entry for a key, narrowed to the rail scope when one is set.
  // (No 'unassigned' case: a CA is never unassigned — the drop handler ignores
  // that pseudo-row and removal deletes empty entries.)
  const trustsFor = (keyId: string) => {
    let trusts = (allEntries ?? []).filter(
      (e) => e.type === 'ca' && e.key_ids.includes(keyId),
    )
    if (scopeFilter.kind === 'ou') {
      trusts = trusts.filter((e) => e.ou_ids.includes(scopeFilter.ouId))
    } else if (scopeFilter.kind === 'account') {
      trusts = trusts.filter((e) => e.scope === 'account')
    }
    return trusts
  }

  const caLabels = {
    accountWide: t('pages.credentials.accountWide'),
    node: t('pages.credentials.trustedCA.nodeScope'),
  }
  const placementLabels = (keyId: string) =>
    caPlacements(trustsFor(keyId), ous, caLabels).map((p) => p.label)

  const columns: Column<(typeof rows)[number]>[] = [
    {
      key: 'name',
      header: t('pages.credentials.keysTable.name'),
      alwaysVisible: true,
      sortValue: (k) => k.alias ?? '',
      render: (k) => <span className="text-neutral-2">{k.alias}</span>,
    },
    {
      key: 'fingerprint',
      header: t('pages.credentials.keysTable.fingerprint'),
      defaultHidden: true,
      sortValue: (k) => k.fingerprint ?? '',
      className: 'font-mono text-xs max-w-[240px] truncate',
      render: (k) => <span className="text-neutral-4">{k.fingerprint}</span>,
    },
    {
      key: 'trustedOn',
      header: t('pages.credentials.keysTable.trustedOn'),
      sortValue: (k) => placementLabels(k.id).join(','),
      render: (k) => {
        const placements = caPlacements(trustsFor(k.id), ous, caLabels)
        return placements.length === 0 ? (
          <span className="text-xs text-neutral-7">
            {t('pages.credentials.trustedCA.notTrusted')}
          </span>
        ) : (
          <span className="flex flex-wrap gap-1">
            {placements.map((p) => (
              <span
                key={p.key}
                className="rounded border border-neutral-11 px-1.5 py-0.5 text-xs text-neutral-4"
              >
                {p.label}
              </span>
            ))}
          </span>
        )
      },
    },
    {
      key: 'created',
      header: t('pages.credentials.keysTable.created'),
      defaultHidden: true,
      sortValue: (k) => k.created_at,
      render: (k) => (
        <span className="whitespace-nowrap text-neutral-5">
          {new Date(k.created_at).toLocaleDateString()}
        </span>
      ),
    },
  ]

  return (
    <DataTable
      storageKey="credentials-ca-table"
      columnsLabel={t('pages.credentials.columns')}
      columns={columns}
      rows={rows}
      rowKey={(k) => k.id}
      isSelected={(k) => k.id === selectedKeyId}
      onRowClick={(k) => onRowClick(k.id)}
      rowDraggable={() => true}
      onRowDragStart={(k, e) => {
        e.dataTransfer.setData(SSHKEY_DND_TYPE, k.id)
        e.dataTransfer.effectAllowed = 'move'
      }}
      empty={emptyText}
    />
  )
}
