import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  deleteNodeAttribute,
  getNodeAttributes,
  upsertNodeAttribute,
  type NodeAttribute,
} from '@/entities/nodeAttribute'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { InfoHint } from '@/shared/ui/infoHint'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { hasLineBreak, isEnvKey } from '@/shared/utils/formRules'

// Attribute keys are env-var-safe and resolver-safe: letters, digits and '_',
// not starting with a digit, at most 64 characters. Mirrors the backend
// domain validation.
const MAX_KEY_LENGTH = 64

// AD-style custom node attributes: per-node-unique, optionally secret,
// key/value pairs delivered to the agent env and resolvable as
// {{node.attributes.<key>}}. Built-in facts (name/ip/mac/serial/os/…) live on
// the Overview tab, so this tab is only about custom attributes — plus a pinned
// read-only hostname row, since hostname is part of the identity a node
// replacement transfers.
export const NodeAttributesSection: FC<{
  nodeId: string
  hostname?: string
  // editable gates the add/edit/delete controls; when false the section is
  // read-only (standard facts + attribute values only).
  editable?: boolean
}> = ({ nodeId, hostname, editable = true }) => {
  const { t } = useTranslation()
  const [attrs, setAttrs] = useState<NodeAttribute[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Add form.
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')
  const [newSensitive, setNewSensitive] = useState(true)

  // Inline edit of one attribute's value.
  const [editingKey, setEditingKey] = useState<string | null>(null)
  const [editingValue, setEditingValue] = useState('')
  const [pendingDelete, setPendingDelete] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await getNodeAttributes(nodeId)
      setAttrs(res.attributes)
      setError(null)
    } catch (err) {
      // The server's reason (a watcher node, an invalid key) is the
      // diagnostic; the generic line is only the fallback.
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.nodeAttributesSection.loadError'),
        ),
      )
    } finally {
      setLoading(false)
    }
  }, [nodeId, t])

  useEffect(() => {
    load()
  }, [load])

  const keyTaken = attrs.some((a) => a.key === newKey.trim())
  const keyValid =
    isEnvKey(newKey.trim()) && newKey.trim().length <= MAX_KEY_LENGTH
  // The value is delivered as one line of the node's environment file.
  const valueValid = !hasLineBreak(newValue)
  const canAdd = newKey.trim() !== '' && keyValid && !keyTaken && valueValid

  const handleAdd = useCallback(async () => {
    if (!canAdd) return
    try {
      await upsertNodeAttribute(nodeId, newKey.trim(), {
        value: newValue,
        sensitive: newSensitive,
      })
      setNewKey('')
      setNewValue('')
      setNewSensitive(true)
      await load()
    } catch (err) {
      // The server's reason (a watcher node, an invalid key) is the
      // diagnostic; the generic line is only the fallback.
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.nodeAttributesSection.saveError'),
        ),
      )
    }
  }, [canAdd, nodeId, newKey, newValue, newSensitive, load, t])

  const handleSaveEdit = useCallback(
    async (attr: NodeAttribute) => {
      if (hasLineBreak(editingValue)) {
        setError(
          t('widgets.sidebars.node.nodeAttributesSection.valueLineBreak'),
        )
        return
      }
      try {
        await upsertNodeAttribute(nodeId, attr.key, {
          value: editingValue,
          sensitive: attr.sensitive,
        })
        setEditingKey(null)
        setEditingValue('')
        await load()
      } catch (err) {
        // The server's reason (a watcher node, an invalid key) is the
        // diagnostic; the generic line is only the fallback.
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.node.nodeAttributesSection.saveError'),
          ),
        )
      }
    },
    [nodeId, editingValue, load, t],
  )

  const handleDelete = useCallback(async () => {
    if (!pendingDelete) return
    try {
      await deleteNodeAttribute(nodeId, pendingDelete)
      await load()
    } catch (err) {
      // The server's reason (a watcher node, an invalid key) is the
      // diagnostic; the generic line is only the fallback.
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.nodeAttributesSection.deleteError'),
        ),
      )
    } finally {
      setPendingDelete(null)
    }
  }, [pendingDelete, nodeId, load, t])

  const inputCls =
    'bg-neutral-13 rounded px-2 py-1 text-sm text-neutral-2 placeholder:text-neutral-7 outline-none focus:ring-1 focus:ring-lime-6/50'

  return (
    <div className="flex flex-col gap-5 py-2">
      {/* Custom attributes (built-in facts live on the Overview tab) */}
      <div className="flex flex-col gap-2">
        {error && <div className="text-xs text-red-6">{error}</div>}

        {!loading && attrs.length === 0 && (
          <div className="text-xs text-neutral-6">
            {t('widgets.sidebars.node.nodeAttributesSection.empty')}
          </div>
        )}

        <div className="flex flex-col gap-1.5">
          {/* Pinned hostname: part of the transferable identity, edited on
              Overview or applied by a replacement playbook. */}
          {hostname && (
            <div className="flex items-center gap-2 text-sm">
              <div className="min-w-20 font-mono text-neutral-4 truncate">
                hostname
              </div>
              <div className="flex-1 truncate text-neutral-2">{hostname}</div>
              <span className="text-xxs uppercase tracking-wider text-neutral-6">
                {t('widgets.sidebars.node.nodeAttributesSection.identity')}
              </span>
            </div>
          )}
          {attrs.map((attr) => (
            <div
              key={attr.key}
              className="flex items-center gap-2 text-sm group"
            >
              <div className="min-w-20 font-mono text-neutral-4 truncate">
                {attr.key}
              </div>
              {editingKey === attr.key ? (
                <input
                  autoFocus
                  value={editingValue}
                  onChange={(e) => setEditingValue(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') handleSaveEdit(attr)
                    if (e.key === 'Escape') setEditingKey(null)
                  }}
                  className={`flex-1 ${inputCls}`}
                  placeholder={
                    attr.sensitive
                      ? t(
                          'widgets.sidebars.node.nodeAttributesSection.newSecretValuePlaceholder',
                        )
                      : t(
                          'widgets.sidebars.node.nodeAttributesSection.valuePlaceholder',
                        )
                  }
                />
              ) : (
                <div className="flex-1 truncate text-neutral-2">
                  {attr.sensitive
                    ? attr.has_value
                      ? '••••••••'
                      : t(
                          'widgets.sidebars.node.nodeAttributesSection.emptyValue',
                        )
                    : attr.value ||
                      t(
                        'widgets.sidebars.node.nodeAttributesSection.emptyValue',
                      )}
                </div>
              )}
              {attr.sensitive && (
                <span className="text-xxs uppercase tracking-wider text-violet-5">
                  {t('widgets.sidebars.node.nodeAttributesSection.secretBadge')}
                </span>
              )}
              {editable &&
                (editingKey === attr.key ? (
                  <>
                    <button
                      type="button"
                      onClick={() => handleSaveEdit(attr)}
                      className="text-xs text-lime-6 hover:text-lime-5"
                    >
                      {t('widgets.sidebars.node.nodeAttributesSection.save')}
                    </button>
                    <button
                      type="button"
                      onClick={() => setEditingKey(null)}
                      className="text-xs text-neutral-6 hover:text-neutral-3"
                    >
                      {t('widgets.sidebars.node.nodeAttributesSection.cancel')}
                    </button>
                  </>
                ) : (
                  <div className="flex items-center gap-2 opacity-0 group-hover:opacity-100 transition-opacity">
                    <button
                      type="button"
                      onClick={() => {
                        setEditingKey(attr.key)
                        setEditingValue(attr.sensitive ? '' : attr.value || '')
                      }}
                      className="text-xs text-neutral-5 hover:text-neutral-2"
                    >
                      {t('widgets.sidebars.node.nodeAttributesSection.edit')}
                    </button>
                    <button
                      type="button"
                      onClick={() => setPendingDelete(attr.key)}
                      className="text-xs text-neutral-5 hover:text-red-6"
                    >
                      {t('widgets.sidebars.node.nodeAttributesSection.delete')}
                    </button>
                  </div>
                ))}
            </div>
          ))}
        </div>

        {/* Add form */}
        {editable && (
          <div className="flex flex-col gap-2 mt-2 pt-3 border-t border-neutral-12">
            <div className="flex items-center gap-2">
              <input
                value={newKey}
                onChange={(e) => setNewKey(e.target.value)}
                placeholder={t(
                  'widgets.sidebars.node.nodeAttributesSection.keyPlaceholder',
                )}
                className={`w-40 font-mono ${inputCls}`}
              />
              <input
                value={newValue}
                onChange={(e) => setNewValue(e.target.value)}
                placeholder={t(
                  'widgets.sidebars.node.nodeAttributesSection.valuePlaceholder',
                )}
                className={`flex-1 ${inputCls}`}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && canAdd) handleAdd()
                }}
              />
            </div>
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-1.5">
                <label className="flex items-center gap-1.5 text-xs text-neutral-5 cursor-pointer select-none">
                  <input
                    type="checkbox"
                    checked={newSensitive}
                    onChange={(e) => setNewSensitive(e.target.checked)}
                  />
                  {t('widgets.sidebars.node.nodeAttributesSection.secretLabel')}
                </label>
                <InfoHint
                  text={t(
                    'widgets.sidebars.node.nodeAttributesSection.secretHint',
                  )}
                />
              </div>
              <Button size="sm" onClick={handleAdd} disabled={!canAdd}>
                {t('widgets.sidebars.node.nodeAttributesSection.add')}
              </Button>
            </div>
            {newKey.trim() !== '' && !keyValid && (
              <div className="text-xxs text-red-6">
                {t('widgets.sidebars.node.nodeAttributesSection.keyInvalid')}
              </div>
            )}
            {!valueValid && (
              <div className="text-xxs text-red-6">
                {t(
                  'widgets.sidebars.node.nodeAttributesSection.valueLineBreak',
                )}
              </div>
            )}
            {keyTaken && (
              <div className="text-xxs text-red-6">
                {t('widgets.sidebars.node.nodeAttributesSection.keyTakenError')}
              </div>
            )}
          </div>
        )}
      </div>

      <ConfirmDialog
        open={!!pendingDelete}
        title={t('widgets.sidebars.node.nodeAttributesSection.deleteTitle')}
        message={t(
          'widgets.sidebars.node.nodeAttributesSection.deleteConfirmMessage',
          { key: pendingDelete },
        )}
        confirmText={t('widgets.sidebars.node.nodeAttributesSection.delete')}
        destructive
        onConfirm={handleDelete}
        onCancel={() => setPendingDelete(null)}
      />
    </div>
  )
}
