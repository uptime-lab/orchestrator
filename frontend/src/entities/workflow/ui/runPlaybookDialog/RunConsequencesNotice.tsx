import { FC, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { WorkflowTemplate } from '../../api/types'
import { useWorkflowBlocks } from '../../model'
import { runConsequences, runGate, type RunConsequences } from './consequences'

// What starting a run of `template` will do, and whether it may start yet.
// Shared by every place a run is started, so the consequences and the
// acknowledgement rule are the same wherever the operator starts it.
export const useRunConsequences = (template: WorkflowTemplate | null) => {
  const { blocks, fetchBlocks } = useWorkflowBlocks()
  const [acknowledged, setAcknowledged] = useState(false)

  // The registry is what names the consequences; a page that has not loaded
  // it yet must load it, not read its absence as "harmless".
  useEffect(() => {
    if (template && blocks.length === 0) void fetchBlocks()
  }, [template, blocks.length, fetchBlocks])

  // A tick given for one playbook never carries over to another.
  useEffect(() => {
    setAcknowledged(false)
  }, [template?.id])

  const consequences = useMemo<RunConsequences>(() => {
    const graph = template?.graph
    if (!graph || blocks.length === 0) return { dangerous: [], reboots: [] }
    const byType = Object.fromEntries(blocks.map((b) => [b.type, b]))
    return runConsequences(
      graph.nodes.map((graphNode) => graphNode.type),
      byType,
    )
  }, [template, blocks])

  const gate = runGate(blocks.length > 0, consequences, acknowledged)
  return { consequences, gate, acknowledged, setAcknowledged }
}

type RunConsequencesNoticeProps = {
  consequences: RunConsequences
  gate: ReturnType<typeof runGate>
  acknowledged: boolean
  onAcknowledgedChange: (value: boolean) => void
  disabled?: boolean
}

// The consequences box with its acknowledgement. Not a second dialog: the
// checkbox sits beside what it acknowledges, where a confirm-modal would
// replace it with its own text.
export const RunConsequencesNotice: FC<RunConsequencesNoticeProps> = ({
  consequences,
  gate,
  acknowledged,
  onAcknowledgedChange,
  disabled,
}) => {
  const { t } = useTranslation()
  if (gate === 'loading') {
    return (
      <div className="text-xs text-neutral-6">
        {t('entities.workflow.runPlaybookDialog.readingBlocks')}
      </div>
    )
  }
  if (consequences.dangerous.length === 0 && consequences.reboots.length === 0)
    return null
  return (
    <div className="flex flex-col gap-2 rounded border border-amber-7/50 bg-amber-6/10 px-3 py-2 text-xs">
      {consequences.dangerous.length > 0 && (
        <div className="text-amber-5">
          {t('entities.workflow.runPlaybookDialog.dangerous', {
            blocks: consequences.dangerous.join(', '),
          })}
        </div>
      )}
      {consequences.reboots.length > 0 && (
        <div className="text-neutral-4">
          {t('entities.workflow.runPlaybookDialog.reboots', {
            blocks: consequences.reboots.join(', '),
          })}
        </div>
      )}
      {consequences.dangerous.length > 0 && (
        <label className="flex items-center gap-2 text-neutral-3 cursor-pointer">
          <input
            type="checkbox"
            checked={acknowledged}
            onChange={(e) => onAcknowledgedChange(e.target.checked)}
            disabled={disabled}
          />
          {t('entities.workflow.runPlaybookDialog.acknowledge')}
        </label>
      )}
    </div>
  )
}
