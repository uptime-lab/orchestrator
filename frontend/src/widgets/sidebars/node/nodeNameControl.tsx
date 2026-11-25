import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'

// Inline editor for the node's display name, shown at the top of the Properties
// tab. The old "Edit node info" form also carried SSH key / default
// username+password, but those are now delivered separately (access entries and
// secrets), so the only thing left worth editing here is the name.
export const NodeNameControl: FC<{
  nodeId: string
  name?: string
  editable?: boolean
}> = ({ nodeId, name, editable = true }) => {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(name || '')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setValue(name || '')
  }, [name])

  const cancel = useCallback(() => {
    setEditing(false)
    setValue(name || '')
    setError(null)
  }, [name])

  const save = useCallback(async () => {
    const next = value.trim()
    if (!next) {
      setError(t('widgets.sidebars.node.nodeNameControl.nameRequired'))
      return
    }
    if (next === (name || '')) {
      setEditing(false)
      return
    }
    setSaving(true)
    setError(null)
    try {
      await apiNodes.putUpdateNode(nodeId, { name: next })
      await nodesModel.fetchNodesFx()
      setEditing(false)
    } catch {
      setError(t('widgets.sidebars.node.nodeNameControl.saveError'))
    } finally {
      setSaving(false)
    }
  }, [value, name, nodeId, t])

  return (
    <div className="flex flex-col gap-1">
      {/* Fixed-height row so switching view <-> edit never shifts the content
          below. min-w-0 lets the input shrink instead of overflowing; the small
          px keeps the value indented and the focus ring off the panel's
          overflow-clipped edge. */}
      <div className="flex items-center gap-2 min-h-[2.25rem] px-1">
        {editing ? (
          <>
            <input
              autoFocus
              value={value}
              onChange={(e) => setValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') save()
                if (e.key === 'Escape') cancel()
              }}
              disabled={saving}
              placeholder={t(
                'widgets.sidebars.node.nodeNameControl.placeholder',
              )}
              className="flex-1 min-w-0 bg-neutral-13 rounded px-2.5 py-1.5 text-sm text-neutral-2 placeholder:text-neutral-7 outline-none focus:ring-1 focus:ring-lime-6/50"
            />
            <button
              type="button"
              onClick={save}
              disabled={saving}
              className="flex-shrink-0 text-xs text-lime-6 hover:text-lime-5 disabled:opacity-50"
            >
              {saving
                ? t('widgets.sidebars.node.nodeNameControl.saving')
                : t('widgets.sidebars.node.nodeNameControl.save')}
            </button>
            <button
              type="button"
              onClick={cancel}
              disabled={saving}
              className="flex-shrink-0 text-xs text-neutral-6 hover:text-neutral-3"
            >
              {t('widgets.sidebars.node.nodeNameControl.cancel')}
            </button>
          </>
        ) : (
          <>
            <span className="flex-1 min-w-0 text-sm font-medium text-neutral-2 truncate">
              {name || '—'}
            </span>
            {editable && (
              <button
                type="button"
                onClick={() => setEditing(true)}
                className="flex-shrink-0 text-xs uppercase tracking-wide text-neutral-6 hover:text-neutral-2 transition-colors"
              >
                {t('widgets.sidebars.node.nodeNameControl.edit')}
              </button>
            )}
          </>
        )}
      </div>
      {error && <div className="text-xs text-red-6">{error}</div>}
    </div>
  )
}
