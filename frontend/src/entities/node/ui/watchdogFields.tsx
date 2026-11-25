import { t } from 'i18next'
import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import type { WatchdogConfig } from '@/entities/node/api/watchdog'

// The controlled watchdog field grid, shared by the node sidebar's custom
// editor and the OU/account policy form. It deliberately does NOT
// re-implement validation: every bound lives in the backend domain
// (WatchdogConfig.Validate) and the device runs its own pre-arm checks on
// top; a second copy here would drift and start refusing configurations the
// platform accepts. The numeric inputs carry the bounds as min/max hints so
// the operator sees the range, and a rejected save surfaces the server's own
// wording.

export const NUMBER_FIELDS = {
  watchdog_timeout_sec: { min: 5, max: 15 },
  agent_change_sec: { min: 120, max: 3600 },
  ping_count: { min: 1, max: 10 },
  interval_sec: { min: 1, max: 60 },
  retry_timeout_sec: { min: 30, max: 600 },
  max_load_1: { min: 0, max: 100 },
  max_load_5: { min: 0, max: 100 },
  max_load_15: { min: 0, max: 100 },
  max_temperature_c: { min: 0, max: 110 },
  min_memory_pages: { min: 0, max: 131072 },
} as const

export type NumberField = keyof typeof NUMBER_FIELDS

// The field labels, resolved lazily so the locale is read at render rather
// than at module load.
const numberFieldLabel = (field: NumberField): string => {
  const labels: Record<NumberField, string> = {
    watchdog_timeout_sec: t(
      'entities.node.watchdogFields.field.watchdog_timeout_sec',
    ),
    agent_change_sec: t('entities.node.watchdogFields.field.agent_change_sec'),
    ping_count: t('entities.node.watchdogFields.field.ping_count'),
    interval_sec: t('entities.node.watchdogFields.field.interval_sec'),
    retry_timeout_sec: t(
      'entities.node.watchdogFields.field.retry_timeout_sec',
    ),
    max_load_1: t('entities.node.watchdogFields.field.max_load_1'),
    max_load_5: t('entities.node.watchdogFields.field.max_load_5'),
    max_load_15: t('entities.node.watchdogFields.field.max_load_15'),
    max_temperature_c: t(
      'entities.node.watchdogFields.field.max_temperature_c',
    ),
    min_memory_pages: t('entities.node.watchdogFields.field.min_memory_pages'),
  }
  return labels[field]
}

const inputClass =
  'w-28 rounded border border-neutral-11 bg-neutral-13 px-2 py-1 font-mono text-xs text-neutral-2'

// A watchdog with nothing switched on is what the platform means by "not
// armed", so editors start from the safest supervision rather than an empty
// config the server would refuse.
export const DEFAULT_WATCHDOG_CONFIG: WatchdogConfig = {
  enabled: true,
  os_hang: false,
  watchdog_timeout_sec: 15,
  network: false,
  agent_health: true,
  agent_change_sec: 300,
  interval_sec: 10,
  retry_timeout_sec: 60,
}

// parseWatchdogTargets turns the comma/space-separated targets input into the
// ping_targets list a submitted config carries.
export const parseWatchdogTargets = (targets: string): string[] =>
  targets
    .split(/[,\s]+/)
    .map((t) => t.trim())
    .filter(Boolean)

const Divider: FC<{ label: string }> = ({ label }) => (
  <div className="flex items-center gap-2 pt-1">
    <span className="text-xxs font-bold uppercase text-neutral-6">{label}</span>
    <div className="h-px flex-1 bg-neutral-11" />
  </div>
)

const Toggle: FC<{
  label: string
  hint?: string
  checked: boolean
  disabled?: boolean
  onChange: (next: boolean) => void
}> = ({ label, hint, checked, disabled, onChange }) => (
  <label className="flex items-start gap-2 text-xs text-neutral-4">
    <input
      type="checkbox"
      checked={checked}
      disabled={disabled}
      onChange={(e) => onChange(e.target.checked)}
      className="mt-0.5"
    />
    <span>
      {label}
      {hint && <span className="block text-xxs text-neutral-6">{hint}</span>}
    </span>
  </label>
)

const NumberInput: FC<{
  field: NumberField
  value: number | undefined
  disabled?: boolean
  onChange: (next: number) => void
}> = ({ field, value, disabled, onChange }) => {
  const spec = NUMBER_FIELDS[field]
  return (
    <label className="flex items-center justify-between gap-2 text-xs text-neutral-4">
      <span>{numberFieldLabel(field)}</span>
      <input
        type="number"
        min={spec.min}
        max={spec.max}
        step={1}
        value={value ?? 0}
        disabled={disabled}
        // Every watchdog number is a whole number on the wire; a decimal
        // would refuse the whole save as a malformed request.
        onChange={(e) => onChange(Math.round(Number(e.target.value)))}
        className={inputClass}
      />
    </label>
  )
}

export const WatchdogFields: FC<{
  config: WatchdogConfig
  // The ping-targets input as typed (parsed with parseWatchdogTargets at
  // submit, so half-typed separators never eat a target).
  targets: string
  disabled?: boolean
  onChange: <K extends keyof WatchdogConfig>(
    key: K,
    value: WatchdogConfig[K],
  ) => void
  onTargetsChange: (next: string) => void
}> = ({ config, targets, disabled, onChange, onTargetsChange }) => {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col gap-3">
      <Divider label={t('entities.node.watchdogFields.sectionSupervise')} />
      <Toggle
        label={t('entities.node.watchdogFields.agentHealth')}
        hint={t('entities.node.watchdogFields.agentHealthHint')}
        checked={config.agent_health}
        disabled={disabled}
        onChange={(v) => onChange('agent_health', v)}
      />
      {config.agent_health && (
        <NumberInput
          field="agent_change_sec"
          value={config.agent_change_sec}
          disabled={disabled}
          onChange={(v) => onChange('agent_change_sec', v)}
        />
      )}

      <Toggle
        label={t('entities.node.watchdogFields.osHang')}
        hint={t('entities.node.watchdogFields.osHangHint')}
        checked={config.os_hang}
        disabled={disabled}
        onChange={(v) => onChange('os_hang', v)}
      />
      {config.os_hang && (
        <NumberInput
          field="watchdog_timeout_sec"
          value={config.watchdog_timeout_sec}
          disabled={disabled}
          onChange={(v) => onChange('watchdog_timeout_sec', v)}
        />
      )}

      <Toggle
        label={t('entities.node.watchdogFields.network')}
        hint={t('entities.node.watchdogFields.networkHint')}
        checked={config.network}
        disabled={disabled}
        onChange={(v) => onChange('network', v)}
      />
      {config.network && (
        <div className="flex flex-col gap-2">
          <label className="flex flex-col gap-1 text-xs text-neutral-4">
            <span>{t('entities.node.watchdogFields.pingTargetsLabel')}</span>
            <input
              type="text"
              value={targets}
              disabled={disabled}
              placeholder={t(
                'entities.node.watchdogFields.pingTargetsPlaceholder',
              )}
              onChange={(e) => onTargetsChange(e.target.value)}
              className="rounded border border-neutral-11 bg-neutral-13 px-2 py-1 font-mono text-xs text-neutral-2"
            />
          </label>
          <NumberInput
            field="ping_count"
            value={config.ping_count ?? 3}
            disabled={disabled}
            onChange={(v) => onChange('ping_count', v)}
          />
          <label className="flex items-center justify-between gap-2 text-xs text-neutral-4">
            <span>{t('entities.node.watchdogFields.interfaceLabel')}</span>
            <input
              type="text"
              value={config.interface ?? ''}
              disabled={disabled}
              placeholder={t(
                'entities.node.watchdogFields.interfacePlaceholder',
              )}
              onChange={(e) => onChange('interface', e.target.value)}
              className={inputClass}
            />
          </label>
        </div>
      )}

      <Divider label={t('entities.node.watchdogFields.sectionThresholds')} />
      <p className="text-xxs text-neutral-6">
        {t('entities.node.watchdogFields.thresholdsHint')}
      </p>
      {(
        [
          'max_load_1',
          'max_load_5',
          'max_load_15',
          'max_temperature_c',
          'min_memory_pages',
        ] as NumberField[]
      ).map((field) => (
        <NumberInput
          key={field}
          field={field}
          value={config[field as keyof WatchdogConfig] as number | undefined}
          disabled={disabled}
          onChange={(v) => onChange(field as keyof WatchdogConfig, v as never)}
        />
      ))}

      <Divider label={t('entities.node.watchdogFields.sectionTiming')} />
      <NumberInput
        field="interval_sec"
        value={config.interval_sec}
        disabled={disabled}
        onChange={(v) => onChange('interval_sec', v)}
      />
      <NumberInput
        field="retry_timeout_sec"
        value={config.retry_timeout_sec}
        disabled={disabled}
        onChange={(v) => onChange('retry_timeout_sec', v)}
      />
    </div>
  )
}
