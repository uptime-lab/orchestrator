import { useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { t } from 'i18next'
import { FC, ReactNode, useCallback, useEffect, useRef, useState } from 'react'

import {
  accessLevelLabel,
  apiAccess,
  lockReasonText,
  type AccessEntry,
  type EffectiveAccessEntry,
} from '@/entities/access'
import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import type { OSUser } from '@/entities/node/api/osUsers.ts'
import { useNode } from '@/entities/node/hooks.ts'
import { apiOUs, type EffectivePolicyResult, type Flag } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { sshKeysModel, useSSHKeys } from '@/entities/sshKey'

import { InfoHint } from '@/shared/ui/infoHint'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SectionDivider } from './infoRow.tsx'

type Props = {
  nodeId: string
}

// One labelled line inside a user/CA card: a fixed narrow label column and a
// value, so every card reads as the same little table.
const Field: FC<{ label: string; children: ReactNode }> = ({
  label,
  children,
}) => (
  <div className="flex gap-2 text-xs">
    <span className="w-20 flex-shrink-0 text-neutral-7">{label}</span>
    <span className="flex-1 min-w-0 text-neutral-4 break-words">
      {children}
    </span>
  </div>
)

// Human phrase for where a resolved policy value came from.
const sourcePhrase = (s?: string): string => {
  switch (s) {
    case 'node':
      return t('widgets.sidebars.node.nodeAccessSection.sourceNode')
    case 'ou':
      return t('widgets.sidebars.node.nodeAccessSection.sourceOU')
    // The node-policy routes name the parent unit 'root_ou'.
    case 'root':
    case 'root_ou':
      return t('widgets.sidebars.node.nodeAccessSection.sourceRoot')
    case 'account':
      return t('widgets.sidebars.node.nodeAccessSection.sourceAccount')
    case 'default':
      return t('widgets.sidebars.node.nodeAccessSection.sourceDefault')
    default:
      return s ?? t('widgets.sidebars.node.nodeAccessSection.sourceDefault')
  }
}

/**
 * Credentials for a node: the sshd password-login policy, then the managed
 * users and trusted CAs grouped by where they come from (account-wide, OU, and
 * this node). Account/OU entries are read-only here (editing them would
 * silently change every node under that scope); node-scoped users can be
 * assigned and unassigned inline — a lightweight, test-oriented path, the main
 * one being OU.
 */
export const NodeAccessSection = ({ nodeId }: Props) => {
  const navigate = useNavigate()
  const { node } = useNode({ nodeId })
  const { keys } = useSSHKeys()
  const [entries, setEntries] = useState<EffectiveAccessEntry[] | null>(null)
  const [unassigned, setUnassigned] = useState<AccessEntry[]>([])
  const [effPolicy, setEffPolicy] = useState<EffectivePolicyResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  // The managed users and CAs are served on access.manage; the password-login
  // policy and the on-device scan are not, so only the entry lists wait on it.
  const canManageAccess = useCan()(Actions.AccessManage)

  const pwd: Flag = node?.ssh_password_auth ?? 'inherit'

  // Key names live in the shared SSH-keys store; refresh it so keys resolve to
  // names instead of raw IDs.
  useEffect(() => {
    sshKeysModel.fetchSSHKeysFx()
  }, [])

  const load = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const [eff, pol] = await Promise.all([
        canManageAccess
          ? apiAccess.getEffectiveAccess(nodeId)
          : Promise.resolve([] as EffectiveAccessEntry[]),
        apiOUs.getEffectivePolicy(nodeId),
      ])
      setEntries(eff)
      setEffPolicy(pol)
    } catch {
      setError(t('widgets.sidebars.node.nodeAccessSection.loadError'))
    } finally {
      setIsLoading(false)
    }
  }, [nodeId, canManageAccess])

  const loadUnassigned = useCallback(async () => {
    if (!canManageAccess) return
    try {
      const list = await apiAccess.getAccessEntries({ kind: 'unassigned' })
      setUnassigned(list.filter((e) => e.type === 'user'))
    } catch {
      // Non-fatal: the assign picker just shows nothing to assign.
    }
  }, [canManageAccess])

  useEffect(() => {
    void load()
    void loadUnassigned()
  }, [load, loadUnassigned])

  const setPasswordPolicy = useCallback(
    async (value: Flag) => {
      if (busy || value === pwd) return
      setBusy(true)
      try {
        // Partial update: the service leaves boot_to_os / network_first
        // untouched when their fields are omitted.
        await apiOUs.setNodePolicy(nodeId, { ssh_password_auth: value })
        await nodesModel.fetchNodesFx()
        const pol = await apiOUs.getEffectivePolicy(nodeId)
        setEffPolicy(pol)
      } catch (err) {
        // Say so. "Keep current state" is indistinguishable from "the click did
        // not register", on a setting that rewrites how the fleet is logged
        // into — and this control is one of the two paths that can dispatch
        // apply_access_config to a device.
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeAccessSection.passwordPolicyError'),
          ),
        )
      } finally {
        setBusy(false)
      }
    },
    [nodeId, pwd, busy],
  )

  const assignUser = useCallback(
    async (entryId: string) => {
      if (!entryId) return
      setBusy(true)
      try {
        await apiAccess.updateAccessEntry(entryId, {
          scope: { node_id: nodeId },
        })
        await Promise.all([load(), loadUnassigned()])
      } catch {
        setError(t('widgets.sidebars.node.nodeAccessSection.assignError'))
      } finally {
        setBusy(false)
      }
    },
    [nodeId, load, loadUnassigned],
  )

  // Manual re-push of the effective config to the device — the companion of
  // the automatic re-apply after recovery, for "the node looks out of sync".
  const [reapplyNote, setReapplyNote] = useState<string | null>(null)
  const reapply = useCallback(async () => {
    if (busy) return
    setBusy(true)
    setReapplyNote(null)
    try {
      const { queued } = await apiAccess.reapplyNodeAccess(nodeId)
      setReapplyNote(
        queued
          ? t('widgets.sidebars.node.nodeAccessSection.reapplyQueued')
          : t('widgets.sidebars.node.nodeAccessSection.reapplySent'),
      )
    } catch (e) {
      const msg =
        (e as { response?: { data?: { message?: string } } })?.response?.data
          ?.message ?? t('widgets.sidebars.node.nodeAccessSection.reapplyError')
      setReapplyNote(msg)
    } finally {
      setBusy(false)
    }
  }, [busy, nodeId])

  // "On the device": a live, read-only snapshot of the real OS accounts, which
  // the managed list deliberately never shows. Two-step because the scan runs
  // as an async command — dispatch, then poll the parsed result.
  const [osUsers, setOsUsers] = useState<OSUser[] | null>(null)
  const [scanState, setScanState] = useState<'idle' | 'scanning' | 'error'>(
    'idle',
  )
  const [scanError, setScanError] = useState<string | null>(null)
  // Cancels an in-flight scan poll on unmount or node switch, so a closed panel
  // does not keep firing requests or render another node's result.
  const scanActive = useRef(true)
  useEffect(() => {
    scanActive.current = true
    return () => {
      scanActive.current = false
    }
  }, [nodeId])
  const scan = useCallback(async () => {
    setScanState('scanning')
    setScanError(null)
    setOsUsers(null)
    try {
      const commandId = await apiNodes.scanOSUsers(nodeId)
      // Poll for up to ~15s; the command carries a 15s device-side timeout.
      const deadline = Date.now() + 20000
      for (;;) {
        await new Promise((r) => setTimeout(r, 1500))
        if (!scanActive.current) return
        const res = await apiNodes.getOSUserScan(nodeId, commandId)
        if (!scanActive.current) return
        if (res.status === 'completed') {
          setOsUsers(res.users ?? [])
          setScanState('idle')
          return
        }
        if (res.status === 'failed') {
          setScanError(
            res.detail ??
              t('widgets.sidebars.node.nodeAccessSection.scanIncomplete'),
          )
          setScanState('error')
          return
        }
        if (Date.now() > deadline) {
          setScanError(t('widgets.sidebars.node.nodeAccessSection.scanTimeout'))
          setScanState('error')
          return
        }
      }
    } catch (e) {
      if (!scanActive.current) return
      const msg =
        (e as { response?: { data?: { message?: string } } })?.response?.data
          ?.message ?? t('widgets.sidebars.node.nodeAccessSection.scanError')
      setScanError(msg)
      setScanState('error')
    }
  }, [nodeId])

  const unassignUser = useCallback(
    async (entryId: string) => {
      setBusy(true)
      try {
        // Empty scope = unassigned (applies to no node) — non-destructive.
        await apiAccess.updateAccessEntry(entryId, { scope: {} })
        await Promise.all([load(), loadUnassigned()])
      } catch {
        setError(t('widgets.sidebars.node.nodeAccessSection.unassignError'))
      } finally {
        setBusy(false)
      }
    },
    [load, loadUnassigned],
  )

  // Never surface a raw UUID: fall back to a short fingerprint tail, then a
  // shortened id, so an unresolved key still reads as "a key" not a number.
  const keyLabel = (keyId: string) => {
    const key = keys.find((k) => k.id === keyId)
    if (key?.alias) return key.alias
    if (key?.fingerprint) {
      const fp = key.fingerprint
      return fp.length > 18 ? `…${fp.slice(-14)}` : fp
    }
    return t('widgets.sidebars.node.nodeAccessSection.keyFallback', {
      id: keyId.slice(0, 8),
    })
  }

  const all = entries ?? []
  const atLevel = (levels: EffectiveAccessEntry['source']['level'][]) =>
    all.filter((e) => levels.includes(e.source.level))
  const account = atLevel(['account'])
  const ou = atLevel(['ou', 'root'])
  const local = atLevel(['node'])

  const renderUser = (entry: EffectiveAccessEntry, removable: boolean) => {
    const lockText = lockReasonText(entry)
    const locked = entry.lifecycle === 'locked'
    return (
      <div key={entry.entry_id} className="flex flex-col gap-1.5 py-2">
        <div className="flex items-center justify-between gap-2">
          <span className="flex items-center gap-2 min-w-0">
            <span className="truncate text-sm">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.nodeAccessSection.userLabel')}{' '}
              </span>
              <span className="text-neutral-2 font-medium">
                {entry.username}
              </span>
            </span>
            {entry.sudo ? (
              <span
                title={t('widgets.sidebars.node.nodeAccessSection.sudoTitle')}
                className="flex-shrink-0 text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-orange-6/15 text-orange-6 cursor-help"
              >
                sudo
              </span>
            ) : null}
            {locked ? (
              <span
                title={lockText ?? undefined}
                className="flex-shrink-0 text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-red-6/15 text-red-6 cursor-help"
              >
                {t('widgets.sidebars.node.nodeAccessSection.lockedBadge')}
              </span>
            ) : null}
          </span>
          {removable ? (
            <button
              type="button"
              onClick={() => unassignUser(entry.source.entry_id)}
              disabled={busy}
              className="flex-shrink-0 text-xs text-neutral-6 hover:text-red-6 disabled:opacity-50"
            >
              {t('widgets.sidebars.node.nodeAccessSection.remove')}
            </button>
          ) : null}
        </div>

        <div className="flex flex-col gap-0.5 pl-4">
          <Field label={t('widgets.sidebars.node.nodeAccessSection.keysLabel')}>
            {entry.key_ids.length > 0 ? (
              entry.key_ids.map((keyId) => keyLabel(keyId)).join(', ')
            ) : (
              <span className="text-neutral-6">
                {t('widgets.sidebars.node.nodeAccessSection.noneUserLocked')}
              </span>
            )}
          </Field>
          <Field
            label={t('widgets.sidebars.node.nodeAccessSection.passwordLabel')}
          >
            {entry.has_password ? (
              t('widgets.sidebars.node.nodeAccessSection.set')
            ) : (
              <span className="text-neutral-6">
                {t('widgets.sidebars.node.nodeAccessSection.notSet')}
              </span>
            )}
          </Field>
          {entry.principals?.length ? (
            <Field
              label={t(
                'widgets.sidebars.node.nodeAccessSection.principalsLabel',
              )}
            >
              {entry.principals.join(', ')}
            </Field>
          ) : null}
          {entry.overridden?.length ? (
            <Field
              label={t(
                'widgets.sidebars.node.nodeAccessSection.overridesLabel',
              )}
            >
              {t('widgets.sidebars.node.nodeAccessSection.shadows', {
                levels: entry.overridden
                  .map((o) => accessLevelLabel(o))
                  .join(', '),
              })}
            </Field>
          ) : null}
        </div>
      </div>
    )
  }

  const renderCA = (ca: EffectiveAccessEntry) => (
    <div key={ca.entry_id} className="flex flex-col gap-1.5 py-2">
      <div className="flex items-center gap-2 min-w-0">
        <span className="truncate text-sm">
          <span className="text-neutral-7">
            {t('widgets.sidebars.node.nodeAccessSection.trustedCALabel')}{' '}
          </span>
          <span className="text-neutral-2 font-medium">
            {ca.key_ids.map((id) => keyLabel(id)).join(', ')}
          </span>
        </span>
      </div>
      <div className="flex flex-col gap-0.5 pl-4">
        <Field
          label={t('widgets.sidebars.node.nodeAccessSection.principalsLabel')}
        >
          {ca.principals?.length ? (
            ca.principals.join(', ')
          ) : (
            <span className="text-neutral-6">
              {t('widgets.sidebars.node.nodeAccessSection.none')}
            </span>
          )}
        </Field>
      </div>
    </div>
  )

  // Render a scope group: its users then its CAs, or an empty note.
  const renderScope = (
    list: EffectiveAccessEntry[],
    empty: string,
    removable: boolean,
  ) => {
    const users = list.filter((e) => e.type === 'user')
    const cas = list.filter((e) => e.type === 'ca')
    if (users.length === 0 && cas.length === 0) {
      return <div className="text-neutral-7 text-xs py-1.5">{empty}</div>
    }
    return (
      <>
        {users.map((u) => renderUser(u, removable))}
        {cas.map((c) => renderCA(c))}
      </>
    )
  }

  // Password-login policy segmented control (Inherit / Allowed / Blocked).
  const pwdOptions: { value: Flag; label: string; activeClass: string }[] = [
    {
      value: 'inherit',
      label: t('widgets.sidebars.node.nodeAccessSection.inherit'),
      activeClass: 'border-orange-7/50 bg-orange-6/10 text-orange-6',
    },
    {
      value: 'on',
      label: t('widgets.sidebars.node.nodeAccessSection.allowed'),
      activeClass: 'border-green-8/50 bg-green-7/10 text-green-7',
    },
    {
      value: 'off',
      label: t('widgets.sidebars.node.nodeAccessSection.blocked'),
      activeClass: 'border-red-7/50 bg-red-6/10 text-red-6',
    },
  ]
  const pwdEff = effPolicy?.ssh_password_auth

  return (
    <div className="flex flex-col gap-6">
      {error ? <div className="text-status-error text-sm">{error}</div> : null}

      {/* Password login policy */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center gap-3">
          <div className="flex-1 min-w-0">
            <SectionDivider
              label={t(
                'widgets.sidebars.node.nodeAccessSection.passwordLoginTitle',
              )}
            />
          </div>
          <InfoHint
            text={t(
              'widgets.sidebars.node.nodeAccessSection.passwordLoginHint',
            )}
          />
          <button
            type="button"
            onClick={() => void load()}
            className="text-neutral-7 hover:text-neutral-2 text-xs flex-shrink-0"
            disabled={isLoading}
          >
            {isLoading
              ? t('widgets.sidebars.node.nodeAccessSection.loading')
              : t('widgets.sidebars.node.nodeAccessSection.refresh')}
          </button>
        </div>
        <div className="flex gap-2">
          {pwdOptions.map((opt) => (
            <button
              key={opt.value}
              type="button"
              onClick={() => setPasswordPolicy(opt.value)}
              disabled={busy}
              className={clsx(
                'flex-1 rounded px-2 py-2 text-xs font-medium uppercase tracking-wide border transition-colors disabled:opacity-50 disabled:cursor-not-allowed',
                pwd === opt.value
                  ? opt.activeClass
                  : 'border-neutral-11 bg-neutral-13 text-neutral-4 hover:text-neutral-2 hover:border-neutral-9',
              )}
            >
              {opt.label}
            </button>
          ))}
        </div>
        <div className="min-h-[2rem] text-xs text-neutral-4">
          {pwdEff ? (
            <>
              {t('widgets.sidebars.node.nodeAccessSection.passwordLoginIs')}{' '}
              <span className="text-neutral-2">
                {pwdEff.value
                  ? t('widgets.sidebars.node.nodeAccessSection.allowedValue')
                  : t('widgets.sidebars.node.nodeAccessSection.blockedValue')}
              </span>{' '}
              <span className="text-neutral-7">
                ({sourcePhrase(pwdEff.source)})
              </span>
              .
            </>
          ) : null}
        </div>
      </div>

      {!canManageAccess && (
        <div className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.nodeAccessSection.noPermission')}
        </div>
      )}

      {/* All nodes (account-wide) */}
      {canManageAccess && (
        <>
          <div className="flex flex-col gap-1">
            <SectionDivider
              label={t('widgets.sidebars.node.nodeAccessSection.allNodesTitle')}
            />
            {renderScope(
              account,
              t('widgets.sidebars.node.nodeAccessSection.emptyAccount'),
              false,
            )}
          </div>

          {/* Organizational unit */}
          <div className="flex flex-col gap-1">
            <SectionDivider
              label={t('widgets.sidebars.node.nodeAccessSection.ouTitle')}
            />
            {renderScope(
              ou,
              t('widgets.sidebars.node.nodeAccessSection.emptyOU'),
              false,
            )}
          </div>

          {/* This node (assignable) */}
          <div className="flex flex-col gap-1">
            <div className="flex items-center gap-3">
              <div className="flex-1 min-w-0">
                <SectionDivider
                  label={t(
                    'widgets.sidebars.node.nodeAccessSection.assignedTitle',
                  )}
                />
              </div>
              <InfoHint
                text={t('widgets.sidebars.node.nodeAccessSection.assignedHint')}
              />
            </div>
            {renderScope(
              local,
              t('widgets.sidebars.node.nodeAccessSection.emptyNode'),
              true,
            )}

            <div className="flex items-center gap-2 pt-2">
              <select
                value=""
                disabled={busy || unassigned.length === 0}
                onChange={(e) => {
                  const id = e.target.value
                  if (id) void assignUser(id)
                }}
                className="flex-1 min-w-0 bg-neutral-13 rounded px-2 py-2 text-xs text-neutral-2 outline-none disabled:opacity-50"
              >
                <option value="">
                  {unassigned.length === 0
                    ? t(
                        'widgets.sidebars.node.nodeAccessSection.noUnassignedUsers',
                      )
                    : t('widgets.sidebars.node.nodeAccessSection.assignUser')}
                </option>
                {unassigned.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.username}
                    {u.sudo ? ' (sudo)' : ''}
                  </option>
                ))}
              </select>
              <button
                type="button"
                onClick={() =>
                  navigate({
                    to: '/credentials',
                    search: { tab: 'users' } as never,
                  })
                }
                className="flex-shrink-0 text-xs uppercase tracking-wide text-neutral-6 hover:text-neutral-2"
              >
                {t('widgets.sidebars.node.nodeAccessSection.newUser')}
              </button>
            </div>
          </div>
        </>
      )}

      {/* On the device: real OS accounts, live read-only snapshot. */}
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-3">
          <div className="flex-1 min-w-0">
            <SectionDivider
              label={t('widgets.sidebars.node.nodeAccessSection.onDeviceTitle')}
            />
          </div>
          <InfoHint
            text={t('widgets.sidebars.node.nodeAccessSection.onDeviceHint')}
          />
          <button
            type="button"
            onClick={() => void scan()}
            disabled={scanState === 'scanning'}
            className="text-neutral-7 hover:text-neutral-2 text-xs flex-shrink-0 disabled:opacity-50"
          >
            {scanState === 'scanning'
              ? t('widgets.sidebars.node.nodeAccessSection.scanning')
              : t('widgets.sidebars.node.nodeAccessSection.scan')}
          </button>
        </div>
        {scanState === 'error' ? (
          <div className="text-status-error text-xs">{scanError}</div>
        ) : null}
        {osUsers === null && scanState !== 'error' ? (
          <div className="text-neutral-7 text-xs">
            {t('widgets.sidebars.node.nodeAccessSection.notScanned')}
          </div>
        ) : null}
        {osUsers !== null && osUsers.length === 0 ? (
          <div className="text-neutral-7 text-xs py-1">
            {t('widgets.sidebars.node.nodeAccessSection.noLoginAccounts')}
          </div>
        ) : null}
        {osUsers?.map((u) => (
          <div
            key={u.username}
            className="flex items-center justify-between gap-2 py-1 text-xs"
          >
            <span className="min-w-0 truncate">
              <span className="text-neutral-2 font-medium">{u.username}</span>
              <span className="text-neutral-7">
                {' '}
                · uid {u.uid} · {u.shell}
              </span>
            </span>
            <span
              className={clsx(
                'flex-shrink-0 text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded',
                u.managed
                  ? 'bg-lime-6/15 text-lime-6'
                  : 'bg-neutral-11 text-neutral-6',
              )}
            >
              {u.managed
                ? t('widgets.sidebars.node.nodeAccessSection.managedBadge')
                : t('widgets.sidebars.node.nodeAccessSection.unmanagedBadge')}
            </span>
          </div>
        ))}
      </div>

      {/* Re-apply: self-heal for "the device is out of sync with this list". */}
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => void reapply()}
            disabled={busy}
            className="flex-shrink-0 rounded border border-neutral-11 bg-neutral-13 px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-neutral-4 hover:text-neutral-2 hover:border-neutral-9 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {t('widgets.sidebars.node.nodeAccessSection.reapplyButton')}
          </button>
          <InfoHint
            text={t('widgets.sidebars.node.nodeAccessSection.reapplyHint')}
          />
          {reapplyNote ? (
            <span className="text-neutral-7 text-xs min-w-0">
              {reapplyNote}
            </span>
          ) : null}
        </div>
      </div>
    </div>
  )
}
