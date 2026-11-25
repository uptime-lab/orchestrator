import { FC, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { WatchdogConfig } from '@/entities/node/api/watchdog'
import {
  DEFAULT_WATCHDOG_CONFIG,
  parseWatchdogTargets,
  WatchdogFields,
} from '@/entities/node/ui/watchdogFields'

import { Button } from '@/shared/ui/button/button.tsx'

// The full watchdog editor, behind the presets rather than instead of them.
// Presets cover the configurations worth having; this exists for the fleet that
// needs a threshold nobody anticipated — a specific temperature ceiling, a
// second ping target, a slower petting interval. The field grid itself lives
// in entities/node/ui/watchdogFields, shared with the OU/account policy form.

export const WatchdogForm: FC<{
  // The node's stored configuration, so editing starts from what is actually
  // desired rather than from a blank slate that would silently drop fields.
  initial?: WatchdogConfig
  disabled?: boolean
  busy?: boolean
  onSubmit: (config: WatchdogConfig) => void
  onCancel: () => void
}> = ({ initial, disabled, busy, onSubmit, onCancel }) => {
  const { t } = useTranslation()
  const [config, setConfig] = useState<WatchdogConfig>({
    ...DEFAULT_WATCHDOG_CONFIG,
    ...(initial ?? {}),
    // Editing always produces an arm; disarming is its own button, so a stored
    // config that happens to be disabled must not submit as a no-op.
    enabled: true,
  })
  const [targets, setTargets] = useState(
    (initial?.ping_targets ?? []).join(', '),
  )

  const set = <K extends keyof WatchdogConfig>(
    key: K,
    value: WatchdogConfig[K],
  ) => setConfig((prev) => ({ ...prev, [key]: value }))

  const submit = () => {
    onSubmit({
      ...config,
      ping_targets: config.network ? parseWatchdogTargets(targets) : [],
      ping_count: config.network ? (config.ping_count ?? 3) : 0,
    })
  }

  return (
    <div className="flex flex-col gap-3">
      <WatchdogFields
        config={config}
        targets={targets}
        disabled={disabled}
        onChange={set}
        onTargetsChange={setTargets}
      />
      <div className="flex gap-2">
        <Button
          variant="normal"
          kind="primary"
          className="text-xs"
          disabled={disabled || busy}
          onClick={submit}
        >
          {busy
            ? t('widgets.sidebars.node.watchdogForm.applying')
            : t('widgets.sidebars.node.watchdogForm.arm')}
        </Button>
        <Button
          variant="normal"
          kind="secondary"
          className="text-xs"
          onClick={onCancel}
        >
          {t('widgets.sidebars.node.watchdogForm.cancel')}
        </Button>
      </div>
    </div>
  )
}
