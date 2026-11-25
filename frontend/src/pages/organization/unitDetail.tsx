import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { t } from 'i18next'
import { ReactNode, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { OUPolicyForm, type PolicyFormUpdate } from '@/widgets/ouPolicyForm'
import { SecretsAttachmentSection } from '@/widgets/secretsAttachment'

import { nodesModel } from '@/entities/node'
import {
  apiOUs,
  ousModel,
  useOUs,
  type OUSummary,
  type OUSummaryNode,
} from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { getOUUtilityLadder, getUtilityOUCounts } from '@/entities/utility/api'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { InfoHint } from '@/shared/ui/infoHint'
import { showError, showSuccess, showWarning } from '@/shared/ui/toast'

import { PlatformAccessSection } from './platformAccess'

// The unit page's two tabs (`?tab=`): the policies the unit sets, and what it
// contains and delivers — nodes, shell and platform access, secrets,
// playbooks, utilities. Splitting them keeps the policy form, which is long,
// from pushing the unit's contents below several screens of fields.
export type UnitTab = 'policies' | 'contents'
const UNIT_TABS: UnitTab[] = ['policies', 'contents']
export const isUnitTab = (value: unknown): value is UnitTab =>
  typeof value === 'string' && (UNIT_TABS as string[]).includes(value)

// One summary section: header with the true total and a deep link into the
// page that manages the resource, first-N rows below. Every row carries the
// OU-perspective column (provenance / inheritance / effect) the original page
// cannot show — that is the section's licence to exist
// (docs/ORGANIZATION-PAGE-PLAN.md).
const SectionCard = ({
  title,
  total,
  onOpen,
  openLabel,
  children,
}: {
  title: string
  total?: number
  onOpen?: () => void
  openLabel?: string
  children: ReactNode
}) => (
  <div className="bg-neutral-13 rounded-lg p-5 flex flex-col gap-3">
    <div className="flex items-center justify-between">
      <h2 className="text-sm font-bold uppercase text-neutral-4">
        {title}
        {total !== undefined && (
          <span className="ml-2 text-neutral-6 font-normal">{total}</span>
        )}
      </h2>
      {onOpen && (
        <button
          type="button"
          onClick={onOpen}
          className="text-xs text-lime-6 hover:text-lime-5 uppercase"
        >
          {openLabel ?? t('pages.organization.unit.open')}
        </button>
      )}
    </div>
    {children}
  </div>
)

const EmptyLine = ({ text }: { text: string }) => (
  <div className="text-sm text-neutral-6">{text}</div>
)

// Human names for policy fields and summary sections — backend identifiers
// never reach the UI (COPY-STYLE: no internal jargon).
const policyFieldLabel = (field: string) => {
  const labels: Record<string, string> = {
    network_first: t('pages.organization.unit.policyField.network_first'),
    boot_to_os: t('pages.organization.unit.policyField.boot_to_os'),
    ssh_password_auth: t(
      'pages.organization.unit.policyField.ssh_password_auth',
    ),
    default_username: t('pages.organization.unit.policyField.default_username'),
    default_password: t('pages.organization.unit.policyField.default_password'),
    ssh_key: t('pages.organization.unit.policyField.ssh_key'),
    os_image: t('pages.organization.unit.policyField.os_image'),
    device_profile: t('pages.organization.unit.policyField.device_profile'),
    led_config: t('pages.organization.unit.policyField.led_config'),
    watchdog_config: t('pages.organization.unit.policyField.watchdog_config'),
  }
  return labels[field] ?? field
}
const sectionLabel = (section: string) => {
  const labels: Record<string, string> = {
    nodes: t('pages.organization.unit.section.nodes'),
    access_entries: t('pages.organization.unit.section.access_entries'),
    secrets: t('pages.organization.unit.section.secrets'),
    workflows: t('pages.organization.unit.section.workflows'),
    master_tokens: t('pages.organization.unit.section.master_tokens'),
  }
  return labels[section] ?? section
}

// Compact policy-inheritance pill for a node row.
const nodePolicyLabel = (n: OUSummaryNode): string => {
  if (n.policy_mode === 'overridden') {
    const count = n.overridden_fields?.length ?? 0
    return t('pages.organization.unit.overrides', { count })
  }
  if (n.policy_mode === 'via_child')
    return t('pages.organization.unit.decidedByChild')
  return t('pages.organization.unit.inherits')
}

export const UnitDetail = ({
  ouId,
  tab,
  onTabChange,
}: {
  ouId: string
  tab: UnitTab
  onTabChange: (tab: UnitTab) => void
}) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const can = useCan()
  const { ous } = useOUs()
  const canManage = can(Actions.OUManage) || can(Actions.OUManage, ouId)
  // The attachment routes check the right account-wide; a unit-scoped grant
  // would only be refused.
  const canManageSecrets = can(Actions.SecretManage)
  // The reset clears node-level policy, gated like node-to-unit assignment.
  const canResetNodePolicy =
    // The reset route checks the right account-wide.
    can(Actions.NodeAssignOU)

  const {
    data: summary,
    isLoading,
    error,
  } = useQuery<OUSummary>({
    queryKey: ['ou-summary', ouId],
    queryFn: () => apiOUs.getOUSummary(ouId),
  })
  const { data: utilityCounts } = useQuery({
    queryKey: ['utility-ou-counts'],
    queryFn: getUtilityOUCounts,
    staleTime: 60 * 1000,
  })
  // The rollup behind the Utilities section: per utility, how many of the
  // nodes it reaches have actually applied it. Read separately from the
  // summary, which excludes utilities on purpose.
  const { data: utilityLadder } = useQuery({
    queryKey: ['ou-utility-ladder', ouId],
    queryFn: () => getOUUtilityLadder(ouId),
    staleTime: 30 * 1000,
  })

  const [name, setName] = useState('')
  // Renaming is opt-in behind a pencil: the name used to be a live input
  // styled as the heading, and a stray click plus a keystroke renamed the
  // unit with nothing announcing that the heading was editable.
  const [renaming, setRenaming] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [resetTarget, setResetTarget] = useState<OUSummaryNode | null>(null)
  const [resetting, setResetting] = useState(false)
  const [applyingProfile, setApplyingProfile] = useState(false)
  const [confirmProfileApply, setConfirmProfileApply] = useState(false)
  useEffect(() => {
    if (summary) setName(summary.unit.name)
  }, [summary])

  const refresh = useCallback(async () => {
    await Promise.allSettled([
      queryClient.invalidateQueries({ queryKey: ['ou-summary', ouId] }),
      ousModel.fetchOUsFx(),
    ])
  }, [queryClient, ouId])

  const handleRename = useCallback(async () => {
    setRenaming(false)
    if (!summary) return
    if (!name.trim() || name.trim() === summary.unit.name) {
      setName(summary.unit.name)
      return
    }
    try {
      await apiOUs.putUpdateOU(ouId, { name: name.trim() })
      await refresh()
    } catch (err) {
      setName(summary.unit.name)
      showError(t('pages.organization.unit.renameError'), err)
    }
  }, [summary, name, ouId, refresh, t])

  // The deliberate fleet button (see the backend route): nothing dispatches
  // on an equipage edit, so pushing the hardware profile to the unit's nodes
  // is this explicit confirm, with the outcome named per node.
  const handleApplyProfileToUnit = useCallback(async () => {
    setApplyingProfile(true)
    try {
      const report = await apiOUs.applyDeviceProfileToUnit(ouId)
      const summaryText = t('pages.organization.unit.applySummary', {
        dispatched: report.dispatched,
        queued: report.queued,
      })
      if (report.skipped.length > 0) {
        showWarning(
          t('pages.organization.unit.applySkipped', {
            summary: summaryText,
            count: report.skipped.length,
            details: report.skipped
              .map((skipItem) => `${skipItem.name} — ${skipItem.reason}`)
              .join('; '),
          }),
        )
      } else {
        showSuccess(summaryText)
      }
    } catch (e) {
      showError(t('pages.organization.unit.applyError'), e)
    } finally {
      setApplyingProfile(false)
      setConfirmProfileApply(false)
    }
  }, [ouId, t])

  const handleSavePolicies = useCallback(
    async (update: PolicyFormUpdate) => {
      await apiOUs.putUpdateOU(ouId, update)
      await refresh()
    },
    [ouId, refresh],
  )

  const handleResetNode = useCallback(async () => {
    if (!resetTarget) return
    setResetting(true)
    try {
      await apiOUs.resetNodePolicy(resetTarget.id)
      setResetTarget(null)
      // The node's own record changed; the node list and its panel read it.
      await Promise.all([refresh(), nodesModel.fetchNodesFx()])
    } catch (err) {
      showError(t('pages.organization.unit.resetError'), err)
    } finally {
      setResetting(false)
    }
  }, [resetTarget, refresh, t])

  const handleDelete = useCallback(async () => {
    setDeleteOpen(false)
    try {
      await apiOUs.deleteOU(ouId)
      await ousModel.fetchOUsFx()
      navigate({
        to: '/organization',
        search: {
          ou: undefined,
          create: undefined,
          tab: undefined,
          sidebar: undefined,
        },
        replace: true,
      })
    } catch (err) {
      // The server names the guard that refused (children, nodes, playbooks,
      // utilities) — that sentence is the operator's next step.
      showError(t('pages.organization.unit.deleteError'), err)
    }
  }, [ouId, navigate, t])

  if (isLoading) {
    return (
      <div className="p-8 text-neutral-4">
        {t('pages.organization.unit.loading')}
      </div>
    )
  }
  if (error || !summary) {
    return (
      <div className="p-8 text-neutral-4">
        {t('pages.organization.unit.loadError')}
      </div>
    )
  }

  const { unit, counts } = summary
  const parent = unit.parent_id
    ? ous.find((u) => u.id === unit.parent_id)
    : null
  const utilitiesCount = utilityCounts?.[ouId] ?? 0

  return (
    <div className="flex flex-col gap-5 max-w-5xl">
      {/* Header: identity + delete. */}
      <div className="flex items-start justify-between gap-4">
        <div className="flex flex-col gap-1 min-w-0">
          <div className="flex items-center gap-2 min-w-0">
            {renaming ? (
              <input
                type="text"
                value={name}
                autoFocus
                onChange={(e) => setName(e.target.value)}
                onBlur={handleRename}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
                  if (e.key === 'Escape') {
                    // Reset BEFORE blur so the blur's save sees the stored
                    // name and does nothing.
                    setName(summary.unit.name)
                    setRenaming(false)
                  }
                }}
                className="bg-transparent text-lg font-bold uppercase text-neutral-1 outline-none border-b border-lime-6 min-w-0"
                aria-label={t('pages.organization.unit.nameLabel')}
              />
            ) : (
              <>
                <h1 className="text-lg font-bold uppercase text-neutral-1 truncate">
                  {unit.name}
                </h1>
                {canManage && (
                  <button
                    type="button"
                    onClick={() => {
                      setName(unit.name)
                      setRenaming(true)
                    }}
                    className="p-1 text-neutral-6 hover:text-neutral-2 transition-colors flex-shrink-0"
                    aria-label={t('pages.organization.unit.renameButton')}
                    title={t('pages.organization.unit.renameButton')}
                  >
                    <svg
                      className="w-3.5 h-3.5"
                      viewBox="0 0 20 20"
                      fill="currentColor"
                    >
                      <path d="M13.586 3.586a2 2 0 112.828 2.828l-8.5 8.5a1 1 0 01-.44.263l-3 1a1 1 0 01-1.265-1.265l1-3a1 1 0 01.263-.44l8.5-8.5z" />
                    </svg>
                  </button>
                )}
              </>
            )}
          </div>
          <div className="text-xs text-neutral-5">
            {unit.is_root
              ? counts.children > 0
                ? t('pages.organization.unit.topLevelWithChildren', {
                    count: counts.children,
                  })
                : t('pages.organization.unit.topLevel')
              : t('pages.organization.unit.nestedUnder', {
                  name: parent?.name ?? '—',
                })}
            {counts.master_tokens > 0 && (
              <>
                {' '}
                ·{' '}
                {t('pages.organization.unit.masterTokens', {
                  count: counts.master_tokens,
                })}
              </>
            )}
          </div>
        </div>
        {canManage && (
          <Button
            size="md"
            variant="destructive"
            onClick={() => setDeleteOpen(true)}
          >
            {t('pages.organization.unit.deleteButton')}
          </Button>
        )}
      </div>

      {/* Tabs — the same bar as the Secrets and Operations pages. */}
      <div className="flex gap-1 border-b border-neutral-12">
        {UNIT_TABS.map((item) => (
          <button
            key={item}
            type="button"
            onClick={() => onTabChange(item)}
            className={clsx(
              'font-mono text-sm px-3 py-2 border-b-2 -mb-px transition-colors',
              tab === item
                ? 'text-lime-6 border-lime-6'
                : 'text-neutral-4 hover:text-neutral-2 border-transparent',
            )}
          >
            {t(`pages.organization.unit.tabs.${item}`)}
          </button>
        ))}
      </div>

      {tab === 'policies' && (
        <>
          {/* Policies — the primary sections; the form renders its own titled
              cards (Boot / SSH access / Provisioning defaults / Hardware). */}
          <OUPolicyForm
            stored={unit}
            effective={summary.effective_policy}
            effectiveOSImageName={summary.effective_os_image_name}
            effectiveDeviceProfile={summary.effective_device_profile}
            effectiveDeviceProfileSource={
              summary.effective_device_profile_source
            }
            passwordAuthImpact={summary.password_auth_impact}
            disabled={!canManage}
            onSave={handleSavePolicies}
            // The deliberate fleet button for the hardware profile, inside
            // the Hardware card. The apply reaches the unit's own members
            // only, so that is the count, and the button is rendered only
            // when there is at least one: dispatching to zero teaches nothing.
            hardwareActions={
              canManage && (summary.counts?.nodes ?? 0) > 0 ? (
                <Button
                  size="sm"
                  disabled={applyingProfile}
                  onClick={() => setConfirmProfileApply(true)}
                >
                  {applyingProfile
                    ? t('pages.organization.unit.applyingProfile')
                    : t('pages.organization.unit.applyProfile', {
                        count: summary.counts?.nodes ?? 0,
                      })}
                </Button>
              ) : undefined
            }
          />
        </>
      )}

      {tab === 'contents' && (
        <>
          {/* Nodes the unit's policies reach, with inheritance status. */}
          {summary.nodes && (
            <SectionCard
              title={t('pages.organization.unit.nodesTitle')}
              total={summary.nodes.total}
              onOpen={() =>
                navigate({ to: '/', search: { ou: ouId, sidebar: undefined } })
              }
              openLabel={t('pages.organization.unit.openInNodes')}
            >
              {summary.nodes.total === 0 ? (
                <EmptyLine text={t('pages.organization.unit.nodesEmpty')} />
              ) : (
                <div className="flex flex-col divide-y divide-neutral-12">
                  {summary.nodes.items.map((n) => (
                    <div
                      key={n.id}
                      className="flex items-center gap-3 py-2 text-sm"
                    >
                      <span className="text-neutral-2 truncate">{n.name}</span>
                      <span className="text-xs text-neutral-6">{n.status}</span>
                      {n.via_child_ou && (
                        <span className="text-xs text-neutral-6">
                          {t('pages.organization.unit.viaChild', {
                            name:
                              ous.find((u) => u.id === n.via_child_ou)?.name ??
                              t('pages.organization.unit.childFallback'),
                          })}
                        </span>
                      )}
                      <span
                        className={clsx(
                          'ml-auto text-xs',
                          n.policy_mode === 'inherits'
                            ? 'text-neutral-5'
                            : 'text-yellow-5',
                        )}
                      >
                        {nodePolicyLabel(n)}
                      </span>
                      {n.policy_mode === 'overridden' &&
                        (n.overridden_fields?.length ?? 0) > 0 && (
                          <span className="whitespace-nowrap">
                            <InfoHint
                              className="ml-1"
                              text={t(
                                'pages.organization.unit.overriddenHint',
                                {
                                  fields: n
                                    .overridden_fields!.map(policyFieldLabel)
                                    .join(', '),
                                },
                              )}
                            />
                          </span>
                        )}
                      {canResetNodePolicy && n.policy_mode === 'overridden' && (
                        <button
                          type="button"
                          onClick={() => setResetTarget(n)}
                          className="text-xs text-neutral-5 hover:text-neutral-2 uppercase whitespace-nowrap"
                        >
                          {t('pages.organization.unit.resetToInherited')}
                        </button>
                      )}
                    </div>
                  ))}
                  {summary.nodes.total > summary.nodes.items.length && (
                    <div className="pt-2 text-xs text-neutral-6">
                      {t('pages.organization.unit.andMore', {
                        count: summary.nodes.total - summary.nodes.items.length,
                      })}
                    </div>
                  )}
                </div>
              )}
            </SectionCard>
          )}

          {/* Shell access reaching this unit, with the level it comes from. */}
          {summary.access_entries && (
            <SectionCard
              title={t('pages.organization.unit.accessTitle')}
              total={summary.access_entries.total}
              onOpen={() =>
                navigate({
                  to: '/credentials',
                  search: { tab: 'users', ou: ouId, sidebar: undefined },
                })
              }
              openLabel={t('pages.organization.unit.openInCredentials')}
            >
              {summary.access_entries.total === 0 ? (
                <EmptyLine text={t('pages.organization.unit.accessEmpty')} />
              ) : (
                <div className="flex flex-col divide-y divide-neutral-12">
                  {summary.access_entries.items.map((e) => (
                    <div
                      key={e.id}
                      className="flex items-center gap-3 py-2 text-sm"
                    >
                      <span className="text-neutral-2 truncate">
                        {e.type === 'ca'
                          ? t('pages.organization.unit.caTrust')
                          : e.username}
                      </span>
                      <span className="text-xs text-neutral-6">
                        {t('pages.organization.unit.keyCount', {
                          count: e.key_count,
                        })}
                        {e.sudo ? t('pages.organization.unit.sudoSuffix') : ''}
                      </span>
                      <span className="ml-auto flex items-center gap-2 text-xs">
                        {e.shadowed && (
                          <span className="text-yellow-5 whitespace-nowrap">
                            {t('pages.organization.unit.shadowed')}
                            <InfoHint
                              className="ml-1"
                              text={t('pages.organization.unit.shadowedHint')}
                            />
                          </span>
                        )}
                        <span className="text-neutral-5">
                          {t('pages.organization.unit.from', {
                            level:
                              e.effective_from === 'unit'
                                ? t('pages.organization.unit.levelUnit')
                                : e.effective_from === 'parent'
                                  ? t('pages.organization.unit.levelParent')
                                  : t('pages.organization.unit.levelAccount'),
                          })}
                        </span>
                      </span>
                    </div>
                  ))}
                  {summary.access_entries.total >
                    summary.access_entries.items.length && (
                    <div className="pt-2 text-xs text-neutral-6">
                      {t('pages.organization.unit.andMore', {
                        count:
                          summary.access_entries.total -
                          summary.access_entries.items.length,
                      })}
                    </div>
                  )}
                </div>
              )}
            </SectionCard>
          )}

          {/* Platform roles scoped to this unit (or inherited from its parent),
          with grant/revoke for member managers. */}
          <SectionCard title={t('pages.organization.unit.platformAccessTitle')}>
            <PlatformAccessSection ouId={ouId} parentId={unit.parent_id} />
          </SectionCard>

          {/* Secrets delivered at this unit, with provenance — plus management
          (attach/detach), which is the secrets grant, not the OU grant. */}
          {summary.secrets && (
            <SectionCard
              title={t('pages.organization.unit.secretsTitle')}
              total={summary.secrets.total}
              onOpen={() =>
                navigate({
                  to: '/secrets',
                  search: { tab: 'secrets', ou: ouId, sidebar: undefined },
                })
              }
              openLabel={t('pages.organization.unit.openInSecrets')}
            >
              <SecretsAttachmentSection
                scope={{ kind: 'ou', ouId }}
                canManage={canManageSecrets}
              />
            </SectionCard>
          )}

          {/* Playbooks scoped to this unit, with scope width. */}
          {summary.workflows && (
            <SectionCard
              title={t('pages.organization.unit.playbooksTitle')}
              total={summary.workflows.total}
              onOpen={() =>
                navigate({
                  to: '/workflows',
                  search: {
                    tab: 'templates',
                    ou: ouId,
                    sidebar: undefined,
                    templateId: undefined,
                    executionId: undefined,
                  },
                })
              }
              openLabel={t('pages.organization.unit.openInOperations')}
            >
              {summary.workflows.total === 0 ? (
                <EmptyLine text={t('pages.organization.unit.playbooksEmpty')} />
              ) : (
                <div className="flex flex-col divide-y divide-neutral-12">
                  {summary.workflows.items.map((w) => (
                    <div
                      key={w.id}
                      className="flex items-center gap-3 py-2 text-sm"
                    >
                      <span className="text-neutral-2 truncate">{w.name}</span>
                      <span className="text-xs text-neutral-6">{w.status}</span>
                      <span className="ml-auto text-xs text-neutral-5">
                        {w.scope_ou_count === 1
                          ? t('pages.organization.unit.scopeOnlyThisUnit')
                          : t('pages.organization.unit.scopeUnits', {
                              count: w.scope_ou_count,
                            })}
                      </span>
                    </div>
                  ))}
                  {summary.workflows.total > summary.workflows.items.length && (
                    <div className="pt-2 text-xs text-neutral-6">
                      {t('pages.organization.unit.andMore', {
                        count:
                          summary.workflows.total -
                          summary.workflows.items.length,
                      })}
                    </div>
                  )}
                </div>
              )}
            </SectionCard>
          )}

          {/* Utilities: a real section since the per-unit rollup endpoint exists.
          "of N nodes" is how many nodes the utility RESOLVES to, not the unit's
          size — a nearer assignment on a node overrides the unit's, and a child
          unit can resolve a different set. */}
          <SectionCard
            title={t('pages.organization.unit.utilitiesTitle')}
            total={utilitiesCount}
            onOpen={() =>
              navigate({
                to: '/utilities',
                search: { tab: 'assignments', ou: ouId, sidebar: undefined },
              })
            }
            openLabel={t('pages.organization.unit.openInUtilities')}
          >
            {utilitiesCount === 0 ? (
              <EmptyLine text={t('pages.organization.unit.utilitiesEmpty')} />
            ) : !utilityLadder ? (
              <div className="text-sm text-neutral-5">
                {t('pages.organization.unit.loading')}
              </div>
            ) : utilityLadder.length === 0 ? (
              // An assignment can exist while reaching no node of this unit — an
              // empty unit, or one whose nodes all take a nearer assignment.
              <div className="text-sm text-neutral-4">
                {t('pages.organization.unit.utilitiesNoReach', {
                  count: utilitiesCount,
                })}
              </div>
            ) : (
              <div className="flex flex-col gap-1">
                {utilityLadder.map((row) => (
                  <div
                    key={row.utility_id}
                    className="flex items-baseline gap-3 text-sm"
                  >
                    <span className="text-neutral-3 flex-1 truncate">
                      {row.name || row.utility_id}
                    </span>
                    <span className="text-neutral-5">
                      {t('pages.organization.unit.appliedOn', {
                        applied: row.applied,
                        nodes: row.nodes,
                      })}
                    </span>
                    {row.update_pending > 0 && (
                      <span className="text-yellow-6 text-xs">
                        {t('pages.organization.unit.pending', {
                          count: row.update_pending,
                        })}
                      </span>
                    )}
                    {row.failed > 0 && (
                      <span className="text-red-6 text-xs">
                        {t('pages.organization.unit.failed', {
                          count: row.failed,
                        })}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            )}
          </SectionCard>

          {summary.omitted && summary.omitted.length > 0 && (
            <div className="text-xs text-neutral-6">
              {t('pages.organization.unit.hiddenForRole', {
                sections: summary.omitted.map(sectionLabel).join(', '),
              })}
            </div>
          )}
        </>
      )}

      <ConfirmDialog
        open={confirmProfileApply}
        title={t('pages.organization.unit.applyTitle')}
        message={
          t('pages.organization.unit.applyMessage', {
            count: summary?.counts?.nodes ?? 0,
          }) +
          ((summary?.counts?.child_nodes ?? 0) > 0
            ? ' ' +
              t('pages.organization.unit.applyNestedNote', {
                count: summary?.counts?.child_nodes ?? 0,
              })
            : '')
        }
        confirmText={
          applyingProfile
            ? t('pages.organization.unit.applying')
            : t('pages.organization.unit.apply')
        }
        busy={applyingProfile}
        onConfirm={handleApplyProfileToUnit}
        onCancel={() => setConfirmProfileApply(false)}
      />
      <ConfirmDialog
        open={!!resetTarget}
        title={t('pages.organization.unit.resetTitle')}
        message={t('pages.organization.unit.resetMessage', {
          name: resetTarget?.name,
          fields:
            (resetTarget?.overridden_fields ?? [])
              .map(policyFieldLabel)
              .join(', ') || t('pages.organization.unit.resetFieldsFallback'),
        })}
        confirmText={
          resetting
            ? t('pages.organization.unit.resetting')
            : t('pages.organization.unit.reset')
        }
        busy={resetting}
        onConfirm={handleResetNode}
        onCancel={() => setResetTarget(null)}
      />
      <ConfirmDialog
        open={deleteOpen}
        title={t('pages.organization.unit.deleteTitle')}
        message={t('pages.organization.unit.deleteMessage', {
          name: unit.name,
        })}
        confirmText={t('pages.organization.unit.deleteConfirm')}
        destructive
        onConfirm={handleDelete}
        onCancel={() => setDeleteOpen(false)}
      />
    </div>
  )
}
