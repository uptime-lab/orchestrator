import clsx from 'clsx'
import { t } from 'i18next'
import React, { FC, useEffect, useMemo } from 'react'

import {
  REBOOT_EXPECTED_MS,
  useTimedProgress,
} from '@/widgets/commandNotification/rebootProgress'

import type { NodeCommand } from '@/entities/node/api/types'
import { useImages } from '@/entities/osImage/hooks'
import { fetchImageFx, fetchImagesFx } from '@/entities/osImage/model'

import {
  failedInstallStaysOnRecovery,
  isLongOutput,
  scriptTimeoutSeconds,
  showsReportedOutput,
} from './commandHistoryView'
import { DHCP_RENEW_MARKER } from './hostnameControl.tsx'

type CommandHistoryProps = {
  commands: NodeCommand[]
}

// The history view is capped; older activity lives in the device's agent logs.
const MAX_HISTORY = 20

const getCommandName = (slug: string): string => {
  // Covers every dispatchable slug in backend domain/node_command; an unknown
  // slug falls back to a humanized form rather than the raw identifier.
  const names: Record<string, string> = {
    reboot: t('widgets.sidebars.node.commandHistory.commandName.reboot'),
    shutdown: t('widgets.sidebars.node.commandHistory.commandName.shutdown'),
    status_check: t(
      'widgets.sidebars.node.commandHistory.commandName.status_check',
    ),
    install_os: t(
      'widgets.sidebars.node.commandHistory.commandName.install_os',
    ),
    install_agent: t(
      'widgets.sidebars.node.commandHistory.commandName.install_agent',
    ),
    update_boot_order: t(
      'widgets.sidebars.node.commandHistory.commandName.update_boot_order',
    ),
    update_file: t(
      'widgets.sidebars.node.commandHistory.commandName.update_file',
    ),
    provision_connection: t(
      'widgets.sidebars.node.commandHistory.commandName.provision_connection',
    ),
    enable_eeprom: t(
      'widgets.sidebars.node.commandHistory.commandName.enable_eeprom',
    ),
    erase_drive: t(
      'widgets.sidebars.node.commandHistory.commandName.erase_drive',
    ),
    update_agent: t(
      'widgets.sidebars.node.commandHistory.commandName.update_agent',
    ),
    set_hostname: t(
      'widgets.sidebars.node.commandHistory.commandName.set_hostname',
    ),
    patch_file: t(
      'widgets.sidebars.node.commandHistory.commandName.patch_file',
    ),
    patch_file_offline: t(
      'widgets.sidebars.node.commandHistory.commandName.patch_file_offline',
    ),
    read_file: t('widgets.sidebars.node.commandHistory.commandName.read_file'),
    wait_for_file: t(
      'widgets.sidebars.node.commandHistory.commandName.wait_for_file',
    ),
    run_script: t(
      'widgets.sidebars.node.commandHistory.commandName.run_script',
    ),
    apply_access_config: t(
      'widgets.sidebars.node.commandHistory.commandName.apply_access_config',
    ),
    apply_access_config_offline: t(
      'widgets.sidebars.node.commandHistory.commandName.apply_access_config_offline',
    ),
    capture_image: t(
      'widgets.sidebars.node.commandHistory.commandName.capture_image',
    ),
    install_packages: t(
      'widgets.sidebars.node.commandHistory.commandName.install_packages',
    ),
    manage_service: t(
      'widgets.sidebars.node.commandHistory.commandName.manage_service',
    ),
    download_file: t(
      'widgets.sidebars.node.commandHistory.commandName.download_file',
    ),
    write_file: t(
      'widgets.sidebars.node.commandHistory.commandName.write_file',
    ),
    delete_file: t(
      'widgets.sidebars.node.commandHistory.commandName.delete_file',
    ),
    apply_watchdog_config: t(
      'widgets.sidebars.node.commandHistory.commandName.apply_watchdog_config',
    ),
    release_identity: t(
      'widgets.sidebars.node.commandHistory.commandName.release_identity',
    ),
    apply_device_profile: t(
      'widgets.sidebars.node.commandHistory.commandName.apply_device_profile',
    ),
    identify: t('widgets.sidebars.node.commandHistory.commandName.identify'),
    apply_utility_config: t(
      'widgets.sidebars.node.commandHistory.commandName.apply_utility_config',
    ),
    apply_led_config: t(
      'widgets.sidebars.node.commandHistory.commandName.apply_led_config',
    ),
  }
  if (names[slug]) return names[slug]
  const humanized = slug.replace(/_/g, ' ')
  return humanized.charAt(0).toUpperCase() + humanized.slice(1)
}

// A run_script carries no slug-level identity, so recognise the actions we send
// ourselves by a marker comment and give them a real name/description instead of
// showing the raw shell.
const knownScriptAction = (
  script?: string,
): { title: string; description: string } | null => {
  if (typeof script !== 'string') return null
  if (script.includes(DHCP_RENEW_MARKER)) {
    return {
      title: t('widgets.sidebars.node.commandHistory.refreshDhcpTitle'),
      description: t(
        'widgets.sidebars.node.commandHistory.refreshDhcpDescription',
      ),
    }
  }
  return null
}

// Display title for a command, payload-aware for the run_script actions we mint.
export const commandTitle = (command: NodeCommand): string => {
  if (command.slug === 'run_script') {
    const action = knownScriptAction(asText(command.payload?.script))
    if (action) return action.title
  }
  return getCommandName(command.slug)
}

const getStatusColor = (
  status: NodeCommand['status'],
): {
  bgColor: string
  textColor: string
  borderColor: string
} => {
  switch (status) {
    case 'success':
      return {
        bgColor: 'bg-lime-12/10',
        textColor: 'text-lime-6',
        borderColor: 'border-lime-12',
      }
    case 'failed':
      return {
        bgColor: 'bg-red-9/10',
        textColor: 'text-red-6',
        borderColor: 'border-red-9',
      }
    case 'in_progress':
    case 'registered':
    case 'sent':
      return {
        bgColor: 'bg-yellow-5/10',
        textColor: 'text-yellow-6',
        borderColor: 'border-yellow-6',
      }
    default:
      return {
        bgColor: 'bg-neutral-9/10',
        textColor: 'text-neutral-6',
        borderColor: 'border-neutral-9',
      }
  }
}

const formatDate = (dateString: string): string => {
  const date = new Date(dateString)
  return date.toLocaleString('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

// asText / asList narrow an unknown payload value for display. The payload is
// Record<string, unknown> because it comes from the API as arbitrary JSON —
// typing it `any` let a misspelt key render as "undefined" with no complaint.
const asText = (v: unknown): string => (typeof v === 'string' ? v : '')
const asList = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []

// Format payload parameters based on command type
const formatCommandParams = (
  slug: string,
  payload: Record<string, unknown>,
  imageMap?: Map<string, { title: string; version?: string }>,
): React.ReactNode => {
  if (!payload || Object.keys(payload).length === 0) {
    return (
      <div className="text-xs text-neutral-6">
        {t('widgets.sidebars.node.commandHistory.noParameters')}
      </div>
    )
  }

  switch (slug) {
    case 'install_os': {
      const imageID = asText(payload.image_id)
      const imageInfo = imageID ? imageMap?.get(imageID) : undefined
      const imageDisplay = imageInfo
        ? imageInfo.version
          ? `${imageInfo.title} ${imageInfo.version}`
          : imageInfo.title
        : imageID
      return (
        <div className="flex flex-col gap-1.5">
          {asText(payload.image_id) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.image')}
              </span>{' '}
              <span className="text-neutral-2">{imageDisplay}</span>
            </div>
          )}
          {asText(payload.target_device) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.targetDevice')}
              </span>{' '}
              <span className="text-neutral-2 font-mono">
                {asText(asText(payload.target_device))}
              </span>
            </div>
          )}
          {payload.reboot !== undefined && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.reboot')}
              </span>{' '}
              <span className="text-neutral-2">
                {payload.reboot
                  ? t('widgets.sidebars.node.commandHistory.yes')
                  : t('widgets.sidebars.node.commandHistory.no')}
              </span>
            </div>
          )}
        </div>
      )
    }

    case 'install_agent':
      return (
        <div className="flex flex-col gap-1.5">
          {asText(payload.target_device) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.targetDevice')}
              </span>{' '}
              <span className="text-neutral-2 font-mono">
                {asText(asText(payload.target_device))}
              </span>
            </div>
          )}
          {asText(payload.agent_version) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.agentVersion')}
              </span>{' '}
              <span className="text-neutral-2">
                {asText(asText(payload.agent_version))}
              </span>
            </div>
          )}
          {payload.root_partition !== undefined && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.rootPartition')}
              </span>{' '}
              <span className="text-neutral-2">
                {asText(asText(payload.root_partition))}
              </span>
            </div>
          )}
        </div>
      )

    case 'provision_connection':
      return (
        <div className="flex flex-col gap-1.5">
          {asText(payload.target_device) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.targetDevice')}
              </span>{' '}
              <span className="text-neutral-2 font-mono">
                {asText(asText(payload.target_device))}
              </span>
            </div>
          )}
          {asText(payload.username) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.username')}
              </span>{' '}
              <span className="text-neutral-2">
                {asText(asText(payload.username))}
              </span>
            </div>
          )}
          {asText(payload.password) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.password')}
              </span>{' '}
              <span className="text-neutral-2">••••••••</span>
            </div>
          )}
          {asText(payload.ssh_key) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.sshKey')}
              </span>{' '}
              <span className="text-neutral-2 font-mono break-all">
                {String(payload.ssh_key).substring(0, 60)}
                {String(payload.ssh_key).length > 60 ? '...' : ''}
              </span>
            </div>
          )}
          {payload.root_partition !== undefined && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.rootPartition')}
              </span>{' '}
              <span className="text-neutral-2">
                {asText(asText(payload.root_partition))}
              </span>
            </div>
          )}
        </div>
      )

    case 'apply_utility_config': {
      // Named rather than left to the generic fallback: the payload carries a
      // params_json blob per utility, and dumping it produced an unreadable
      // wall of JSON that overflowed the card.
      //
      // The params are deliberately not shown. They are visible where they are
      // EDITED — the utility's own form and the node's utility section — and a
      // history card is a record of what was dispatched, which the utility ids
      // and the config hash already answer. (Utility params are readable by
      // anyone who can view the node; the Tailscale entry says so in its own
      // description. Not repeating a credential into a second surface is
      // hygiene, not a permission boundary.)
      const applied = asList(payload.utilities as unknown)
      const ids = Array.isArray(payload.utilities)
        ? (payload.utilities as unknown[])
            .map((u) =>
              u && typeof u === 'object'
                ? asText((u as Record<string, unknown>).utility_id)
                : '',
            )
            .filter(Boolean)
        : applied
      const unionHash = asText(payload.union_hash)
      return (
        <div className="flex flex-col gap-1.5">
          {ids.length > 0 && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.utilities')}
              </span>
              <div className="mt-1 flex flex-wrap gap-1">
                {ids.map((id) => (
                  <span
                    key={id}
                    className="inline-flex items-center px-2 py-0.5 rounded border border-neutral-4/50 text-neutral-4 bg-transparent text-xs"
                  >
                    {id}
                  </span>
                ))}
              </div>
            </div>
          )}
          {unionHash && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.configHash')}
              </span>{' '}
              <span className="text-neutral-2 font-mono break-all">
                {unionHash.slice(0, 12)}
              </span>
            </div>
          )}
        </div>
      )
    }

    case 'update_boot_order':
      return (
        <div className="flex flex-col gap-1.5">
          {asList(payload.boot_order).length > 0 && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.bootOrder')}
              </span>
              <div className="mt-1 flex flex-wrap gap-1">
                {asList(payload.boot_order).map((device, index) => (
                  <span
                    // Boot order IS positional: the row shows "1. nvme, 2. sd"
                    // and the same device may appear twice, so the index is
                    // the identity here rather than a stand-in for one.
                    // eslint-disable-next-line react/no-array-index-key
                    key={`${index}-${device}`}
                    className="inline-flex items-center px-2 py-0.5 rounded border border-neutral-4/50 text-neutral-4 bg-transparent text-xs"
                  >
                    {index + 1}. {device}
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>
      )

    case 'update_file':
      return (
        <div className="flex flex-col gap-1.5">
          {asText(payload.path) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.path')}
              </span>{' '}
              <span className="text-neutral-2 font-mono">
                {asText(payload.path)}
              </span>
            </div>
          )}
          {asText(payload.content) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.contentLength')}
              </span>{' '}
              <span className="text-neutral-2">
                {t('widgets.sidebars.node.commandHistory.characters', {
                  chars: asText(payload.content).length,
                })}
              </span>
            </div>
          )}
          {asText(payload.hash) && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.hash')}
              </span>{' '}
              <span className="text-neutral-2 font-mono text-xs">
                {asText(payload.hash)}
              </span>
            </div>
          )}
          {payload.version !== undefined && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.version')}
              </span>{' '}
              <span className="text-neutral-2">{asText(payload.version)}</span>
            </div>
          )}
        </div>
      )

    case 'reboot':
    case 'shutdown':
      return (
        <div className="flex flex-col gap-1.5">
          {payload.delay_seconds !== undefined && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.delay')}
              </span>{' '}
              <span className="text-neutral-2">
                {t('widgets.sidebars.node.commandHistory.seconds', {
                  seconds: payload.delay_seconds,
                })}
              </span>
            </div>
          )}
        </div>
      )

    case 'run_script': {
      // A recognised action (e.g. our DHCP renew) shows its purpose instead of
      // the raw shell; an ad-hoc script still shows the source.
      const action = knownScriptAction(asText(payload.script))
      if (action) {
        return (
          <div className="text-xs text-neutral-4">{action.description}</div>
        )
      }
      const timeout = scriptTimeoutSeconds(payload.timeout_seconds)
      return (
        <div className="flex flex-col gap-1.5">
          {asText(payload.script) && (
            <div className="text-xs min-w-0">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.script')}
              </span>
              <pre className="mt-0.5 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-neutral-15/60 p-2 font-mono text-xs text-neutral-2">
                {asText(payload.script).slice(0, 2000)}
                {asText(payload.script).length > 2000 ? '…' : ''}
              </pre>
            </div>
          )}
          {timeout !== null && (
            <div className="text-xs">
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.commandHistory.timeout')}
              </span>{' '}
              <span className="text-neutral-2">
                {t('widgets.sidebars.node.commandHistory.timeoutValue', {
                  value: timeout,
                })}
              </span>
            </div>
          )}
        </div>
      )
    }

    default:
      // Generic fallback for unknown command types.
      //
      // break-all, not break-words: a stringified object is one run of
      // characters with no spaces to break at, so break-words leaves it intact
      // and it pushes the card wider than the panel — the whole history then
      // scrolls sideways and the left edge of every card is clipped. Seen with
      // apply_utility_config before it had a case of its own.
      return (
        <div className="flex flex-col gap-1.5">
          {Object.entries(payload).map(([key, value]) => (
            <div key={key} className="text-xs min-w-0">
              <span className="text-neutral-7 capitalize">
                {key.replace(/_/g, ' ')}:
              </span>{' '}
              <span className="text-neutral-2 break-all">
                {typeof value === 'object'
                  ? JSON.stringify(value)
                  : String(value)}
              </span>
            </div>
          ))}
        </div>
      )
  }
}

export const CommandHistory: FC<CommandHistoryProps> = ({ commands }) => {
  const { images } = useImages()

  // Extract unique image IDs from install_os commands
  const imageIds = useMemo(() => {
    const ids = new Set<string>()
    commands.forEach((cmd) => {
      if (cmd.slug === 'install_os' && cmd.payload?.image_id) {
        ids.add(asText(cmd.payload.image_id))
      }
    })
    return Array.from(ids)
  }, [commands])

  // Pre-fetch all images on mount
  useEffect(() => {
    fetchImagesFx()
  }, [])

  // Fetch individual images that might not be in the list
  useEffect(() => {
    imageIds.forEach((imageId) => {
      const imageExists = images.some((img) => img.id === imageId)
      if (!imageExists) {
        fetchImageFx(imageId)
      }
    })
  }, [imageIds, images])

  // Create image ID to image object mapping
  const imageMap = useMemo(() => {
    const map = new Map<string, { title: string; version?: string }>()
    images.forEach((img) => {
      map.set(img.id, {
        title: img.title || img.id,
        version: img.version,
      })
    })
    return map
  }, [images])

  if (commands.length === 0) {
    return (
      <div className="py-8 text-center text-neutral-6 text-sm">
        {t('widgets.sidebars.node.commandHistory.empty')}
      </div>
    )
  }

  const shown = commands.slice(0, MAX_HISTORY)

  return (
    <div className="flex flex-col gap-3 overflow-y-auto max-h-[calc(100vh-200px)] [&::-webkit-scrollbar]:w-2 [&::-webkit-scrollbar-track]:bg-neutral-14 [&::-webkit-scrollbar-thumb]:bg-neutral-8 [&::-webkit-scrollbar-thumb]:rounded-full [&::-webkit-scrollbar-thumb]:border-2 [&::-webkit-scrollbar-thumb]:border-neutral-14">
      {shown.map((command) => (
        <CommandCard key={command.id} command={command} imageMap={imageMap} />
      ))}
      <div className="px-1 py-2 text-center text-xxs text-neutral-6 leading-relaxed">
        {t('widgets.sidebars.node.commandHistory.footer', { max: MAX_HISTORY })}{' '}
        <span className="font-mono text-neutral-5">
          journalctl -u orchestrator-agent
        </span>
        .
      </div>
    </div>
  )
}

// CommandCard renders one command. A reboot reports 100% ("Restart initiated")
// then goes silent until the node reconnects, so its raw progress sticks at
// 100%. For a reboot in progress we instead show a time-based estimate over the
// expected reboot window (capped below 100% until it actually completes).
const CommandCard: FC<{
  command: NodeCommand
  imageMap: Map<string, { title: string; version?: string }>
}> = ({ command, imageMap }) => {
  const statusConfig = getStatusColor(command.status)
  const commandName = commandTitle(command)
  const isReboot = command.slug === 'reboot'
  const rebooting = isReboot && command.status === 'in_progress'
  const timed = useTimedProgress(
    rebooting ? command.created_at : undefined,
    REBOOT_EXPECTED_MS,
  )
  const displayProgress = rebooting
    ? Math.min(99, Math.round(timed * 100))
    : command.progress
  const showProgress =
    command.status === 'in_progress' && (rebooting || command.progress > 0)

  return (
    <div
      className={clsx(
        'rounded-lg border p-4 transition-colors',
        statusConfig.bgColor,
        statusConfig.textColor,
        statusConfig.borderColor,
      )}
    >
      {/* Title | Status + Time stacked vertically on right */}
      <div className="flex items-start justify-between gap-4 mb-3">
        <div className="font-medium flex-1 min-w-0">{commandName}</div>
        <div className="flex flex-col items-end gap-1 flex-shrink-0">
          <div className="flex items-center gap-2">
            <span className="text-xs uppercase font-medium">
              {command.status}
            </span>
            {showProgress && (
              <span className="text-xs font-medium">{displayProgress}%</span>
            )}
          </div>
          <span className="text-xs opacity-60">
            {formatDate(command.updated_at)}
          </span>
        </div>
      </div>

      {/* Details below */}
      {command.details && (
        <div className="mb-3">
          <div className="text-xs opacity-80 break-words">
            {command.details}
          </div>
        </div>
      )}

      {failedInstallStaysOnRecovery(command) && (
        <div className="mb-3 text-xs">
          {t('widgets.sidebars.node.commandHistory.installFailedRecovery')}
        </div>
      )}

      {/* What the agent reported back. Shown for a command that did NOT
          succeed, because that is when the difference between the summary and
          the detail matters: a utility apply reports "failed utilities:
          tailscale" in details while result carries the tool's own words —
          "To approve your machine, visit … https://login.tailscale.com/admin".
          Without this the operator is told WHICH utility failed and never why.
          A successful run_script shows it too: its output is the reason it
          ran. Long output starts collapsed.

          Values are placeholder-redacted for a caller without
          secret.read_value and the keys always survive, so a viewer still
          learns which utility failed. */}
      {showsReportedOutput(command) && command.result && (
        <ReportedOutput result={command.result} />
      )}

      {/* Parameters - structured per command type */}
      {command.payload && Object.keys(command.payload).length > 0 && (
        <div className="mb-3">
          <div className="text-xs font-medium text-neutral-7 uppercase mb-2">
            {t('widgets.sidebars.node.commandHistory.parameters')}
          </div>
          {formatCommandParams(command.slug, command.payload, imageMap)}
        </div>
      )}

      {/* Progress Bar */}
      {showProgress && (
        <div className="mt-2 h-1 bg-neutral-12 rounded-full overflow-hidden">
          <div
            className="h-full bg-yellow-5 transition-all duration-300"
            style={{ width: `${displayProgress}%` }}
          />
        </div>
      )}
    </div>
  )
}

const ReportedOutput: FC<{ result: Record<string, string> }> = ({ result }) => {
  const entries = (
    <div className="flex flex-col gap-1.5">
      {Object.entries(result)
        // union_hash and friends are bookkeeping, not an outcome.
        .filter(([key]) => key !== 'union_hash')
        .map(([key, value]) => (
          <div key={key} className="text-xs min-w-0">
            <span className="text-neutral-7 font-mono">{key}</span>
            <pre className="mt-0.5 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-neutral-15/60 p-2 font-mono text-xs text-neutral-3">
              {value}
            </pre>
          </div>
        ))}
    </div>
  )
  const heading = t('widgets.sidebars.node.commandHistory.reported')
  if (isLongOutput(result)) {
    return (
      <details className="mb-3">
        <summary className="cursor-pointer text-xs font-medium text-neutral-7 uppercase mb-2">
          {heading}
        </summary>
        {entries}
      </details>
    )
  }
  return (
    <div className="mb-3">
      <div className="text-xs font-medium text-neutral-7 uppercase mb-2">
        {heading}
      </div>
      {entries}
    </div>
  )
}
