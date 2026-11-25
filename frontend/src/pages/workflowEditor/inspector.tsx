import { t } from 'i18next'
import { useTranslation } from 'react-i18next'

import type {
  WorkflowBlock,
  WorkflowBlockPolicy,
} from '@/entities/workflow/api/types'
import { CommandInputs } from '@/entities/workflow/ui/workflowEditor/commandInputs'

import { InfoHint } from '@/shared/ui/infoHint'

import {
  isTrigger,
  SCHEDULE_TYPE,
  START_TYPE,
  type BlockNode,
} from './graphUtils'

type InspectorProps = {
  node: BlockNode
  block?: WorkflowBlock
  onChange: (data: Partial<BlockNode['data']>) => void
  onDelete: () => void
  // A DRAFT is a block chosen in the palette and not yet placed. It is the same
  // panel — the block's description and its parameters are the same facts
  // either way, and a second panel that showed them read-only was duplication
  // the author had to close by hand. The only difference is the footer: a draft
  // is added, a placed block is deleted. Filling the parameters in before
  // placing is a free consequence.
  mode?: 'placed' | 'draft'
  onAdd?: () => void
}

// BOOT_LABELS name the CONSEQUENCE. "Applied after the OS boots" is not a
// warning — the platform holds that command and delivers it later, and the run
// is correct either way; saying "needs the OS" there would send an operator
// looking for a problem that does not exist.
const bootLabel = (
  requirement: string,
): { text: string; tone: string } | undefined => {
  const labels: Record<string, { text: string; tone: string }> = {
    managed_os: {
      text: t('pages.workflowEditor.inspector.bootLabel.managed_os'),
      tone: 'border-yellow-5/40 text-yellow-5',
    },
    recovery: {
      text: t('pages.workflowEditor.inspector.bootLabel.recovery'),
      tone: 'border-yellow-5/40 text-yellow-5',
    },
    deferred_to_managed_os: {
      text: t(
        'pages.workflowEditor.inspector.bootLabel.deferred_to_managed_os',
      ),
      tone: 'border-neutral-11 text-neutral-6',
    },
  }
  return labels[requirement]
}

const BlockBadges = ({ block }: { block: WorkflowBlock }) => {
  const { t } = useTranslation()
  const boot = block.boot_requirement
    ? bootLabel(block.boot_requirement)
    : undefined
  if (!boot && !block.dangerous && !block.reboots_node && !block.boot_note)
    return null
  return (
    <div className="flex flex-col gap-1 mt-2">
      <div className="flex flex-wrap gap-1">
        {block.dangerous && (
          <span className="text-[10px] uppercase px-1.5 py-0.5 rounded border border-red-6/40 text-red-6">
            {t('pages.workflowEditor.inspector.destructive')}
          </span>
        )}
        {block.reboots_node && (
          <span className="text-[10px] uppercase px-1.5 py-0.5 rounded border border-yellow-5/40 text-yellow-5">
            {t('pages.workflowEditor.inspector.rebootsNode')}
          </span>
        )}
        {boot && (
          <span
            className={`text-[10px] uppercase px-1.5 py-0.5 rounded border ${boot.tone}`}
          >
            {boot.text}
          </span>
        )}
      </div>
      {block.boot_note && (
        <div className="text-xs text-neutral-6">{block.boot_note}</div>
      )}
    </div>
  )
}

const inputCls =
  'px-2 py-1 bg-neutral-12 border border-neutral-11 rounded text-xs text-neutral-2 focus:outline-none focus:border-purple-5'

// Right panel: name/description, registry-generated params, and (for agent
// blocks) an execution policy. Triggers get a type switch + their params.
export const BlockInspector = ({
  node,
  block,
  onChange,
  onDelete,
  mode = 'placed',
  onAdd,
}: InspectorProps) => {
  const { t } = useTranslation()
  const trigger = isTrigger(node.data.blockType)
  const policy = node.data.policy || {}

  const setPolicy = (patch: Partial<WorkflowBlockPolicy>) => {
    onChange({ policy: { ...policy, ...patch } })
  }

  return (
    <div className="w-80 shrink-0 border-l border-neutral-11 overflow-y-auto p-4 flex flex-col gap-4">
      <div>
        <div className="text-xs font-bold uppercase text-neutral-7">
          {block?.name || node.data.blockType}
        </div>
        {(block?.description || block?.details) && (
          <div className="text-xs text-neutral-6 mt-1">
            {block?.description}
            {block?.details && (
              <span className="whitespace-nowrap">
                &nbsp;
                <InfoHint text={block.details} />
              </span>
            )}
          </div>
        )}
        {block && <BlockBadges block={block} />}
      </div>

      {trigger ? (
        <>
          <div className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {t('pages.workflowEditor.inspector.triggerLabel')}
            </label>
            <select
              value={node.data.blockType}
              onChange={(e) =>
                onChange({ blockType: e.target.value, params: {} })
              }
              className={inputCls}
            >
              <option value={START_TYPE}>
                {t('pages.workflowEditor.inspector.triggerManual')}
              </option>
              <option value={SCHEDULE_TYPE}>
                {t('pages.workflowEditor.inspector.triggerSchedule')}
              </option>
            </select>
          </div>
          {/* Schedule cron etc. come from the trigger block's registry params */}
          {block && block.params.length > 0 && (
            <CommandInputs
              command={{ slug: '', payload: node.data.params }}
              payload={node.data.params}
              block={block}
              onPayloadChange={(params) => onChange({ params })}
            />
          )}
          <div className="text-xs text-neutral-6">
            {node.data.blockType === SCHEDULE_TYPE
              ? t('pages.workflowEditor.inspector.scheduleHint')
              : t('pages.workflowEditor.inspector.manualHint')}
          </div>
        </>
      ) : (
        <>
          <div className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {t('pages.workflowEditor.inspector.stepNameLabel')}
            </label>
            <input
              type="text"
              value={node.data.name || ''}
              onChange={(e) => onChange({ name: e.target.value })}
              className={inputCls}
              placeholder={block?.name}
            />
          </div>
          <div className="flex flex-col gap-1">
            <label className="text-xs text-neutral-7">
              {t('pages.workflowEditor.inspector.descriptionLabel')}
            </label>
            <textarea
              value={node.data.description || ''}
              onChange={(e) => onChange({ description: e.target.value })}
              className={`${inputCls} resize-none`}
              rows={2}
            />
          </div>

          {block && block.params.length > 0 && (
            <div className="flex flex-col gap-2 border-t border-neutral-11 pt-3">
              <div className="text-xs font-medium text-neutral-7 uppercase">
                {t('pages.workflowEditor.inspector.parameters')}
              </div>
              <CommandInputs
                command={{ slug: block.slug || '', payload: node.data.params }}
                payload={node.data.params}
                block={block}
                onPayloadChange={(params) => onChange({ params })}
              />
            </div>
          )}

          {/* Execution policy applies to any executable block. */}
          <div className="flex flex-col gap-2 border-t border-neutral-11 pt-3">
            <div className="text-xs font-medium text-neutral-7 uppercase">
              {t('pages.workflowEditor.inspector.policy')}
            </div>
            <div className="flex gap-2">
              <div className="flex flex-col gap-1 flex-1">
                <label className="text-xs text-neutral-7">
                  {t('pages.workflowEditor.inspector.retries')}
                </label>
                <input
                  type="number"
                  min="0"
                  max="10"
                  value={policy.retries ?? ''}
                  onChange={(e) =>
                    setPolicy({
                      retries:
                        e.target.value === ''
                          ? undefined
                          : Number(e.target.value),
                    })
                  }
                  className={inputCls}
                  placeholder="0"
                />
              </div>
              {/* A server block runs on the platform and has no device
                  timeout to set; the field would be ignored. */}
              {block?.executor !== 'server' && (
                <div className="flex flex-col gap-1 flex-1">
                  <label className="text-xs text-neutral-7">
                    {t('pages.workflowEditor.inspector.timeout')}
                  </label>
                  <input
                    type="number"
                    min="0"
                    value={policy.timeout_seconds ?? ''}
                    onChange={(e) =>
                      setPolicy({
                        timeout_seconds:
                          e.target.value === ''
                            ? undefined
                            : Number(e.target.value),
                      })
                    }
                    className={inputCls}
                    placeholder="0"
                  />
                </div>
              )}
            </div>
            <div className="flex flex-col gap-1">
              <label className="text-xs text-neutral-7">
                {t('pages.workflowEditor.inspector.onError')}
              </label>
              <select
                value={policy.on_error || 'fail'}
                onChange={(e) =>
                  setPolicy({
                    on_error: e.target.value as WorkflowBlockPolicy['on_error'],
                  })
                }
                className={inputCls}
              >
                <option value="fail">
                  {t('pages.workflowEditor.inspector.onErrorFail')}
                </option>
                <option value="continue">
                  {t('pages.workflowEditor.inspector.onErrorContinue')}
                </option>
              </select>
            </div>
          </div>

          {mode === 'draft' ? (
            <button
              type="button"
              onClick={onAdd}
              className="mt-auto text-xs uppercase text-lime-6 border border-lime-6/40 hover:border-lime-6/70 rounded px-2.5 py-1.5 transition-colors"
            >
              {t('pages.workflowEditor.inspector.addToCanvas')}
            </button>
          ) : (
            <button
              type="button"
              onClick={onDelete}
              className="mt-auto text-left text-xs text-red-6 hover:text-red-5 border border-red-6/30 hover:border-red-6/60 rounded px-2.5 py-1.5 transition-colors"
            >
              {t('pages.workflowEditor.inspector.deleteBlock')}
            </button>
          )}
        </>
      )}
    </div>
  )
}
