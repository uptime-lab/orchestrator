import { FC, useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'

import { Button } from '@/shared/ui/button/button.tsx'

import { SectionDivider } from './infoRow'

// Must match backend domain/node.MaxNotesLength. The textarea caps input so the
// operator hits the limit while typing rather than losing a long note to a 400.
const MAX_NOTES_LENGTH = 4096

// Free-form operator notes for a node, shown at the bottom of the Overview tab.
// Nothing on the device reads this — it is the one node field the fleet-list
// search box matches that the device never reports, so it is where rack
// position, owner or "NVMe swapped on <date>" belong.
export const NodeNotesSection: FC<{
  nodeId: string
  notes?: string
  editable?: boolean
}> = ({ nodeId, notes, editable = true }) => {
  const { t } = useTranslation()
  const current = notes || ''
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(current)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)

  // Node objects are also replaced wholesale by the node_updated websocket push,
  // so re-sync from the prop — but never while editing, or a heartbeat-driven
  // update would wipe what is being typed.
  useEffect(() => {
    if (!editing) setValue(current)
  }, [current, editing])

  const cancel = useCallback(() => {
    setEditing(false)
    setValue(current)
    setError(null)
  }, [current])

  const save = useCallback(async () => {
    const next = value.trim()
    if (next === current) {
      setEditing(false)
      setError(null)
      return
    }
    setSaving(true)
    setError(null)
    try {
      await apiNodes.putUpdateNode(nodeId, { notes: next })
      await nodesModel.fetchNodesFx()
      setEditing(false)
    } catch {
      setError(t('widgets.sidebars.node.nodeNotesSection.saveError'))
    } finally {
      setSaving(false)
    }
  }, [value, current, nodeId, t])

  const startEditing = useCallback(() => {
    setEditing(true)
    // Focus after the textarea mounts, caret at the end of the existing text.
    requestAnimationFrame(() => {
      const el = textareaRef.current
      if (!el) return
      el.focus()
      el.setSelectionRange(el.value.length, el.value.length)
    })
  }, [])

  return (
    <div className="flex flex-col gap-2">
      {/* Same full-width divider as Configuration and Live Metrics, so the
          section reads as one more block of the same table. The edit control
          sits on the content row instead of beside the label, the way the
          Copy buttons do on the rows above. */}
      <SectionDivider
        label={t('widgets.sidebars.node.nodeNotesSection.title')}
      />

      {editing ? (
        <>
          {/* Enter inserts a newline — notes are multi-line by nature — so the
              keyboard save is Cmd/Ctrl+Enter. */}
          <textarea
            ref={textareaRef}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') cancel()
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) save()
            }}
            disabled={saving}
            maxLength={MAX_NOTES_LENGTH}
            rows={5}
            placeholder={t(
              'widgets.sidebars.node.nodeNotesSection.placeholder',
            )}
            className="w-full resize-y rounded bg-neutral-13 px-2.5 py-2 font-mono text-sm text-neutral-2 outline-none placeholder:text-neutral-7 focus:ring-1 focus:ring-lime-6/50"
          />
          <div className="flex items-center gap-2">
            <Button size="sm" kind="secondary" onClick={save} disabled={saving}>
              {saving
                ? t('widgets.sidebars.node.nodeNotesSection.saving')
                : t('widgets.sidebars.node.nodeNotesSection.save')}
            </Button>
            <Button
              size="sm"
              kind="tertiary"
              onClick={cancel}
              disabled={saving}
            >
              {t('widgets.sidebars.node.nodeNotesSection.cancel')}
            </Button>
            <span className="ml-auto text-xs text-neutral-7">
              {value.length}/{MAX_NOTES_LENGTH}
            </span>
          </div>
        </>
      ) : (
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0 flex-1 whitespace-pre-wrap break-words text-sm text-neutral-3">
            {current || (
              <span className="text-neutral-7">
                {t('widgets.sidebars.node.nodeNotesSection.empty')}
              </span>
            )}
          </div>
          {editable && (
            <Button
              size="sm"
              kind="secondary"
              onClick={startEditing}
              title={
                current
                  ? t('widgets.sidebars.node.nodeNotesSection.editTitle')
                  : t('widgets.sidebars.node.nodeNotesSection.addTitle')
              }
              className="flex-shrink-0"
            >
              {current
                ? t('widgets.sidebars.node.nodeNotesSection.edit')
                : t('widgets.sidebars.node.nodeNotesSection.add')}
            </Button>
          )}
        </div>
      )}

      {error && <div className="text-xs text-red-6">{error}</div>}
    </div>
  )
}
