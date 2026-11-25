import clsx from 'clsx'
import { FC, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiNodes } from '@/entities/node/api'
import type { OverclockPreset } from '@/entities/node/api/overclock.ts'
import { fetchNodeCommandsFx } from '@/entities/node/model'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { overclockButtonState } from './overclockState'
import type { OverclockInfo } from './overviewData.ts'
import { SubViewHeader } from './subViewHeader.tsx'

// OverclockControl is the Advanced-tab sub-view for applying a per-board
// overclock preset to /boot/firmware/config.txt (via the backend's
// patch_file-based /overclock endpoint). Two hard rules, both deliberate:
//   - NEVER auto-reboot: the preset only takes effect on the next boot, and
//     the operator chooses when that happens;
//   - NEVER auto-apply: applying is an explicit click on an explicit preset,
//     with the boot-risk warning always visible.
// "Stock" is the revert path — it clears the managed overclock block.
export const OverclockControl: FC<{
  nodeId: string
  // Current detection from config.txt (useOverclock); null while loading or
  // when config files are unavailable.
  overclock: OverclockInfo | null
  online: boolean
  // A command is running on the node; Apply waits until it settles.
  nodeBusy: boolean
  // Whether the viewer may write node files (node.file_write) — gates Apply.
  canApply: boolean
  onBack: () => void
}> = ({ nodeId, overclock, online, nodeBusy, canApply, onBack }) => {
  const { t } = useTranslation()
  const [presets, setPresets] = useState<OverclockPreset[] | null>(null)
  const [board, setBoard] = useState<string>('')
  const [loadError, setLoadError] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [applied, setApplied] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    apiNodes
      .getOverclockPresets(nodeId)
      .then((res) => {
        if (!alive) return
        setBoard(res.board)
        setPresets(res.presets)
      })
      .catch(() => {
        if (alive) setLoadError(true)
      })
    return () => {
      alive = false
    }
  }, [nodeId])

  const handleApply = async () => {
    if (!selected) return
    setBusy(true)
    setError(null)
    setApplied(null)
    try {
      await apiNodes.applyOverclockPreset(nodeId, selected)
      await fetchNodeCommandsFx(nodeId)
      setApplied(selected)
    } catch (err) {
      // The server's reason (a watcher node, an invalid key) is the
      // diagnostic; the generic line is only the fallback.
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.overclockControl.sendError'),
        ),
      )
    } finally {
      setBusy(false)
    }
  }

  const selectedPreset = presets?.find((p) => p.name === selected)
  const isRevert = selected === 'stock'
  const buttonState = overclockButtonState({
    selected,
    applied,
    sending: busy,
    nodeBusy,
  })

  return (
    <div className="flex flex-col gap-4">
      <SubViewHeader
        title={t('widgets.sidebars.node.overclockControl.title')}
        onBack={onBack}
      />

      {/* Current state, from the same detection that drives the OC badge. */}
      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.node.overclockControl.current')}{' '}
        {overclock === null ? (
          <span className="text-neutral-6">
            {t('widgets.sidebars.node.overclockControl.unknown')}
          </span>
        ) : overclock.active ? (
          <span
            className="font-mono text-amber-6"
            title={overclock.settings.join(', ')}
          >
            {t('widgets.sidebars.node.overclockControl.overclocked', {
              settings: overclock.settings.join(', '),
            })}
          </span>
        ) : (
          <span className="text-lime-6">
            {t('widgets.sidebars.node.overclockControl.stockClocks')}
          </span>
        )}
      </div>

      {loadError && (
        <div className="text-xs text-red-5">
          {t('widgets.sidebars.node.overclockControl.loadError')}
        </div>
      )}

      {presets !== null && presets.length === 0 && !loadError && (
        <div className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.overclockControl.noPresets')}
        </div>
      )}

      {presets !== null && presets.length > 0 && (
        <>
          {board && (
            <div className="text-xs text-neutral-6">
              {t('widgets.sidebars.node.overclockControl.presetsForBoard')}{' '}
              <span className="font-mono text-neutral-4">{board}</span>
            </div>
          )}
          <div className="flex flex-col gap-2">
            {presets.map((p) => (
              <button
                key={p.name}
                type="button"
                onClick={() => {
                  setSelected(p.name)
                  setApplied(null)
                  setError(null)
                }}
                className={clsx(
                  'flex flex-col gap-1 rounded border px-3 py-2 text-left transition-colors',
                  selected === p.name
                    ? 'border-lime-8 bg-lime-6/10'
                    : 'border-neutral-11 hover:border-neutral-9',
                )}
              >
                <div className="flex items-center gap-2">
                  <span className="text-sm text-neutral-2">{p.label}</span>
                  {p.name === 'stock' && (
                    <span className="text-xxs uppercase tracking-wide text-neutral-6">
                      {t('widgets.sidebars.node.overclockControl.revert')}
                    </span>
                  )}
                </div>
                <span className="text-xs text-neutral-6">{p.description}</span>
                {p.directives.length > 0 && (
                  <span className="font-mono text-xxs text-neutral-5">
                    {p.directives.join('  ')}
                  </span>
                )}
              </button>
            ))}
          </div>

          {canApply ? (
            <Button
              size="md"
              onClick={handleApply}
              disabled={buttonState !== 'apply'}
              title={
                buttonState === 'nodeBusy'
                  ? t('widgets.sidebars.node.overclockControl.nodeBusy')
                  : undefined
              }
              grow
            >
              {buttonState === 'sending'
                ? t('widgets.sidebars.node.overclockControl.sending')
                : buttonState === 'sent'
                  ? t('widgets.sidebars.node.overclockControl.sent')
                  : isRevert
                    ? t('widgets.sidebars.node.overclockControl.revertToStock')
                    : selectedPreset
                      ? t('widgets.sidebars.node.overclockControl.applyNamed', {
                          preset: selectedPreset.label.toLowerCase(),
                        })
                      : t('widgets.sidebars.node.overclockControl.applyPreset')}
            </Button>
          ) : (
            <div className="text-xs text-neutral-6">
              {t('widgets.sidebars.node.overclockControl.noPermission')}
            </div>
          )}

          {error && <div className="text-xs text-red-5">{error}</div>}

          {applied && !error && (
            <div className="rounded border border-lime-6/30 bg-lime-6/10 px-3 py-2 text-xs text-lime-6">
              {t('widgets.sidebars.node.overclockControl.appliedBefore')}{' '}
              <span className="font-medium">
                {t('widgets.sidebars.node.overclockControl.appliedReboot')}
              </span>
              {t('widgets.sidebars.node.overclockControl.appliedAfter')}
              {!online &&
                t('widgets.sidebars.node.overclockControl.appliedOffline')}
            </div>
          )}

          {/* Boot-risk warning: always visible, not just at apply time. */}
          <div className="rounded border border-amber-6/40 bg-amber-6/10 px-3 py-2 text-xs text-neutral-4">
            {t('widgets.sidebars.node.overclockControl.risk1')}{' '}
            <span className="text-neutral-2">
              {t('widgets.sidebars.node.overclockControl.riskReboot')}
            </span>
            {t('widgets.sidebars.node.overclockControl.risk2')}{' '}
            <span className="text-neutral-2">
              {t('widgets.sidebars.node.overclockControl.riskFailsafe')}
            </span>
            {t('widgets.sidebars.node.overclockControl.risk3')}{' '}
            <span className="text-neutral-2">
              {t('widgets.sidebars.node.overclockControl.riskOffByDefault')}
            </span>
            {t('widgets.sidebars.node.overclockControl.risk4')}{' '}
            <span className="text-neutral-2">
              {t('widgets.sidebars.node.overclockControl.riskStock')}
            </span>
            {t('widgets.sidebars.node.overclockControl.risk5')}
          </div>
        </>
      )}
    </div>
  )
}
