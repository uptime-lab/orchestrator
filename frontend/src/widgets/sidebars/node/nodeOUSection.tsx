import clsx from 'clsx'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { nodesModel } from '@/entities/node'
import { useNode } from '@/entities/node/hooks.ts'
import { apiOUs, type EffectivePolicyResult, type Flag } from '@/entities/ou'

import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SectionDivider } from './infoRow.tsx'

// Boot behavior is the only node policy that is live/actionable once a node is
// online and adopted. The other resolved policies (Auto-Provision EEPROM,
// provisioning username/password/SSH key, OS image) are install-time
// provisioning defaults that belong to the OU/account config and the Install OS
// flow — showing them here only competed with the live "Effective access"
// section below, so they were removed.
export const NodeOUSection: FC<{ nodeId: string }> = ({ nodeId }) => {
  const { t } = useTranslation()
  const { node } = useNode({ nodeId })
  const [isProcessing, setIsProcessing] = useState(false)
  // The displayed selection is DERIVED from the node in the store; local state
  // holds only the optimistic choice while a save is in flight. A copy kept in
  // useState and synced from the node in an effect painted "Inherit" for one
  // frame on every open, because the effect runs after the first render while
  // the node itself is already in the store.
  const [pendingBoot, setPendingBoot] = useState<Flag | null>(null)
  const [effective, setEffective] = useState<EffectivePolicyResult | null>(null)
  const [bootError, setBootError] = useState<string | null>(null)
  const networkFirst: Flag = node?.network_first ?? 'inherit'
  const bootToOs: Flag = pendingBoot ?? node?.boot_to_os ?? 'inherit'

  // The effective policy is fetched only to resolve what "Inherit" currently
  // maps to. When the node overrides boot_to_os the resolved value simply
  // equals the chosen button, so the inherited highlight is shown only while
  // Inherit is selected.
  //
  // It is fetched again whenever a live node update changes what it resolves
  // from — the node's own setting, its unit, a one-shot recovery boot being
  // set or completed — so the lit option follows the node instead of the
  // moment the panel was opened.
  const storedBoot = node?.boot_to_os
  const ouId = node?.ou_id
  const revertPending = !!node?.recovery_revert_pending
  useEffect(() => {
    let cancelled = false
    apiOUs
      .getEffectivePolicy(nodeId)
      .then((result) => {
        if (!cancelled) {
          setEffective(result)
        }
      })
      .catch(() => {
        if (!cancelled) {
          setEffective(null)
        }
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, storedBoot, ouId, revertPending])

  // Boot target is a segmented control with no Save button: clicking an option
  // applies it immediately. Only boot_to_os travels: the route changes just the
  // flags it is given, so re-sending network_first from this page's copy of
  // the node could only revert a change made elsewhere. On failure we revert the selection to the node's current value so the
  // buttons never show an unsaved state — AND say what happened. Reverting
  // silently was worse than doing nothing: the control springs back with no
  // explanation, on the setting that decides whether the device netboots, so an
  // operator reads it as a UI glitch and clicks again.
  const applyBootTarget = useCallback(
    async (value: Flag) => {
      if (isProcessing || value === bootToOs) return
      setPendingBoot(value)
      setBootError(null)
      try {
        setIsProcessing(true)
        await apiOUs.setNodePolicy(nodeId, { boot_to_os: value })
        // The store carries the saved value once the refetch resolves, so
        // dropping the optimistic choice afterwards cannot flash the old one.
        await nodesModel.fetchNodesFx()
        const result = await apiOUs.getEffectivePolicy(nodeId)
        setEffective(result)
      } catch (err) {
        setBootError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeOUSection.bootTargetError'),
          ),
        )
      } finally {
        // On failure this IS the revert: the derived value falls back to the
        // node's stored setting.
        setPendingBoot(null)
        setIsProcessing(false)
      }
    },
    [nodeId, networkFirst, bootToOs, isProcessing, t],
  )

  // While the node is set to Inherit we do not spell the resolved target out
  // inside the Inherit button; instead we also light up whichever concrete
  // option (Local OS / Recovery) inheritance currently maps to, and tag that
  // button with a small source badge. boot_to_os true = Local OS, false =
  // recovery netboot.
  const resolvedBoot = effective?.boot_to_os
  const resolvedValue: Flag | null =
    bootToOs === 'inherit' && resolvedBoot
      ? resolvedBoot.value
        ? 'on'
        : 'off'
      : null
  const inheritedSource = resolvedBoot?.source

  // Compact badge + human tooltip for where the inherited value comes from.
  const sourceBadge = (s?: string) => {
    switch (s) {
      case 'ou':
        return t('widgets.sidebars.node.nodeOUSection.sourceBadge.ou')
      // The node-policy routes name the parent unit 'root_ou'.
      case 'root':
      case 'root_ou':
        return t('widgets.sidebars.node.nodeOUSection.sourceBadge.root')
      case 'account':
        return t('widgets.sidebars.node.nodeOUSection.sourceBadge.account')
      case 'default':
        return t('widgets.sidebars.node.nodeOUSection.sourceBadge.default')
      default:
        return s
          ? s.toUpperCase()
          : t('widgets.sidebars.node.nodeOUSection.sourceBadge.ou')
    }
  }
  const sourceHuman = (s?: string) => {
    switch (s) {
      case 'ou':
        return t('widgets.sidebars.node.nodeOUSection.sourceHuman.ou')
      // The node-policy routes name the parent unit 'root_ou'.
      case 'root':
      case 'root_ou':
        return t('widgets.sidebars.node.nodeOUSection.sourceHuman.root')
      case 'account':
        return t('widgets.sidebars.node.nodeOUSection.sourceHuman.account')
      case 'default':
        return t('widgets.sidebars.node.nodeOUSection.sourceHuman.default')
      default:
        return t('widgets.sidebars.node.nodeOUSection.sourceHuman.ou')
    }
  }

  // Each boot behavior owns a color: Inherit = orange, Local OS = green (a
  // healthy running OS), Recovery = violet with a glow (matching the RECOVERY
  // status badge). The active option lights up; the rest stay neutral.
  const bootTargetOptions: {
    value: Flag
    label: string
    activeClass: string
  }[] = [
    {
      value: 'inherit',
      label: t('widgets.sidebars.ou.bootTargetShort.inherit'),
      activeClass: 'border-orange-7/50 bg-orange-6/10 text-orange-6',
    },
    {
      value: 'on',
      label: t('widgets.sidebars.ou.bootTargetShort.on'),
      activeClass:
        'border-green-8/50 bg-green-7/10 text-green-7 shadow-[0_0_14px_rgba(64,255,61,0.25)]',
    },
    {
      value: 'off',
      label: t('widgets.sidebars.ou.bootTargetShort.off'),
      activeClass:
        'border-violet-7/50 bg-violet-6/10 text-violet-5 shadow-[0_0_14px_rgba(142,102,255,0.35)]',
    },
  ]

  return (
    <div className="flex flex-col gap-3">
      <SectionDivider label={t('widgets.sidebars.ou.bootBehavior')} />
      {/* Segmented control — selecting an option applies it immediately (no
          Save button). The active option lights up in its own color; while the
          node inherits, the resolved option is lit too and tagged with its
          source. */}
      <div className="flex gap-2">
        {bootTargetOptions.map((opt) => {
          const chosen = bootToOs === opt.value
          // When the node inherits, the concrete option inheritance maps to is
          // lit up too and carries the source badge.
          const inherited = resolvedValue === opt.value
          const active = chosen || inherited
          return (
            <button
              key={opt.value}
              type="button"
              onClick={() => applyBootTarget(opt.value)}
              disabled={isProcessing}
              title={
                inherited
                  ? t('widgets.sidebars.node.nodeOUSection.inheritedFrom', {
                      source: sourceHuman(inheritedSource),
                    })
                  : undefined
              }
              className={clsx(
                'relative flex-1 rounded px-2 py-2.5 text-xs font-medium uppercase tracking-wide border transition-colors disabled:opacity-50 disabled:cursor-not-allowed',
                active
                  ? opt.activeClass
                  : 'border-neutral-11 bg-neutral-13 text-neutral-4 hover:text-neutral-2 hover:border-neutral-9',
              )}
            >
              {opt.label}
              {/* Source badge on the option inheritance currently resolves to
                  (top-right corner); hover explains where it comes from. */}
              {inherited && (
                <span className="absolute top-0.5 right-1 text-[8px] font-semibold leading-none tracking-normal opacity-80">
                  {sourceBadge(inheritedSource)}
                </span>
              )}
            </button>
          )
        })}
      </div>
      {/* A refused change reverts the control, so without this the operator
          sees the buttons spring back and nothing else — on the setting that
          decides whether the device netboots. */}
      {bootError && <div className="text-xs text-red-6">{bootError}</div>}
      {/* Hint follows the selected behavior. A fixed min-height reserves room
          for the tallest hint so switching options never shifts the sections
          below. */}
      <div className="min-h-[2.5rem] text-xs text-neutral-4">
        {t(`widgets.sidebars.ou.bootTargetHint.${bootToOs}`)}
      </div>
    </div>
  )
}
