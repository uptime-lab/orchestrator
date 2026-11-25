import { useQuery } from '@tanstack/react-query'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { getDeviceProfileCatalog } from '@/entities/deviceProfile'
import type { LEDConfig, LEDSource } from '@/entities/node/api/led'
import type { WatchdogConfig } from '@/entities/node/api/watchdog'
import {
  LED_ALL_SOURCES,
  LED_DEFAULT_BRIGHTNESS,
  ledConfigToRows,
  ledRowToSpec,
  ledSourceDefaults,
  ledSpecsInvalidBrightness,
  ledSpecsInvalidRange,
  type LEDRow,
} from '@/entities/node/ui/ledFields'
import {
  DEFAULT_WATCHDOG_CONFIG,
  parseWatchdogTargets,
  WatchdogFields,
} from '@/entities/node/ui/watchdogFields'
import { imagesHooks, imagesModel } from '@/entities/osImage'
import type { Flag, OUDeviceProfile, OUEffectivePolicy } from '@/entities/ou'

import { Button } from '@/shared/ui/button/button.tsx'
import { InfoHint } from '@/shared/ui/infoHint'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

// Framed like an input: on the card background a bare dark fill reads as
// static text, and nothing said "this is a dropdown". The
// native chevron hugs the frame's right edge, so it is replaced by our own
// with breathing room (appearance-none + the FormSelect wrapper below).
const selectClassName =
  'w-full appearance-none bg-neutral-14 border border-neutral-10 rounded pl-2.5 pr-10 py-2.5 text-white outline-none transition-colors hover:border-neutral-8 focus:border-lime-6 disabled:opacity-50 disabled:cursor-not-allowed'

// A framed select with its own chevron, inset from the right edge.
const FormSelect = ({
  wrapperClassName,
  children,
  ...props
}: React.SelectHTMLAttributes<HTMLSelectElement> & {
  wrapperClassName?: string
}) => (
  // min-w-0: a flex item defaults to min-width auto, and a select's intrinsic
  // minimum is its widest option — one long option label would otherwise pin
  // the card's min-content width and grow the page a horizontal scrollbar.
  <div className={'relative min-w-0 ' + (wrapperClassName ?? '')}>
    <select {...props} className={selectClassName}>
      {children}
    </select>
    <svg
      className="pointer-events-none absolute right-4 top-1/2 -translate-y-1/2 w-4 h-4 text-neutral-5"
      fill="none"
      stroke="currentColor"
      viewBox="0 0 24 24"
      aria-hidden="true"
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth={2}
        d="M19 9l-7 7-7-7"
      />
    </svg>
  </div>
)

// The stored values the form edits — a unit's own fields or the account
// default policy (both carry the same shape).
export type PolicyFormStored = {
  network_first: Flag
  boot_to_os: Flag
  ssh_password_auth: Flag
  os_image_id?: string
  device_profile?: OUDeviceProfile
  led_config?: LEDConfig
  watchdog_config?: WatchdogConfig
}

// The update payload the form submits. Provisioning strings: "" clears back to
// inherit; password is present only when typed or explicitly cleared;
// device_profile is omitted while the hardware catalog has not loaded (the
// backend treats omitted as unchanged).
export type PolicyFormUpdate = {
  network_first: Flag
  boot_to_os: Flag
  ssh_password_auth: Flag
  os_image_id: string
  device_profile?: OUDeviceProfile
  // Omitted while no platform is selected (the LED controls are board-scoped
  // and hidden); all-defaults clears back to inherit.
  led_config?: LEDConfig
  // Always sent: a disabled config clears back to inherit, an enabled one
  // declares the supervision for this scope. The save dispatches nothing.
  watchdog_config: WatchdogConfig
}

type PolicyFormProps = {
  stored: PolicyFormStored
  // Resolved values + sources for the unit's chain; absent on the account
  // defaults page, which is the last level before the system default.
  effective?: OUEffectivePolicy
  effectiveOSImageName?: string
  // The hardware equipage resolved for this level, and which level supplied it.
  // The server has always returned both; the unit page forwarded neither, so a
  // child unit with no declaration of its own showed an empty board selector
  // and the operator re-declared locally what it already inherited.
  effectiveDeviceProfile?: OUDeviceProfile
  effectiveDeviceProfileSource?: string
  // How many nodes an ssh_password_auth change would push apply_access_config
  // to (shown BEFORE save). Absent on the account defaults page.
  passwordAuthImpact?: number
  disabled?: boolean
  onSave: (update: PolicyFormUpdate) => Promise<void>
  // Rendered at the foot of the Hardware card: the unit page puts its
  // "apply the profile to the unit's nodes" button here, next to the field it
  // acts on. Floating under the form it read as a stray control.
  hardwareActions?: React.ReactNode
}

// Typed key per known level; an unknown level falls through as the raw
// string rather than a broken lookup.
const sourceLabelKey = (source: string) => {
  switch (source) {
    case 'ou':
      return 'widgets.ouPolicyForm.sourceLabels.ou' as const
    case 'root_ou':
      return 'widgets.ouPolicyForm.sourceLabels.root_ou' as const
    case 'account':
      return 'widgets.ouPolicyForm.sourceLabels.account' as const
    case 'default':
      return 'widgets.ouPolicyForm.sourceLabels.default' as const
    default:
      return null
  }
}

// One line under an inherited field: what actually applies, and which level
// said so. Rendered only while the stored value is inherit/empty — a field the
// unit answers itself needs no provenance.
const EffectiveLine = ({
  show,
  value,
  source,
}: {
  show: boolean
  value: string
  source: string
}) => {
  const { t } = useTranslation()
  if (!show) return null
  const key = sourceLabelKey(source)
  return (
    <div className="text-xs text-neutral-5">
      {t('widgets.ouPolicyForm.effectivePrefix')}{' '}
      <span className="text-neutral-3">{value}</span>{' '}
      {t('widgets.ouPolicyForm.effectiveFrom', {
        source: key ? t(key) : source,
      })}
    </div>
  )
}

// One titled CARD of fields, one field per row (the ROOT caps the column at
// max-w-2xl so the cards fit their content). A two-column pile of
// unrelated fields reads as one confusing blob — each section is its own
// framed card with a prominent, line-separated title, so the grouping is
// visible before any field is read.
const FormSection = ({
  title,
  hint,
  children,
}: {
  title: string
  hint?: string
  children: React.ReactNode
}) => (
  <div className="bg-neutral-13 rounded-lg p-5 flex flex-col gap-4">
    <div className="border-b border-neutral-11 pb-3">
      <div className="text-sm font-bold uppercase tracking-wide text-neutral-2">
        {title}
      </div>
      {hint && <div className="text-xs text-neutral-5 mt-1.5">{hint}</div>}
    </div>
    <div className="flex flex-col gap-5">{children}</div>
  </div>
)

// A field's explanation, framed so it reads as an aside rather than blending
// into the controls. tone="warn" is for the texts that name a way to lose
// access — those stay visible, never behind hover.
const HintBox = ({
  tone = 'default',
  children,
}: {
  tone?: 'default' | 'warn'
  children: React.ReactNode
}) => (
  <div
    className={
      tone === 'warn'
        ? 'rounded border border-yellow-6/60 px-3 py-2 text-xs text-yellow-5'
        : 'rounded border border-neutral-10 px-3 py-2 text-xs text-neutral-4'
    }
  >
    {children}
  </div>
)

// The shared OU/account policy editor. One copy of the form the three sidebars
// used to triplicate (and drift): boot flags, SSH password login, provisioning
// defaults and the hardware platform block.
export const OUPolicyForm = ({
  stored,
  effective,
  effectiveOSImageName,
  effectiveDeviceProfile,
  effectiveDeviceProfileSource,
  passwordAuthImpact,
  disabled,
  onSave,
  hardwareActions,
}: PolicyFormProps) => {
  const { t } = useTranslation()
  const { images } = imagesHooks.useImages()
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const [networkFirst, setNetworkFirst] = useState<Flag>('inherit')
  const [bootToOs, setBootToOs] = useState<Flag>('inherit')
  const [sshPasswordAuth, setSshPasswordAuth] = useState<Flag>('inherit')
  const [osImageId, setOsImageId] = useState('')
  const [profileId, setProfileId] = useState('')
  const [variantId, setVariantId] = useState('')
  const [moduleIds, setModuleIds] = useState<string[]>([])
  const [ledStealth, setLedStealth] = useState(false)
  const [ledActOff, setLedActOff] = useState(false)
  const [ledPwrOff, setLedPwrOff] = useState(false)
  const [ledRows, setLedRows] = useState<LEDRow[]>([])
  const [watchdogDeclared, setWatchdogDeclared] = useState(false)
  const [watchdogConfig, setWatchdogConfig] = useState<WatchdogConfig>(
    DEFAULT_WATCHDOG_CONFIG,
  )
  const [watchdogTargets, setWatchdogTargets] = useState('')

  const { data: profileCatalog } = useQuery({
    queryKey: ['device-profile-catalog'],
    queryFn: getDeviceProfileCatalog,
    staleTime: 5 * 60 * 1000,
  })
  const selectedProfileBoard = profileCatalog?.boards.find(
    (b) => b.id === profileId,
  )
  // LED behaviour follows the board this unit ends up with: its own choice,
  // else the one it inherits. A unit that inherits its board can still set
  // its own LED behaviour.
  const ledBoardId = profileId || effectiveDeviceProfile?.profile_id || ''
  const ledBoard = profileCatalog?.boards.find((b) => b.id === ledBoardId)

  // Fetch once on mount; the stores are shared and cached (empty deps on
  // purpose — the emptiness checks would refire on every store update).
  useEffect(() => {
    if (images.length === 0) imagesModel.fetchImagesFx()
  }, [])

  useEffect(() => {
    setNetworkFirst(stored.network_first)
    setBootToOs(stored.boot_to_os)
    setSshPasswordAuth(stored.ssh_password_auth)
    setOsImageId(stored.os_image_id ?? '')
    setProfileId(stored.device_profile?.profile_id ?? '')
    setVariantId(stored.device_profile?.variant_id ?? '')
    setModuleIds((stored.device_profile?.modules ?? []).map((m) => m.module_id))
    setLedStealth(stored.led_config?.stealth_mode ?? false)
    setLedActOff(stored.led_config?.act_led_off ?? false)
    setLedPwrOff(stored.led_config?.pwr_led_off ?? false)
    // Rows only for the entries the config carries; the render pads to the
    // selected board's LED count (the catalog may not have loaded yet here).
    setLedRows(
      ledConfigToRows(
        stored.led_config,
        (
          stored.led_config?.digital_leds ??
          stored.led_config?.digital_sources ??
          []
        ).length,
      ),
    )
    setWatchdogDeclared(!!stored.watchdog_config?.enabled)
    setWatchdogConfig(
      stored.watchdog_config?.enabled
        ? { ...DEFAULT_WATCHDOG_CONFIG, ...stored.watchdog_config }
        : DEFAULT_WATCHDOG_CONFIG,
    )
    setWatchdogTargets((stored.watchdog_config?.ping_targets ?? []).join(', '))
    setSaved(false)
  }, [stored])

  const passwordAuthChanged = sshPasswordAuth !== stored.ssh_password_auth

  const handleSave = useCallback(async () => {
    setFormError(null)
    setSaved(false)
    try {
      setIsProcessing(true)
      // Hardware equipage travels as one block: empty profile clears back to
      // inherit; without the catalog the block cannot be rebuilt, so it is
      // OMITTED (unchanged) rather than sent as an empty module list that
      // would wipe the declarations on an unrelated edit. Each module keeps
      // its stored port; the board's first port only defaults modules added
      // in this edit.
      let deviceProfile: OUDeviceProfile | undefined
      if (profileCatalog) {
        if (profileId) {
          const firstPort = selectedProfileBoard?.ports?.[0]?.id
          const compatiblePorts = selectedProfileBoard?.compatible_modules
          const storedPorts = new Map(
            (stored.device_profile?.modules ?? []).map((m) => [
              m.module_id,
              m.port_id,
            ]),
          )
          deviceProfile = {
            profile_id: profileId,
            variant_id: variantId || undefined,
            modules: moduleIds.flatMap((id) => {
              // Stored port wins (never rewires an existing declaration); a
              // module added in this edit lands on the server-computed
              // compatible port, falling back to the board's first port.
              const portId =
                storedPorts.get(id) ?? compatiblePorts?.[id] ?? firstPort
              return portId ? [{ module_id: id, port_id: portId }] : []
            }),
          }
        } else {
          deviceProfile = { profile_id: '' }
        }
      }
      // LED behaviour travels only while its controls are visible (a platform
      // is selected and the catalog loaded): all-defaults clears to inherit.
      // The v2 form (digital_leds) is what travels; the platform derives the
      // v1 projection for agents that predate it.
      let ledConfig: LEDConfig | undefined
      if (profileCatalog && ledBoardId) {
        const boardLED = ledBoard?.led
        const count = boardLED?.digital_leds ?? 0
        const specs = Array.from(
          { length: count },
          (_, i) =>
            ledRows[i] ?? { source: 'off', low: '', high: '', brightness: '' },
        ).map(ledRowToSpec)
        while (specs.length > 0 && specs[specs.length - 1].source === 'off') {
          specs.pop()
        }
        ledConfig = {
          stealth_mode: !!boardLED?.stealth_available && ledStealth,
          act_led_off: ledActOff,
          pwr_led_off: ledPwrOff,
          digital_leds: specs.length > 0 ? specs : undefined,
        }
      } else if (profileCatalog && stored.led_config) {
        // No board at all any more: the LED controls are gone, so the stored
        // behaviour is cleared (all-defaults) rather than left applying where
        // nothing shows it.
        ledConfig = {
          stealth_mode: false,
          act_led_off: false,
          pwr_led_off: false,
        }
      }
      // Watchdog travels as one block: disabled clears back to inherit, and
      // the save only STORES — a device is armed solely by its per-node apply.
      const watchdog: WatchdogConfig = watchdogDeclared
        ? {
            ...watchdogConfig,
            enabled: true,
            ping_targets: watchdogConfig.network
              ? parseWatchdogTargets(watchdogTargets)
              : [],
            ping_count: watchdogConfig.network
              ? (watchdogConfig.ping_count ?? 3)
              : 0,
          }
        : { ...DEFAULT_WATCHDOG_CONFIG, enabled: false }
      await onSave({
        network_first: networkFirst,
        boot_to_os: bootToOs,
        ssh_password_auth: sshPasswordAuth,
        os_image_id: osImageId,
        device_profile: deviceProfile,
        led_config: ledConfig,
        watchdog_config: watchdog,
      })
      setSaved(true)
    } catch (error) {
      setFormError(
        extractApiErrorMessage(error, t('widgets.ouPolicyForm.saveError')),
      )
    } finally {
      setIsProcessing(false)
    }
  }, [
    onSave,
    networkFirst,
    bootToOs,
    sshPasswordAuth,
    osImageId,
    profileId,
    variantId,
    moduleIds,
    ledStealth,
    ledActOff,
    ledPwrOff,
    ledRows,
    watchdogDeclared,
    watchdogConfig,
    watchdogTargets,
    profileCatalog,
    selectedProfileBoard,
    ledBoardId,
    ledBoard,
    stored,
  ])

  const busy = isProcessing || !!disabled

  // LED bounds mirrored client-side so a refused save is not the operator's
  // first feedback; the server (LEDConfig.Validate) stays the authority.
  const ledSpecs = ledRows.map(ledRowToSpec)
  const ledInvalidRange = ledSpecsInvalidRange(ledSpecs)
  const ledInvalidBrightness = ledSpecsInvalidBrightness(ledSpecs)
  const ledInvalid = !!ledBoardId && (ledInvalidRange || ledInvalidBrightness)
  const flagLabel = (v: boolean) =>
    v ? t('widgets.ouPolicyForm.flagOn') : t('widgets.ouPolicyForm.flagOff')
  // On the Account defaults page (no effective chain above it) "Inherit" begged
  // the question "from what?" — the honest label there is the system default.
  const isAccountLevel = !effective
  const inheritFlagLabel = isAccountLevel
    ? t('widgets.ouPolicyForm.systemDefaultOn')
    : t('widgets.sidebars.ou.flag.inherit')

  // Only modules the selected board can actually host (server-computed map).
  const compatibleModules = (profileCatalog?.modules ?? []).filter(
    (m) => !!selectedProfileBoard?.compatible_modules?.[m.id],
  )

  return (
    <div className="flex flex-col gap-5 max-w-2xl">
      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <FormSection title={t('widgets.ouPolicyForm.sections.boot')}>
        <div className="flex flex-col gap-2">
          {/* Section already says "Boot" — repeating it in the field label
              read as duplication. */}
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.ouPolicyForm.bootTargetLabel')}
          </label>
          <FormSelect
            name="boot_to_os"
            value={bootToOs}
            onChange={(e) => setBootToOs(e.target.value as Flag)}
            disabled={busy}
          >
            <option value="inherit">
              {isAccountLevel
                ? t('widgets.ouPolicyForm.systemDefaultRecovery')
                : t('widgets.sidebars.ou.bootTarget.inherit')}
            </option>
            <option value="on">{t('widgets.sidebars.ou.bootTarget.on')}</option>
            <option value="off">
              {t('widgets.sidebars.ou.bootTarget.off')}
            </option>
          </FormSelect>
          <EffectiveLine
            show={bootToOs === 'inherit' && !!effective}
            value={
              effective?.boot_to_os.value
                ? t('widgets.sidebars.ou.bootTarget.on')
                : t('widgets.sidebars.ou.bootTarget.off')
            }
            source={effective?.boot_to_os.source ?? ''}
          />
          <HintBox>{t('widgets.sidebars.ou.bootToOsHint')}</HintBox>
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.ou.networkFirst')}
          </label>
          <FormSelect
            name="network_first"
            value={networkFirst}
            onChange={(e) => setNetworkFirst(e.target.value as Flag)}
            disabled={busy}
          >
            <option value="inherit">{inheritFlagLabel}</option>
            <option value="on">{t('widgets.sidebars.ou.flag.on')}</option>
            <option value="off">{t('widgets.sidebars.ou.flag.off')}</option>
          </FormSelect>
          <EffectiveLine
            show={networkFirst === 'inherit' && !!effective}
            value={flagLabel(!!effective?.network_first.value)}
            source={effective?.network_first.source ?? ''}
          />
          <HintBox>{t('widgets.sidebars.ou.networkFirstHint')}</HintBox>
        </div>
      </FormSection>

      {/* Password logins are a second, weaker way in. Turning this off is the
          one control here that can lock people out, so it says what it means,
          never hides behind a boolean — and names the blast radius before the
          save, because the change is pushed to every affected device. */}
      <FormSection title={t('widgets.ouPolicyForm.sections.sshAccess')}>
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.ouPolicyForm.passwordLoginLabel')}
          </label>
          <FormSelect
            name="ssh_password_auth"
            value={sshPasswordAuth}
            onChange={(e) => setSshPasswordAuth(e.target.value as Flag)}
            disabled={busy}
          >
            <option value="inherit">
              {isAccountLevel
                ? t('widgets.ouPolicyForm.systemDefaultAllowed')
                : t('widgets.sidebars.ou.flag.inherit')}
            </option>
            <option value="on">
              {t('widgets.ouPolicyForm.passwordAllowed')}
            </option>
            <option value="off">
              {t('widgets.ouPolicyForm.passwordRefused')}
            </option>
          </FormSelect>
          <EffectiveLine
            show={sshPasswordAuth === 'inherit' && !!effective}
            value={
              effective?.ssh_password_auth.value
                ? t('widgets.ouPolicyForm.passwordAllowed')
                : t('widgets.ouPolicyForm.passwordRefused')
            }
            source={effective?.ssh_password_auth.source ?? ''}
          />
          {passwordAuthChanged &&
            (passwordAuthImpact !== undefined ? (
              <div className="text-xs text-yellow-5">
                {t('widgets.ouPolicyForm.passwordAuthImpact', {
                  count: passwordAuthImpact,
                })}
              </div>
            ) : (
              <div className="text-xs text-yellow-5">
                {t('widgets.ouPolicyForm.passwordAuthImpactUnknown')}
              </div>
            ))}
          <HintBox tone="warn">
            {t('widgets.ouPolicyForm.passwordAuthWarning')}
          </HintBox>
        </div>
      </FormSection>

      {/* The default install image is the one provisioning default still
          consumed: blank-image installs (API / playbook block) resolve it, and
          the Install images rail drag sets it. The legacy first-user fields
          (username, password, provisioning key) are gone from the form —
          provisioning writes access entries now, and nothing in the product
          reads them. The columns stay for data compatibility. */}
      <FormSection title={t('widgets.ouPolicyForm.sections.install')}>
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.ouPolicyForm.defaultImageLabel')}
          </label>
          <FormSelect
            name="os_image_id"
            value={osImageId}
            onChange={(e) => setOsImageId(e.target.value)}
            disabled={busy}
          >
            <option value="">
              {isAccountLevel
                ? t('widgets.ouPolicyForm.noneChoosePerInstall')
                : t('widgets.sidebars.ou.inherit')}
            </option>
            {images.map((image) => (
              <option key={image.id} value={image.id}>
                {image.title}
                {image.version ? ` (${image.version})` : ''}
              </option>
            ))}
          </FormSelect>
          <EffectiveLine
            show={!osImageId && !!effective?.os_image_id.present}
            value={effectiveOSImageName || effective?.os_image_id.value || ''}
            source={effective?.os_image_id.source ?? ''}
          />
          <HintBox>{t('widgets.ouPolicyForm.defaultImageHint')}</HintBox>
        </div>
      </FormSection>

      {/* Hardware platform — inherited node -> OU -> root -> account as ONE
          block (board + variant + modules). Declaring here only STORES: each
          node still gets its config via an explicit Apply. */}
      <FormSection title={t('widgets.ouPolicyForm.sections.hardware')}>
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.ouPolicyForm.platformLabel')}
          </label>
          {!profileCatalog ? (
            <div className="text-sm text-neutral-5">
              {t('widgets.ouPolicyForm.catalogNotLoaded')}
            </div>
          ) : (
            <FormSelect
              name="device_profile_board"
              value={profileId}
              onChange={(e) => {
                const id = e.target.value
                setProfileId(id)
                const board = profileCatalog.boards.find((b) => b.id === id)
                setVariantId(board?.variants?.[0]?.id ?? '')
                setModuleIds([])
                // Board-scoped LED bits reset with the board; the sysfs knobs
                // (ACT/PWR) apply to any Pi and survive.
                setLedStealth(false)
                setLedRows([])
              }}
              disabled={busy}
            >
              {/* No system default exists for hardware: unset at the account
                level simply means no declaration anywhere. */}
              <option value="">
                {isAccountLevel
                  ? t('widgets.ouPolicyForm.noneNotDeclared')
                  : t('widgets.sidebars.ou.inherit')}
              </option>
              {profileCatalog.boards.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name}
                </option>
              ))}
            </FormSelect>
          )}
          <EffectiveLine
            show={!profileId && !!effectiveDeviceProfile?.profile_id}
            value={
              profileCatalog?.boards.find(
                (b) => b.id === effectiveDeviceProfile?.profile_id,
              )?.name ??
              effectiveDeviceProfile?.profile_id ??
              ''
            }
            source={effectiveDeviceProfileSource ?? ''}
          />
          {profileId && (selectedProfileBoard?.variants?.length ?? 0) > 0 && (
            <FormSelect
              name="device_profile_variant"
              value={variantId}
              onChange={(e) => setVariantId(e.target.value)}
              disabled={busy}
            >
              {selectedProfileBoard?.variants?.map((v) => (
                <option key={v.id} value={v.id}>
                  {t('widgets.ouPolicyForm.variantOption', { name: v.name })}
                </option>
              ))}
            </FormSelect>
          )}
          {/* The notes stay OUT of the option label: an <option> cannot wrap or
            truncate, so a long note becomes the select's minimum width and
            widens the whole page. Shown for the chosen variant
            as a wrappable line instead — the node sidebar does the same. */}
          {profileId &&
            (() => {
              const notes = selectedProfileBoard?.variants?.find(
                (v) => v.id === variantId,
              )?.notes
              return notes ? (
                <div className="text-xs text-neutral-6">{notes}</div>
              ) : null
            })()}
          {ledBoardId && (
            <div className="flex flex-col gap-2 pt-2">
              <label className="text-sm font-medium text-neutral-7 uppercase">
                {t('widgets.ouPolicyForm.ledBehaviourLabel')}
              </label>
              {ledBoard?.led?.stealth_available && (
                <label className="flex items-center gap-2 text-sm text-neutral-3 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={ledStealth}
                    disabled={busy}
                    onChange={() => setLedStealth((v) => !v)}
                    className="accent-lime-600"
                  />
                  <span className="whitespace-nowrap">
                    {t('widgets.ouPolicyForm.stealthMode')}
                    <InfoHint
                      className="ml-1"
                      text={t('widgets.ouPolicyForm.stealthModeHint')}
                    />
                  </span>
                </label>
              )}
              <label className="flex items-center gap-2 text-sm text-neutral-3 cursor-pointer">
                <input
                  type="checkbox"
                  checked={ledActOff}
                  disabled={busy}
                  onChange={() => setLedActOff((v) => !v)}
                  className="accent-lime-600"
                />
                {t('widgets.ouPolicyForm.activityLedOff')}
              </label>
              <label className="flex items-center gap-2 text-sm text-neutral-3 cursor-pointer">
                <input
                  type="checkbox"
                  checked={ledPwrOff}
                  disabled={busy}
                  onChange={() => setLedPwrOff((v) => !v)}
                  className="accent-lime-600"
                />
                {t('widgets.ouPolicyForm.powerLedOff')}
              </label>
              {(ledBoard?.led?.digital_leds ?? 0) > 0 && (
                <div className="flex flex-col gap-2">
                  {Array.from(
                    { length: ledBoard?.led?.digital_leds ?? 0 },
                    (_, i) => {
                      const row: LEDRow = ledRows[i] ?? {
                        source: 'off',
                        low: '',
                        high: '',
                        brightness: '',
                      }
                      const patchRow = (patch: Partial<LEDRow>) =>
                        setLedRows((prev) => {
                          const next = [...prev]
                          while (next.length <= i) {
                            next.push({
                              source: 'off',
                              low: '',
                              high: '',
                              brightness: '',
                            })
                          }
                          next[i] = { ...next[i], ...patch }
                          return next
                        })
                      return (
                        <div key={i} className="flex flex-col gap-1.5">
                          <div className="flex items-center gap-3">
                            <span className="text-sm text-neutral-5 w-28">
                              {t('widgets.ouPolicyForm.led.ledN', {
                                index: i + 1,
                              })}
                            </span>
                            <FormSelect
                              wrapperClassName="flex-1"
                              value={row.source}
                              onChange={(e) =>
                                // The bounds are per source, so a source change
                                // resets the pair; brightness survives it.
                                patchRow({
                                  source: e.target.value as LEDSource,
                                  low: '',
                                  high: '',
                                })
                              }
                              disabled={busy}
                            >
                              {LED_ALL_SOURCES.map((s) => (
                                <option key={s} value={s}>
                                  {t(`entities.node.ledFields.source.${s}`)}
                                </option>
                              ))}
                            </FormSelect>
                          </div>
                          {row.source !== 'off' && (
                            <div className="flex items-center gap-1.5 pl-2 text-xs text-neutral-4">
                              <span>
                                {t('entities.node.ledFields.rangeLow')}
                              </span>
                              <input
                                type="number"
                                value={row.low}
                                placeholder={String(
                                  ledSourceDefaults(row.source).low,
                                )}
                                disabled={busy}
                                onChange={(e) =>
                                  patchRow({ low: e.target.value })
                                }
                                className="w-16 bg-neutral-14 border border-neutral-10 rounded px-1.5 py-1 text-xs text-white"
                              />
                              <span>
                                {t('entities.node.ledFields.rangeHigh')}
                              </span>
                              <input
                                type="number"
                                value={row.high}
                                placeholder={String(
                                  ledSourceDefaults(row.source).high,
                                )}
                                disabled={busy}
                                onChange={(e) =>
                                  patchRow({ high: e.target.value })
                                }
                                className="w-16 bg-neutral-14 border border-neutral-10 rounded px-1.5 py-1 text-xs text-white"
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
                                disabled={busy}
                                onChange={(e) =>
                                  patchRow({ brightness: e.target.value })
                                }
                                className="w-16 bg-neutral-14 border border-neutral-10 rounded px-1.5 py-1 text-xs text-white"
                              />
                              <span>
                                {t('entities.node.ledFields.unitPercent')}
                              </span>
                            </div>
                          )}
                        </div>
                      )
                    },
                  )}
                  {ledInvalid && (
                    <div className="text-xs text-red-6">
                      {ledInvalidRange
                        ? t('entities.node.ledFields.invalidRange')
                        : t('entities.node.ledFields.invalidBrightness')}
                    </div>
                  )}
                  <div className="text-xs text-neutral-5">
                    {t('entities.node.ledFields.rangeHint')}
                  </div>
                </div>
              )}
              <HintBox>{t('widgets.ouPolicyForm.ledScopeHint')}</HintBox>
            </div>
          )}
          {profileId && compatibleModules.length > 0 && (
            <div className="flex flex-col gap-2 pt-2">
              {/* Plugged modules the selected platform can host — the RTC today,
                HATs and friends as the registry grows. */}
              <label className="text-sm font-medium text-neutral-7 uppercase">
                {t('widgets.ouPolicyForm.modulesLabel')}
              </label>
              <div className="flex flex-col gap-1">
                {compatibleModules.map((m) => (
                  <label
                    key={m.id}
                    className="flex items-center gap-2 text-sm text-neutral-3 cursor-pointer"
                  >
                    <input
                      type="checkbox"
                      checked={moduleIds.includes(m.id)}
                      disabled={busy}
                      onChange={() =>
                        setModuleIds((prev) =>
                          prev.includes(m.id)
                            ? prev.filter((x) => x !== m.id)
                            : [...prev, m.id],
                        )
                      }
                      className="accent-lime-600"
                    />
                    <span className="whitespace-nowrap">
                      {m.name}
                      {m.description && (
                        <InfoHint className="ml-1" text={m.description} />
                      )}
                    </span>
                  </label>
                ))}
              </div>
            </div>
          )}
          <HintBox>{t('widgets.ouPolicyForm.hardwareHint')}</HintBox>
          {hardwareActions && (
            <div className="flex justify-end">{hardwareActions}</div>
          )}
        </div>
      </FormSection>

      {/* Watchdog supervision — inherited node -> OU -> root -> account as ONE
          block, the LED pattern. Declaring here only STORES the desired
          config: nothing arms a device without a human present, so each node
          still receives it through its own explicit apply. */}
      <FormSection title={t('widgets.ouPolicyForm.sections.watchdog')}>
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.ouPolicyForm.supervisionLabel')}
          </label>
          <FormSelect
            name="watchdog_declared"
            value={watchdogDeclared ? 'declared' : 'inherit'}
            onChange={(e) => setWatchdogDeclared(e.target.value === 'declared')}
            disabled={busy}
          >
            <option value="inherit">
              {isAccountLevel
                ? t('widgets.ouPolicyForm.noneNotDeclared')
                : t('widgets.sidebars.ou.inherit')}
            </option>
            <option value="declared">
              {t('widgets.ouPolicyForm.declaredForScope')}
            </option>
          </FormSelect>
          {watchdogDeclared && (
            <WatchdogFields
              config={watchdogConfig}
              targets={watchdogTargets}
              disabled={busy}
              onChange={(key, value) =>
                setWatchdogConfig((prev) => ({ ...prev, [key]: value }))
              }
              onTargetsChange={setWatchdogTargets}
            />
          )}
          <HintBox>{t('widgets.ouPolicyForm.watchdogHint')}</HintBox>
        </div>
      </FormSection>

      <div className="flex items-center gap-3">
        <Button size="md" onClick={handleSave} disabled={busy || ledInvalid}>
          {isProcessing
            ? t('widgets.ouPolicyForm.saving')
            : t('widgets.ouPolicyForm.savePolicies')}
        </Button>
        {saved && (
          <span className="text-xs text-lime-6">
            {t('widgets.ouPolicyForm.saved')}
          </span>
        )}
        {disabled && (
          <span className="text-xs text-neutral-5">
            {t('widgets.ouPolicyForm.readOnlyRole')}
          </span>
        )}
      </div>
    </div>
  )
}
