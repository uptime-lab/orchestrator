import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { FC, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  getLEDConfig,
  putLEDConfig,
  resetLEDConfig,
  type LEDConfig,
  type LEDSource,
  type LEDState,
} from '@/entities/node/api/led'
import {
  LED_DEFAULT_BRIGHTNESS,
  LED_V1_SOURCES,
  ledConfigToRows,
  ledRowToSpec,
  ledSourceDefaults,
  ledSpecsInvalidBrightness,
  ledSpecsInvalidRange,
  type LEDRow,
} from '@/entities/node/ui/ledFields'

import { Button } from '@/shared/ui/button/button.tsx'
import { InfoHint } from '@/shared/ui/infoHint'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { InfoRow, SectionDivider } from './infoRow.tsx'

// LED behaviour panel: stealth mode and per-LED ambient indication where the
// board declares them (Compute Blade), ACT/PWR disable on any Pi. Desired vs
// applied, like the watchdog panel — a dispatched converge is not an applied
// one, and a queued one has not reached the device at all.

// The apply_led_config command floor; checked pre-emptively so the user is
// told to update the agent instead of watching a dispatch settle skipped.
const MIN_AGENT_VERSION = '0.6.8'

const meetsMinAgentVersion = (version: string | undefined): boolean => {
  const parse = (v: string): number[] | null => {
    const m = /^v?(\d+)\.(\d+)\.(\d+)/.exec(v.trim())
    return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null
  }
  const have = version ? parse(version) : null
  const want = parse(MIN_AGENT_VERSION)
  if (!have || !want) return false
  for (let i = 0; i < 3; i++) {
    if (have[i] !== want[i]) return have[i] > want[i]
  }
  return true
}

const Toggle: FC<{
  label: string
  hint: string
  checked: boolean
  disabled: boolean
  onChange: (v: boolean) => void
}> = ({ label, hint, checked, disabled, onChange }) => (
  <label
    className={
      'flex items-start gap-3 py-1 ' +
      (disabled ? 'opacity-50' : 'cursor-pointer')
    }
  >
    <input
      type="checkbox"
      checked={checked}
      disabled={disabled}
      onChange={(e) => onChange(e.target.checked)}
      className="mt-0.5 accent-lime-6"
    />
    <span className="flex flex-col">
      <span className="text-sm text-neutral-2">{label}</span>
      <span className="text-xs text-neutral-4">{hint}</span>
    </span>
  </label>
)

// Where an inherited desired config came from. Same four levels and the same
// phrasing as the device-profile panel, so two inheritable blocks do not
// describe the same idea differently.
const ledSourceKey = (source?: string) => {
  switch (source) {
    case 'ou':
      return 'widgets.sidebars.node.ledSection.inheritedOU' as const
    case 'root_ou':
      return 'widgets.sidebars.node.ledSection.inheritedRootOU' as const
    case 'account':
      return 'widgets.sidebars.node.ledSection.inheritedAccount' as const
    default:
      return null
  }
}

const numberInputClassName =
  'w-14 bg-neutral-11 border border-neutral-8 rounded px-1.5 py-0.5 text-xs text-neutral-2'

export const LEDSection: FC<{
  nodeId: string
  canManage: boolean
  online: boolean
  agentVersion?: string
}> = ({ nodeId, canManage, online, agentVersion }) => {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery<LEDState>({
    queryKey: ['node-led', nodeId],
    queryFn: () => getLEDConfig(nodeId),
  })
  const state = query.data

  const [stealth, setStealth] = useState(false)
  const [actOff, setActOff] = useState(false)
  const [pwrOff, setPwrOff] = useState(false)
  const [leds, setLeds] = useState<LEDRow[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    if (!state) return
    setStealth(state.desired?.stealth_mode ?? false)
    setActOff(state.desired?.act_led_off ?? false)
    setPwrOff(state.desired?.pwr_led_off ?? false)
    setLeds(ledConfigToRows(state.desired, state.capabilities.digital_leds))
  }, [state])

  // After a dispatch the device's settle arrives seconds later; the delayed
  // refetch is what turns "dispatched" into "applied on the device" (or
  // surfaces the failed command) without the operator reopening the panel.
  const refetchSoon = () => {
    window.setTimeout(() => {
      void queryClient.invalidateQueries({ queryKey: ['node-led', nodeId] })
    }, 5000)
  }
  const save = useMutation({
    mutationFn: (cfg: LEDConfig) => putLEDConfig(nodeId, cfg),
    onSuccess: (next) => {
      setError('')
      queryClient.setQueryData(['node-led', nodeId], next)
      refetchSoon()
    },
    onError: (e) =>
      setError(
        extractApiErrorMessage(
          e,
          t('widgets.sidebars.node.ledSection.applyError'),
        ),
      ),
  })
  const reset = useMutation({
    mutationFn: () => resetLEDConfig(nodeId),
    onSuccess: (next) => {
      setError('')
      queryClient.setQueryData(['node-led', nodeId], next)
      refetchSoon()
    },
    onError: (e) =>
      setError(
        extractApiErrorMessage(
          e,
          t('widgets.sidebars.node.ledSection.resetError'),
        ),
      ),
  })

  if (query.isLoading) {
    return (
      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.node.ledSection.loading')}
      </div>
    )
  }
  if (!state) {
    return (
      <div className="text-sm text-red-4">
        {t('widgets.sidebars.node.ledSection.loadError')}
      </div>
    )
  }

  // The desired config can come from the OU, the root OU or the account: LED
  // behaviour became an inheritable block, and this panel never read
  // desired_source, so an inherited default was invisible on the node. Worse
  // than invisible — `dirty` compares against the inherited value, so Apply
  // stayed disabled at exactly the moment an operator wanted to override it for
  // one node, and Reset changed nothing because there was no node-level config
  // to clear. The sibling inheritable feature (device profile) renders this
  // correctly and is the wording followed here.
  const inheritedKey = ledSourceKey(state.desired_source)
  const inheritedFrom = inheritedKey ? t(inheritedKey) : null
  const isNodeLevel = !state.desired_source || state.desired_source === 'node'

  const caps = state.capabilities
  const agentOK = meetsMinAgentVersion(agentVersion)
  const editable = canManage && agentOK
  const pending = save.isPending || reset.isPending

  // Offer exactly what the agent advertises (SetLED refuses anything else);
  // absent advertisement means a pre-v2 backend or agent — the v1 pair. A
  // configured source outside the advertisement (an agent downgraded after
  // the config was saved) stays listed so the select does not silently
  // misrender the stored value.
  const supported = caps.supported_sources?.length
    ? caps.supported_sources
    : LED_V1_SOURCES
  const sourceOptions: LEDSource[] = [
    'off',
    ...supported.filter((s) => s !== 'off'),
  ]
  for (const row of leds) {
    if (!sourceOptions.includes(row.source)) sourceOptions.push(row.source)
  }

  // Against the node's OWN config. While the panel is showing an inherited
  // one, every control already equals it, so comparing against it left Apply
  // permanently disabled — the operator could see the inherited values, agree
  // with none of them, and have no way to pin their own.
  const baseline = isNodeLevel ? state.desired : undefined
  const baselineSpecs = ledConfigToRows(baseline, caps.digital_leds).map(
    ledRowToSpec,
  )
  const currentSpecs = leds.map(ledRowToSpec)
  const dirty =
    !isNodeLevel ||
    stealth !== (baseline?.stealth_mode ?? false) ||
    actOff !== (baseline?.act_led_off ?? false) ||
    pwrOff !== (baseline?.pwr_led_off ?? false) ||
    JSON.stringify(currentSpecs) !== JSON.stringify(baselineSpecs)

  const invalidRange = ledSpecsInvalidRange(currentSpecs)
  const invalidBrightness = ledSpecsInvalidBrightness(currentSpecs)
  const invalid = invalidRange || invalidBrightness

  const statusLine = state.queued
    ? t('widgets.sidebars.node.ledSection.statusQueued')
    : state.in_sync
      ? t('widgets.sidebars.node.ledSection.statusApplied')
      : state.desired_hash
        ? t('widgets.sidebars.node.ledSection.statusDispatched')
        : t('widgets.sidebars.node.ledSection.statusNotConfigured')

  const anyOn = leds.some((r) => r.source !== 'off')

  const updateRow = (i: number, patch: Partial<LEDRow>) => {
    setLeds((prev) => {
      const next = [...prev]
      next[i] = { ...next[i], ...patch }
      return next
    })
  }

  return (
    <div className="flex flex-col gap-3">
      {!agentOK && (
        <div className="text-xs text-amber-4 bg-amber-9/20 border border-amber-8 rounded px-3 py-2">
          {t('widgets.sidebars.node.ledSection.needsAgent', {
            version: MIN_AGENT_VERSION,
          })}
        </div>
      )}

      {caps.stealth_available && (
        <Toggle
          label={t('widgets.sidebars.node.ledSection.stealthLabel')}
          hint={t('widgets.sidebars.node.ledSection.stealthHint')}
          checked={stealth}
          disabled={!editable || pending}
          onChange={setStealth}
        />
      )}
      <Toggle
        label={t('widgets.sidebars.node.ledSection.actLabel')}
        hint={t('widgets.sidebars.node.ledSection.actHint')}
        checked={actOff}
        disabled={!editable || pending}
        onChange={setActOff}
      />
      <Toggle
        label={t('widgets.sidebars.node.ledSection.pwrLabel')}
        hint={t('widgets.sidebars.node.ledSection.pwrHint')}
        checked={pwrOff}
        disabled={!editable || pending}
        onChange={setPwrOff}
      />

      {caps.digital_leds > 0 && (
        <>
          <SectionDivider
            label={t('widgets.sidebars.node.ledSection.digitalTitle')}
          />
          {leds.map((row, i) => (
            // Chain position IS the identity here: LED 1 is physically LED 1.
            // eslint-disable-next-line react/no-array-index-key
            <div key={`led-${i}`} className="flex flex-col gap-1">
              <label className="flex items-center justify-between gap-3">
                <span className="text-sm text-neutral-2">
                  {t('widgets.sidebars.node.ledSection.ledN', { index: i + 1 })}
                </span>
                <select
                  value={row.source}
                  disabled={!editable || pending}
                  onChange={(e) =>
                    // The bounds are per source, so a source change resets the
                    // pair to defaults; brightness is source-independent.
                    updateRow(i, {
                      source: e.target.value as LEDSource,
                      low: '',
                      high: '',
                    })
                  }
                  className="bg-neutral-11 border border-neutral-8 rounded px-2 py-1 text-sm text-neutral-2"
                >
                  {sourceOptions.map((s) => (
                    <option key={s} value={s}>
                      {t(`entities.node.ledFields.source.${s}`)}
                    </option>
                  ))}
                </select>
              </label>
              {row.source !== 'off' && (
                <div className="flex items-center gap-1.5 pl-3 text-xs text-neutral-4">
                  <span>{t('entities.node.ledFields.rangeLow')}</span>
                  <input
                    type="number"
                    value={row.low}
                    placeholder={String(ledSourceDefaults(row.source).low)}
                    disabled={!editable || pending}
                    onChange={(e) => updateRow(i, { low: e.target.value })}
                    className={numberInputClassName}
                  />
                  <span>{t('entities.node.ledFields.rangeHigh')}</span>
                  <input
                    type="number"
                    value={row.high}
                    placeholder={String(ledSourceDefaults(row.source).high)}
                    disabled={!editable || pending}
                    onChange={(e) => updateRow(i, { high: e.target.value })}
                    className={numberInputClassName}
                  />
                  <span>
                    {row.source === 'temperature'
                      ? t('entities.node.ledFields.unitCelsius')
                      : t('entities.node.ledFields.unitPercent')}
                  </span>
                  <span className="ml-2">
                    {t('entities.node.ledFields.brightness')}
                  </span>
                  <input
                    type="number"
                    min={1}
                    max={100}
                    value={row.brightness}
                    placeholder={String(LED_DEFAULT_BRIGHTNESS)}
                    disabled={!editable || pending}
                    onChange={(e) =>
                      updateRow(i, { brightness: e.target.value })
                    }
                    className={numberInputClassName}
                  />
                  <span>{t('entities.node.ledFields.unitPercent')}</span>
                </div>
              )}
            </div>
          ))}
          {invalid && (
            <div className="text-xs text-red-4">
              {invalidRange
                ? t('entities.node.ledFields.invalidRange')
                : t('entities.node.ledFields.invalidBrightness')}
            </div>
          )}
          <div className="text-xs text-neutral-5">
            {t('widgets.sidebars.node.ledSection.digitalHint')}
          </div>
          <div className="text-xs text-neutral-5">
            {t('entities.node.ledFields.rangeHint')}
          </div>
        </>
      )}

      <SectionDivider
        label={t('widgets.sidebars.node.ledSection.statusTitle')}
      />
      <InfoRow label={t('widgets.sidebars.node.ledSection.stateLabel')}>
        {statusLine}
      </InfoRow>
      {state.last_command?.status === 'failed' && (
        <div className="text-xs text-red-4">
          {t('widgets.sidebars.node.ledSection.lastApplyFailed', {
            details:
              state.last_command.details ||
              t('widgets.sidebars.node.ledSection.noDetails'),
          })}
        </div>
      )}
      {error && <div className="text-xs text-red-4">{error}</div>}

      <div className="flex gap-2">
        <Button
          disabled={!editable || pending || !dirty || invalid}
          onClick={() =>
            save.mutate({
              stealth_mode: stealth,
              act_led_off: actOff,
              pwr_led_off: pwrOff,
              // v2 rides whenever anything is on; the platform derives the v1
              // projection for older agents. All-off clears with the v1 empty
              // list, which the backend reads as "no indication".
              digital_sources: anyOn ? undefined : [],
              digital_leds: anyOn ? currentSpecs : undefined,
            })
          }
        >
          {t('widgets.sidebars.node.ledSection.apply')}
        </Button>
        <Button
          variant="normal"
          disabled={!editable || pending || !isNodeLevel || !state.desired_hash}
          onClick={() => reset.mutate()}
        >
          {t('widgets.sidebars.node.ledSection.resetToDefaults')}
        </Button>
        <InfoHint text={t('widgets.sidebars.node.ledSection.resetHint')} />
      </div>
      {inheritedFrom && (
        <div className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.ledSection.inheritedNote', {
            source: inheritedFrom,
          })}
        </div>
      )}
      {!online && (
        <div className="text-xs text-neutral-5">
          {t('widgets.sidebars.node.ledSection.offlineNote')}
        </div>
      )}
    </div>
  )
}
