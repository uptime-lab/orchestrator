import { useNavigate } from '@tanstack/react-router'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { OURailAside } from '@/widgets/ouRail/OURailAside.tsx'
import { useOUManagement } from '@/widgets/ouRail/useOUManagement'

import { useNodes } from '@/entities/node/hooks.ts'
import { apiOUs, ousModel, useOUs } from '@/entities/ou'

import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { Input } from '@/shared/ui/input'
import { showError } from '@/shared/ui/toast'

import { AccountDefaults } from './accountDefaults'
import { isUnitTab, UnitDetail, type UnitTab } from './unitDetail'

export { isUnitTab }
export type { UnitTab }

const ACCOUNT_DEFAULTS = 'account-defaults'

const selectClassName =
  'w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none transition-colors'

// The Organization page: the OU rail as navigation, a unit's passport as the
// content (docs/ORGANIZATION-PAGE-PLAN.md). This is the ONE rail that edits
// units — permanently, for `ou.manage` holders: delete, drag-to-reparent and
// reorder on the rows, "+ New OU" under the tree. Selecting a unit shows its
// policies (the Policies tab) and everything it delivers (the Contents tab).
// "Account defaults" is the tree's top pseudo-row — the account policy's home.
export const OrganizationPage = ({
  selectedOU,
  createOpen,
  tab,
}: {
  selectedOU?: string
  createOpen?: boolean
  tab: UnitTab
}) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { ous } = useOUs()
  const { nodes } = useNodes()

  // Create is a two-field dialog (name + parent); configuration happens on
  // the unit's page right after — which retires the drifting create form.
  const [createName, setCreateName] = useState('')
  const [createParent, setCreateParent] = useState('')
  const [creating, setCreating] = useState(false)

  const setCreateOpen = useCallback(
    (open: boolean) => {
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          create: open ? (true as const) : undefined,
        }),
        replace: true,
      })
    },
    [navigate],
  )

  const ouManagement = useOUManagement()
  const railProps = {
    ...ouManagement.railProps,
    onCreate: () => {
      setCreateName('')
      setCreateParent('')
      setCreateOpen(true)
    },
  }

  // The default tab stays out of the URL so a plain `?ou=` link keeps working
  // as it always has.
  const handleTabChange = useCallback(
    (next: UnitTab) => {
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          tab: next === 'policies' ? undefined : next,
        }),
        replace: true,
      })
    },
    [navigate],
  )

  const counts = useMemo(() => {
    const byOU: Record<string, number> = {}
    for (const node of nodes) {
      if (node.ou_id) byOU[node.ou_id] = (byOU[node.ou_id] ?? 0) + 1
    }
    return byOU
  }, [nodes])
  const unassignedCount = useMemo(
    () => nodes.filter((n) => !n.ou_id).length,
    [nodes],
  )

  const handleSelect = useCallback(
    (value: string) => {
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          ou: value === ACCOUNT_DEFAULTS ? undefined : value,
        }),
        replace: true,
      })
    },
    [navigate],
  )

  const handleCreate = useCallback(async () => {
    if (!createName.trim()) {
      showError(t('pages.organization.createNameRequired'), undefined)
      return
    }
    setCreating(true)
    try {
      const created = await apiOUs.postCreateOU({
        name: createName.trim(),
        parent_id: createParent || undefined,
      })
      await ousModel.fetchOUsFx()
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          ou: created.id,
          create: undefined,
        }),
      })
    } catch (err) {
      showError(t('pages.organization.createError'), err)
    } finally {
      setCreating(false)
    }
  }, [createName, createParent, navigate, t])

  const roots = ous.filter((ou) => ou.is_root)

  // Roots in position order, each followed by its children — the flat list a
  // mobile select can render.
  const orderedUnits = useMemo(() => {
    const sorted = [...roots].sort((a, b) => a.position - b.position)
    const out: { id: string; label: string }[] = []
    for (const root of sorted) {
      out.push({ id: root.id, label: root.name })
      ous
        .filter((ou) => ou.parent_id === root.id)
        .sort((a, b) => a.position - b.position)
        .forEach((child) =>
          out.push({ id: child.id, label: `— ${child.name}` }),
        )
    }
    return out
  }, [roots, ous])

  return (
    <div className="h-full w-full flex">
      <OURailAside
        counts={counts}
        selected={selectedOU ?? ACCOUNT_DEFAULTS}
        onSelect={handleSelect}
        treeParent={{
          value: ACCOUNT_DEFAULTS,
          label: t('pages.organization.accountDefaults'),
          count: unassignedCount,
        }}
        dropTypes={[]}
        storageKey="organization-ou-panel-collapsed"
        {...railProps}
      />
      {ouManagement.dialog}

      <div className="flex-1 min-w-0 overflow-y-auto p-4 pt-6 md:p-8">
        {/* Below md the rail is hidden, and on this page it is the only
            navigation — a select stands in for it. */}
        <div className="md:hidden mb-4">
          <select
            value={selectedOU ?? ACCOUNT_DEFAULTS}
            onChange={(e) => handleSelect(e.target.value)}
            className={selectClassName}
            aria-label={t('pages.organization.unitSelectLabel')}
          >
            <option value={ACCOUNT_DEFAULTS}>
              {t('pages.organization.accountDefaults')}
            </option>
            {orderedUnits.map((u) => (
              <option key={u.id} value={u.id}>
                {u.label}
              </option>
            ))}
          </select>
        </div>
        {selectedOU ? (
          <UnitDetail
            key={selectedOU}
            ouId={selectedOU}
            tab={tab}
            onTabChange={handleTabChange}
          />
        ) : (
          <AccountDefaults />
        )}
      </div>

      <ConfirmDialog
        open={!!createOpen}
        title={t('pages.organization.createTitle')}
        message={
          <div className="flex flex-col gap-4 text-left">
            <Input
              type="text"
              name="new_ou_name"
              label={t('pages.organization.nameLabel')}
              value={createName}
              onChange={(e) => setCreateName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') handleCreate()
              }}
              disabled={creating}
            />
            <div className="flex flex-col gap-2">
              <label className="text-sm font-medium text-neutral-7 uppercase">
                {t('pages.organization.parentLabel')}
              </label>
              <select
                value={createParent}
                onChange={(e) => setCreateParent(e.target.value)}
                disabled={creating}
                className={selectClassName}
              >
                <option value="">{t('pages.organization.parentNone')}</option>
                {roots.map((root) => (
                  <option key={root.id} value={root.id}>
                    {root.name}
                  </option>
                ))}
              </select>
              <div className="text-xs text-neutral-4">
                {t('pages.organization.parentHint')}
              </div>
            </div>
          </div>
        }
        confirmText={
          creating
            ? t('pages.organization.creating')
            : t('pages.organization.create')
        }
        busy={creating}
        onConfirm={handleCreate}
        onCancel={() => setCreateOpen(false)}
      />
    </div>
  )
}
