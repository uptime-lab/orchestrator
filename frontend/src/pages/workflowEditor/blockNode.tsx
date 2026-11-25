import { Handle, Position, type NodeProps } from '@xyflow/react'
import clsx from 'clsx'
import { memo } from 'react'
import { useTranslation } from 'react-i18next'

import { useWorkflowBlocks } from '@/entities/workflow/model'

import { isTrigger, type BlockNode } from './graphUtils'

const CATEGORY_COLORS: Record<string, string> = {
  triggers: 'bg-lime-6',
  power: 'bg-orange-6',
  diagnostics: 'bg-blue-6',
  os: 'bg-purple-5',
  software: 'bg-teal-6',
  system: 'bg-purple-5',
  boot: 'bg-blue-6',
  files: 'bg-green-6',
  storage: 'bg-red-6',
  control: 'bg-yellow-6',
  integrations: 'bg-pink-6',
}

// Port dot colors so condition true/false read at a glance.
const PORT_COLORS: Record<string, string> = {
  true: '!bg-green-6',
  false: '!bg-red-6',
}

export const BlockNodeView = memo(
  ({ data, selected }: NodeProps<BlockNode>) => {
    const { t } = useTranslation()
    const { blocks } = useWorkflowBlocks()
    const block = blocks.find((b) => b.type === data.blockType)

    if (isTrigger(data.blockType)) {
      const label =
        data.blockType === 'trigger.schedule'
          ? t('pages.workflowEditor.blockNode.schedule')
          : t('pages.workflowEditor.blockNode.start')
      return (
        <div
          className={clsx(
            'px-5 py-2 rounded-full border text-xs font-bold uppercase tracking-wider',
            'bg-neutral-12 text-lime-6',
            selected ? 'border-lime-6' : 'border-neutral-10',
          )}
        >
          {label}
          {/* id must match DEFAULT_PORT: graph edges carry sourceHandle "out",
            and React Flow silently drops edges whose handle id is missing —
            the start→first-block link would exist in the data but never render. */}
          <Handle
            id="out"
            type="source"
            position={Position.Bottom}
            className="!w-2.5 !h-2.5 !bg-lime-6 !border-neutral-13"
          />
        </div>
      )
    }

    const categoryColor =
      CATEGORY_COLORS[block?.category || ''] || 'bg-neutral-8'
    const title = data.name || block?.name || data.blockType
    const outputs = block?.outputs || [{ name: 'out' }]
    const multiPort = outputs.length > 1

    return (
      <div
        className={clsx(
          'w-52 rounded-lg border bg-neutral-13 shadow-lg',
          selected ? 'border-purple-5' : 'border-neutral-10',
          block?.dangerous && !selected && 'border-red-6/40',
        )}
      >
        <Handle
          type="target"
          position={Position.Top}
          className="!w-2.5 !h-2.5 !bg-neutral-7 !border-neutral-13"
        />
        <div className="flex items-center gap-2 px-3 py-2">
          <span
            className={clsx('w-2 h-2 rounded-full shrink-0', categoryColor)}
          />
          <div className="min-w-0">
            <div className="text-xs font-medium text-neutral-2 truncate">
              {title}
            </div>
            <div className="text-[10px] text-neutral-6 truncate">
              {block?.name && data.name ? block.name : block?.category || ''}
              {block?.dangerous && (
                <span className="text-red-6 ml-1 uppercase">
                  {t('pages.workflowEditor.palette.destructive')}
                </span>
              )}
            </div>
          </div>
        </div>
        {/* Output ports: single centered handle, or labeled handles for
          multi-port blocks (condition true/false). */}
        {multiPort ? (
          <div className="flex justify-around border-t border-neutral-11 px-2 py-1">
            {outputs.map((port, i) => (
              <div
                key={port.name}
                className="relative px-2 text-[10px] text-neutral-5 uppercase"
              >
                {port.label || port.name}
                <Handle
                  id={port.name}
                  type="source"
                  position={Position.Bottom}
                  style={{ left: `${((i + 0.5) / outputs.length) * 100}%` }}
                  className={clsx(
                    '!w-2.5 !h-2.5 !border-neutral-13 !bottom-0',
                    PORT_COLORS[port.name] || '!bg-neutral-7',
                  )}
                />
              </div>
            ))}
          </div>
        ) : (
          <Handle
            id="out"
            type="source"
            position={Position.Bottom}
            className="!w-2.5 !h-2.5 !bg-neutral-7 !border-neutral-13"
          />
        )}
      </div>
    )
  },
)

BlockNodeView.displayName = 'BlockNodeView'
