import clsx from 'clsx'
import { t } from 'i18next'
import { useCallback, useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import type { BootOrder } from '@/entities/node/api/types'
import { imagesModel } from '@/entities/osImage'
import { useImages } from '@/entities/osImage/hooks'
import { sshKeysModel } from '@/entities/sshKey'
import { useSSHKeys } from '@/entities/sshKey/hooks'
import type {
  CommandSpec,
  WorkflowBlock,
  WorkflowBlockParam,
} from '@/entities/workflow/api/types'
import { useWorkflowBlocks } from '@/entities/workflow/model'

import { BootOrderSelector } from '@/shared/ui/bootOrderSelector'
import { CodeEditor, type CodeLanguage } from '@/shared/ui/codeEditor'
import { InfoHint } from '@/shared/ui/infoHint'

import { CronField } from './cronField'

export type CommandInputsProps = {
  command: CommandSpec
  payload: Record<string, unknown>
  onPayloadChange: (payload: Record<string, unknown>) => void
  disabled?: boolean
  // Explicit block definition — supplied for server/control blocks that have
  // no node-command slug to look up. Falls back to slug lookup when omitted.
  block?: WorkflowBlock
}

const inputClassName =
  'px-2 py-1 bg-neutral-12 border border-neutral-11 rounded text-xs text-neutral-2 focus:outline-none focus:border-purple-5'

const bootOrderName = (item: string): string => {
  const names: Record<string, string> = {
    disk: t('entities.workflow.commandInputs.bootOrder.disk'),
    network: t('entities.workflow.commandInputs.bootOrder.network'),
    cdrom: t('entities.workflow.commandInputs.bootOrder.cdrom'),
  }
  return names[item] || item
}

// Which highlighter a text param gets. Keyed on the param NAME, which is
// stable across blocks (run_script.script, manage_service.unit_content);
// everything else renders as plain monospace.
const codeLanguageFor = (paramName: string): CodeLanguage => {
  switch (paramName) {
    case 'script':
      return 'shell'
    case 'unit_content':
      return 'ini'
    default:
      return 'plain'
  }
}

const asString = (value: unknown): string =>
  typeof value === 'string' ? value : ''
const asNumber = (value: unknown): number | '' =>
  typeof value === 'number' ? value : ''
const asBool = (value: unknown): boolean => value === true

// Renders the parameter form for a workflow step. Fields are generated from
// the block registry served by the backend, so new block types get forms
// without frontend changes.
export const CommandInputs = ({
  command,
  payload,
  onPayloadChange,
  disabled = false,
  block: blockProp,
}: CommandInputsProps) => {
  const { t } = useTranslation()
  const { blocks, blocksBySlug, fetchBlocks } = useWorkflowBlocks()
  const { images } = useImages()
  const { keys } = useSSHKeys()

  const block = blockProp ?? blocksBySlug[command.slug]
  const needsImages = block?.params.some((p) => p.type === 'os_image')
  const needsKeys = block?.params.some((p) => p.type === 'ssh_key')

  useEffect(() => {
    if (blocks.length === 0) {
      fetchBlocks()
    }
  }, [blocks.length, fetchBlocks])

  // Fetch referenced entities if not loaded (needed for both edit and read-only modes)
  useEffect(() => {
    if (needsImages && images.length === 0) {
      imagesModel.fetchImagesFx()
    }
    if (needsKeys && keys.length === 0) {
      sshKeysModel.fetchSSHKeysFx()
    }
  }, [needsImages, needsKeys, images.length, keys.length])

  const updatePayload = useCallback(
    (key: string, value: unknown) => {
      onPayloadChange({
        ...payload,
        [key]: value,
      })
    },
    [payload, onPayloadChange],
  )

  const removePayloadKey = useCallback(
    (key: string) => {
      const newPayload = { ...payload }
      delete newPayload[key]
      onPayloadChange(newPayload)
    },
    [payload, onPayloadChange],
  )

  if (!block) {
    return (
      <div className="text-xs text-neutral-6 italic">
        {blocks.length === 0
          ? t('entities.workflow.commandInputs.loadingBlock')
          : t('entities.workflow.commandInputs.unknownCommand', {
              slug: command.slug,
            })}
      </div>
    )
  }

  if (block.params.length === 0) {
    return (
      <div className="text-xs text-neutral-6 italic">
        {t('entities.workflow.commandInputs.noParams')}
      </div>
    )
  }

  const displayValue = (param: WorkflowBlockParam): string | undefined => {
    const value = payload[param.name]
    if (value === undefined || value === null || value === '') {
      return undefined
    }
    switch (param.type) {
      case 'bool':
        return value
          ? t('entities.workflow.commandInputs.yes')
          : t('entities.workflow.commandInputs.no')
      case 'os_image': {
        const image = images.find((img) => img.id === value)
        return image
          ? `${image.title}${image.version ? ` (${image.version})` : ''}${image.kind && image.kind !== 'os' ? ` — ${image.kind}` : ''}`
          : String(value)
      }
      case 'ssh_key': {
        const key = keys.find((k) => k.id === value)
        return key
          ? `${key.alias} (${key.fingerprint.substring(0, 16)}...)`
          : String(value)
      }
      case 'boot_order': {
        const bootOrder = (value as string[]) || []
        return bootOrder.length > 0
          ? bootOrder.map((item) => bootOrderName(item)).join(' → ')
          : undefined
      }
      default:
        return String(value)
    }
  }

  if (disabled) {
    return (
      <div className="flex flex-col gap-2">
        {block.params.map((param) => {
          const value = displayValue(param)
          if (value === undefined && !param.required) {
            return null
          }
          if (param.type === 'text') {
            return (
              <div key={param.name} className="flex flex-col gap-1">
                <label className="text-xs font-medium text-neutral-7">
                  {param.label}
                </label>
                <div className="text-xs text-neutral-2 font-mono bg-neutral-12 p-2 rounded border border-neutral-11 whitespace-pre-wrap max-h-32 overflow-y-auto">
                  {asString(payload[param.name]) ||
                    t('entities.workflow.commandInputs.notSet')}
                </div>
              </div>
            )
          }
          return (
            <div key={param.name} className="flex flex-col gap-1">
              <label className="text-xs font-medium text-neutral-7">
                {param.label}
              </label>
              <div
                className={
                  value === undefined
                    ? 'text-sm text-neutral-6 italic'
                    : 'text-sm text-neutral-2'
                }
              >
                {value ?? t('entities.workflow.commandInputs.notSet')}
              </div>
            </div>
          )
        })}
      </div>
    )
  }

  const renderParam = (param: WorkflowBlockParam) => {
    const label = param.required
      ? t('entities.workflow.commandInputs.labelRequired', {
          label: param.label,
        })
      : t('entities.workflow.commandInputs.labelOptional', {
          label: param.label,
        })
    const value = payload[param.name]

    switch (param.type) {
      case 'bool':
        return (
          <div key={param.name} className="flex items-center gap-2">
            <input
              type="checkbox"
              // A param the playbook does not set takes the registry
              // default on the server, so that is what the box shows.
              checked={
                value === undefined ? param.default === true : asBool(value)
              }
              onChange={(e) => updatePayload(param.name, e.target.checked)}
              className="w-3 h-3 text-purple-5 bg-neutral-12 border-neutral-11 rounded focus:ring-purple-5"
            />
            <label className="text-xs text-neutral-7">{param.label}</label>
            {param.details && (
              <InfoHint text={param.details} className="ml-1" />
            )}
          </div>
        )

      case 'int':
        return (
          <div key={param.name} className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {label}
              {param.details && (
                <InfoHint text={param.details} className="ml-1" />
              )}
            </label>
            <input
              type="number"
              value={asNumber(value)}
              onChange={(e) => {
                if (e.target.value === '') {
                  removePayloadKey(param.name)
                } else {
                  updatePayload(param.name, parseInt(e.target.value, 10) || 0)
                }
              }}
              className={inputClassName}
              placeholder={
                param.placeholder ??
                (typeof param.default === 'number'
                  ? String(param.default)
                  : undefined)
              }
              min={param.min}
              max={param.max}
            />
          </div>
        )

      case 'text':
        // Script-shaped params get a code editor instead of a bare textarea.
        // It commits on blur/debounce, never per keystroke — the editor page
        // re-serializes the whole graph on every payload change (isDirty), so
        // a per-keystroke commit makes a large script janky.
        if (disabled) {
          return (
            <div key={param.name} className="flex flex-col gap-1">
              <label className="text-xs text-neutral-7">
                {label}
                {param.details && (
                  <InfoHint text={param.details} className="ml-1" />
                )}
              </label>
              <pre
                className={`${inputClassName} max-h-64 overflow-auto whitespace-pre-wrap font-mono`}
              >
                {asString(value)}
              </pre>
            </div>
          )
        }
        return (
          <div key={param.name} className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {label}
              {param.details && (
                <InfoHint text={param.details} className="ml-1" />
              )}
            </label>
            <CodeEditor
              value={asString(value)}
              language={codeLanguageFor(param.name)}
              placeholder={param.placeholder}
              onCommit={(next) => updatePayload(param.name, next)}
            />
          </div>
        )

      case 'enum':
        return (
          <div key={param.name} className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {label}
              {param.details && (
                <InfoHint text={param.details} className="ml-1" />
              )}
            </label>
            <select
              value={asString(value)}
              onChange={(e) => updatePayload(param.name, e.target.value)}
              className={inputClassName}
            >
              <option value="">
                {t('entities.workflow.commandInputs.selectPlaceholder')}
              </option>
              {(param.enum_values || []).map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
          </div>
        )

      case 'os_image':
        return (
          <div key={param.name} className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {label}
              {param.details && (
                <InfoHint text={param.details} className="ml-1" />
              )}
            </label>
            <select
              value={asString(value)}
              onChange={(e) => {
                if (e.target.value === '') {
                  removePayloadKey(param.name)
                } else {
                  updatePayload(param.name, e.target.value)
                }
              }}
              className={inputClassName}
            >
              <option value="">
                {t('entities.workflow.commandInputs.selectOSImage')}
              </option>
              {images.map((image) => (
                <option key={image.id} value={image.id}>
                  {image.title} {image.version ? `(${image.version})` : ''}
                  {image.kind && image.kind !== 'os' ? ` — ${image.kind}` : ''}
                </option>
              ))}
            </select>
            {param.description && (
              <span className="text-xs text-neutral-6">
                {param.description}
              </span>
            )}
          </div>
        )

      case 'ssh_key':
        return (
          <div key={param.name} className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {label}
              {param.details && (
                <InfoHint text={param.details} className="ml-1" />
              )}
            </label>
            <select
              value={asString(value)}
              onChange={(e) => {
                if (e.target.value === '') {
                  removePayloadKey(param.name)
                } else {
                  updatePayload(param.name, e.target.value)
                }
              }}
              className={inputClassName}
            >
              <option value="">
                {t('entities.workflow.commandInputs.selectSSHKey')}
              </option>
              {keys.map((key) => (
                <option key={key.id} value={key.id}>
                  {key.alias} ({key.fingerprint.substring(0, 16)}...)
                </option>
              ))}
            </select>
            {param.description && (
              <span className="text-xs text-neutral-6">
                {param.description}
              </span>
            )}
          </div>
        )

      case 'boot_order':
        return (
          <div key={param.name} className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {label}
              {param.details && (
                <InfoHint text={param.details} className="ml-1" />
              )}
            </label>
            <BootOrderSelector
              value={(value as BootOrder) || []}
              onChange={(bootOrder) => updatePayload(param.name, bootOrder)}
              disabled={disabled}
            />
          </div>
        )

      case 'cron':
        return (
          <CronField
            key={param.name}
            label={label}
            value={asString(value)}
            description={param.description}
            details={param.details}
            placeholder={param.placeholder}
            inputClassName={inputClassName}
            onChange={(cron) => updatePayload(param.name, cron)}
          />
        )

      case 'string':
      case 'expr':
      default:
        return (
          <div key={param.name} className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {label}
              {param.details && (
                <InfoHint text={param.details} className="ml-1" />
              )}
            </label>
            <input
              type="text"
              value={asString(value)}
              onChange={(e) => updatePayload(param.name, e.target.value)}
              className={clsx(
                inputClassName,
                param.type === 'expr' && 'font-mono',
              )}
              placeholder={param.placeholder}
            />
            {param.description && (
              <span className="text-xs text-neutral-6">
                {param.description}
              </span>
            )}
          </div>
        )
    }
  }

  return (
    <div className="flex flex-col gap-2">{block.params.map(renderParam)}</div>
  )
}
