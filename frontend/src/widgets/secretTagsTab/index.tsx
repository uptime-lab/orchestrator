import clsx from 'clsx'
import { FC, useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useNodes } from '@/entities/node/hooks'
import { useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import {
  apiSecrets,
  SECRET_TAG_DND_TYPE,
  useSecretTags,
  type SecretAttachment,
} from '@/entities/secret'

import Badge, { BadgeColor } from '@/shared/ui/badge'
import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'

import { describeAttachment } from './attachmentLabel'

// The pickable palette (mirrors the backend secret_tag.Palette). Solid-bg
// classes are literal so Tailwind keeps them (same values as Badge's bg map).
const PALETTE: BadgeColor[] = [
  'red',
  'pink',
  'purple',
  'violet',
  'blue',
  'lightBlue',
  'green',
  'lime',
  'yellow',
  'orange',
]

const SWATCH: Record<string, string> = {
  red: 'bg-red-7',
  pink: 'bg-pink-7',
  purple: 'bg-purple-5',
  violet: 'bg-violet-5',
  blue: 'bg-blue-8',
  lightBlue: 'bg-lightBlue-8',
  green: 'bg-green-8',
  lime: 'bg-lime-7',
  yellow: 'bg-yellow-5',
  orange: 'bg-orange-5',
}

const KEY_PATTERN = /^[a-z0-9._-]{1,40}$/

const ColorSwatches: FC<{
  value: string
  onChange: (color: BadgeColor) => void
}> = ({ value, onChange }) => (
  <div className="flex items-center gap-1.5 flex-wrap">
    {PALETTE.map((color) => (
      <button
        key={color}
        type="button"
        onClick={() => onChange(color)}
        aria-label={color}
        className={clsx(
          'w-5 h-5 rounded-full transition-transform',
          SWATCH[color],
          value === color
            ? 'ring-2 ring-offset-2 ring-offset-neutral-14 ring-neutral-2 scale-110'
            : 'hover:scale-110',
        )}
      />
    ))}
  </div>
)

// The Tags management tab of the Secrets section: every tag as a colored chip
// with a name + color, its usage/delivery counts, and create/rename/recolor/
// delete + optional OU assignment. Membership stays on the secrets; this only
// manages presentation and lifecycle.
export const SecretTagsTab: FC = () => {
  // Tag names and colours are account-wide secret metadata: renaming one
  // cascades across every secret and attachment carrying it, so it rides on the
  // same grant as the secrets themselves.
  const canManage = useCan()(Actions.SecretManage)
  const { t } = useTranslation()
  const { tags, fetchTags } = useSecretTags()
  const { ous } = useOUs()
  const { nodes } = useNodes()

  // Which tag's delivery list is open, and what it holds. One at a time: the
  // list answers "where does this tag reach", which is a question about one tag.
  const [openTag, setOpenTag] = useState<string | null>(null)
  const [attachments, setAttachments] = useState<SecretAttachment[] | null>(
    null,
  )

  const ouNames = useMemo(() => {
    const map: Record<string, string> = {}
    ous.forEach((ou) => {
      map[ou.id] = ou.name
    })
    return map
  }, [ous])
  // The Secrets page does not load the fleet, so this is usually empty and the
  // attachment falls back to the node id. That is deliberate: an attachment
  // that rendered as nothing would be one nobody could remove.
  const nodeNames = useMemo(() => {
    const map: Record<string, string> = {}
    nodes.forEach((node) => {
      map[node.id] = node.name
    })
    return map
  }, [nodes])

  const loadAttachments = useCallback(async (tag: string) => {
    setAttachments(null)
    try {
      const result = await apiSecrets.getSecretTagAttachments(tag)
      setAttachments(result.attachments)
      setError(null)
    } catch {
      setAttachments([])
      setError(t('widgets.secretTagsTab.loadAttachmentsError'))
    }
  }, [])

  const toggleAttachments = useCallback(
    (tag: string) => {
      if (openTag === tag) {
        setOpenTag(null)
        return
      }
      setOpenTag(tag)
      loadAttachments(tag)
    },
    [openTag, loadAttachments],
  )

  // Detaching stops delivery to that unit or node. It does not touch the tag,
  // the secrets carrying it, or any other attachment — the counts on the row
  // are refreshed because they are what the operator was reading.
  const handleDetach = useCallback(
    async (tag: string, attachmentId: string) => {
      try {
        await apiSecrets.detachSecretTag(tag, attachmentId)
        await loadAttachments(tag)
        await fetchTags()
      } catch {
        setError(t('widgets.secretTagsTab.detachError'))
      }
    },
    [loadAttachments, fetchTags],
  )

  const [error, setError] = useState<string | null>(null)

  // Create form.
  const [newName, setNewName] = useState('')
  const [newColor, setNewColor] = useState<BadgeColor>('blue')
  const [newOU, setNewOU] = useState('')

  // Inline edit.
  const [editingTag, setEditingTag] = useState<string | null>(null)
  const [editName, setEditName] = useState('')
  const [editColor, setEditColor] = useState<BadgeColor>('blue')
  const [pendingDelete, setPendingDelete] = useState<string | null>(null)

  useEffect(() => {
    fetchTags()
  }, [fetchTags])

  const normalizedNew = newName.trim().toLowerCase()
  const nameValid = KEY_PATTERN.test(normalizedNew)
  const nameTaken = tags.some((tg) => tg.tag === normalizedNew)
  const canCreate = normalizedNew !== '' && nameValid && !nameTaken

  const handleCreate = useCallback(async () => {
    if (!canCreate) return
    try {
      await apiSecrets.createSecretTag({ name: normalizedNew, color: newColor })
      if (newOU) {
        await apiSecrets.attachSecretTag(normalizedNew, { ou_id: newOU })
      }
      setNewName('')
      setNewOU('')
      await fetchTags()
      setError(null)
    } catch {
      setError(t('widgets.secretTagsTab.createError'))
    }
  }, [canCreate, normalizedNew, newColor, newOU, fetchTags])

  const handleSaveEdit = useCallback(
    async (original: string) => {
      const nextName = editName.trim().toLowerCase()
      if (!KEY_PATTERN.test(nextName)) {
        setError(t('widgets.secretTagsTab.invalidName'))
        return
      }
      try {
        await apiSecrets.updateSecretTag(original, {
          name: nextName !== original ? nextName : undefined,
          color: editColor,
        })
        setEditingTag(null)
        await fetchTags()
        setError(null)
      } catch {
        setError(t('widgets.secretTagsTab.updateError'))
      }
    },
    [editName, editColor, fetchTags],
  )

  const handleDelete = useCallback(async () => {
    if (!pendingDelete) return
    try {
      await apiSecrets.deleteSecretTag(pendingDelete)
      await fetchTags()
    } catch {
      setError(t('widgets.secretTagsTab.deleteError'))
    } finally {
      setPendingDelete(null)
    }
  }, [pendingDelete, fetchTags])

  const inputCls =
    'bg-neutral-13 rounded px-2.5 py-1.5 text-sm text-neutral-2 placeholder:text-neutral-7 outline-none focus:ring-1 focus:ring-lime-6/50'

  return (
    <div className="flex flex-col gap-6 max-w-3xl">
      <p className="text-sm text-neutral-5">{t('pages.secrets.tags.hint')}</p>

      {error && <div className="text-sm text-red-6">{error}</div>}

      {/* Create */}
      <div className="flex flex-col gap-3 p-4 rounded border border-neutral-12 bg-neutral-14">
        <div className="text-xs uppercase font-bold text-neutral-5">
          {t('pages.secrets.tags.create')}
        </div>
        <div className="flex items-center gap-2 flex-wrap">
          <input
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            placeholder={t('pages.secrets.tags.namePlaceholder')}
            className={`w-48 font-mono ${inputCls}`}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && canCreate) handleCreate()
            }}
          />
          <select
            value={newOU}
            onChange={(e) => setNewOU(e.target.value)}
            className={inputCls}
            aria-label={t('pages.secrets.tags.assignOU')}
          >
            <option value="">{t('pages.secrets.tags.noOU')}</option>
            {ous.map((ou) => (
              <option key={ou.id} value={ou.id}>
                {ou.name}
              </option>
            ))}
          </select>
          {canManage && (
            <Button size="md" onClick={handleCreate} disabled={!canCreate}>
              {t('pages.secrets.tags.create')}
            </Button>
          )}
        </div>
        <ColorSwatches value={newColor} onChange={setNewColor} />
        {newName.trim() !== '' && !nameValid && (
          <div className="text-xxs text-red-6">
            {t('pages.secrets.tags.invalidName')}
          </div>
        )}
        {nameTaken && (
          <div className="text-xxs text-red-6">
            {t('pages.secrets.tags.nameTaken')}
          </div>
        )}
      </div>

      {/* List */}
      <div className="flex flex-col gap-1">
        {tags.length === 0 && (
          <div className="text-sm text-neutral-6">
            {t('pages.secrets.tags.empty')}
          </div>
        )}
        {tags.map((tg) => {
          const delivered = tg.ou_count + tg.node_count > 0
          const editing = editingTag === tg.tag
          return (
            <div
              key={tg.tag}
              // The row itself is the drag source: this is the tab where tag
              // management lives, so attaching a tag to a unit from here is the
              // gesture a user expects, and a row is a larger and more
              // discoverable target than the rail's chip. Not while renaming —
              // a drag would fight the text selection in the input.
              draggable={!editing}
              onDragStart={(e) => {
                e.dataTransfer.setData(SECRET_TAG_DND_TYPE, tg.tag)
                e.dataTransfer.effectAllowed = 'move'
              }}
              className={clsx(
                'flex items-center flex-wrap gap-3 py-2 px-1 border-b border-neutral-13 group',
                !editing && 'cursor-grab active:cursor-grabbing',
              )}
            >
              {editing ? (
                <div className="flex-1 flex flex-col gap-2">
                  <div className="flex items-center gap-2">
                    <input
                      autoFocus
                      value={editName}
                      onChange={(e) => setEditName(e.target.value)}
                      className={`w-48 font-mono ${inputCls}`}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') handleSaveEdit(tg.tag)
                        if (e.key === 'Escape') setEditingTag(null)
                      }}
                    />
                    <button
                      type="button"
                      onClick={() => handleSaveEdit(tg.tag)}
                      className="text-xs text-lime-6 hover:text-lime-5"
                    >
                      {t('widgets.secretTagsTab.save')}
                    </button>
                    <button
                      type="button"
                      onClick={() => setEditingTag(null)}
                      className="text-xs text-neutral-6 hover:text-neutral-3"
                    >
                      {t('widgets.secretTagsTab.cancel')}
                    </button>
                  </div>
                  <ColorSwatches value={editColor} onChange={setEditColor} />
                </div>
              ) : (
                <>
                  <div className="flex-shrink-0">
                    <Badge
                      color={(tg.color || 'neutral') as BadgeColor}
                      variant="solid"
                    >
                      #{tg.tag}
                    </Badge>
                  </div>
                  <div className="text-xs text-neutral-5 flex-1 min-w-0">
                    {t('pages.secrets.tags.usage', { count: tg.count })}
                    {delivered && (
                      // The count was the whole answer until now: an operator
                      // could see that a tag delivered somewhere and had no way
                      // to learn where, or to stop it.
                      <button
                        type="button"
                        onClick={() => toggleAttachments(tg.tag)}
                        className="text-lime-6 hover:text-lime-5"
                      >
                        {' '}
                        ▸{' '}
                        {t('pages.secrets.tags.delivered', {
                          ous: tg.ou_count,
                          nodes: tg.node_count,
                        })}
                      </button>
                    )}
                  </div>
                  <div
                    className={clsx(
                      'flex items-center gap-2 opacity-0 transition-opacity',
                      canManage && 'group-hover:opacity-100',
                    )}
                  >
                    {canManage && (
                      <button
                        type="button"
                        onClick={() => {
                          setEditingTag(tg.tag)
                          setEditName(tg.tag)
                          setEditColor((tg.color || 'blue') as BadgeColor)
                        }}
                        className="text-xs text-neutral-5 hover:text-neutral-2"
                      >
                        {t('widgets.secretTagsTab.edit')}
                      </button>
                    )}
                    {canManage && (
                      <button
                        type="button"
                        onClick={() => setPendingDelete(tg.tag)}
                        className="text-xs text-neutral-5 hover:text-red-6"
                      >
                        {t('widgets.secretTagsTab.delete')}
                      </button>
                    )}
                  </div>
                </>
              )}
              {openTag === tg.tag && !editing && (
                <div className="basis-full flex flex-col gap-1 pl-2 pt-2 text-xs">
                  {attachments === null && (
                    <span className="text-neutral-6">
                      {t('widgets.secretTagsTab.loading')}
                    </span>
                  )}
                  {attachments?.length === 0 && (
                    <span className="text-neutral-6">
                      {t('pages.secrets.tags.noAttachments')}
                    </span>
                  )}
                  {attachments?.map((att) => {
                    const target = describeAttachment(att, ouNames, nodeNames)
                    if (!target) return null
                    return (
                      <div key={att.id} className="flex items-center gap-2">
                        <span className="text-neutral-7 w-10 flex-shrink-0">
                          {target.kind === 'unit'
                            ? t('widgets.secretTagsTab.kindUnit')
                            : t('widgets.secretTagsTab.kindNode')}
                        </span>
                        <span className="text-neutral-4 truncate">
                          {target.label}
                        </span>
                        {canManage && (
                          <button
                            type="button"
                            onClick={() => handleDetach(tg.tag, att.id)}
                            className="text-neutral-6 hover:text-red-6"
                          >
                            {t('pages.secrets.tags.detach')}
                          </button>
                        )}
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })}
      </div>

      <ConfirmDialog
        open={!!pendingDelete}
        title={t('pages.secrets.tags.deleteTitle')}
        message={t('pages.secrets.tags.deleteMessage', { tag: pendingDelete })}
        confirmText={t('widgets.secretTagsTab.delete')}
        destructive
        onConfirm={handleDelete}
        onCancel={() => setPendingDelete(null)}
      />
    </div>
  )
}
