import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import type { OU } from '@/entities/ou'

export type PropertiesPanelProps = {
  name: string
  description: string
  ouIds: string[]
  ous: OU[]
  disabled?: boolean
  onNameChange: (value: string) => void
  onDescriptionChange: (value: string) => void
  onClose: () => void
}

// PropertiesPanel holds what a playbook IS — its name and description — in the
// same right-hand column as the block inspector, so "select a thing, edit it on
// the right" is one habit rather than two.
//
// The description used to live in a one-line strip pinned to the bottom of the
// window, below the canvas, where it read as a status bar rather than a field
// anybody was meant to fill in.
//
// Scope is shown and NOT edited here. A playbook's organizational units are
// assigned on the Playbooks page, by dragging it onto the unit tree, which is
// the only place that can show what else lives in a unit — and the only place
// where assigning it to several units makes visual sense.
export const PropertiesPanel: FC<PropertiesPanelProps> = ({
  name,
  description,
  ouIds,
  ous,
  disabled,
  onNameChange,
  onDescriptionChange,
  onClose,
}) => {
  const { t } = useTranslation()
  const scopeNames = ouIds.map((id) => {
    const ou = ous.find((candidate) => candidate.id === id)
    if (!ou) return t('pages.workflowEditor.properties.unknownUnit')
    const parent = ou.parent_id
      ? ous.find((candidate) => candidate.id === ou.parent_id)
      : undefined
    return parent ? `${parent.name} / ${ou.name}` : ou.name
  })

  return (
    <div className="w-80 shrink-0 border-l border-neutral-11 overflow-y-auto p-4 flex flex-col gap-4">
      <div className="flex items-start justify-between gap-2">
        <div className="text-xs font-bold uppercase text-neutral-7">
          {t('pages.workflowEditor.properties.title')}
        </div>
        <button
          type="button"
          onClick={onClose}
          className="text-neutral-6 hover:text-neutral-2 text-sm leading-none"
          aria-label={t('pages.workflowEditor.properties.close')}
        >
          ✕
        </button>
      </div>

      <div className="flex flex-col gap-1">
        <label className="text-xs text-neutral-7">
          {t('pages.workflowEditor.properties.nameLabel')}
        </label>
        <input
          type="text"
          value={name}
          onChange={(e) => onNameChange(e.target.value)}
          disabled={disabled}
          className="px-3 py-1.5 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
          placeholder={t('pages.workflowEditor.properties.namePlaceholder')}
        />
      </div>

      <div className="flex flex-col gap-1">
        <label className="text-xs text-neutral-7">
          {t('pages.workflowEditor.properties.descriptionLabel')}
        </label>
        <textarea
          value={description}
          onChange={(e) => onDescriptionChange(e.target.value)}
          disabled={disabled}
          rows={5}
          className="px-3 py-1.5 bg-neutral-13 border border-neutral-11 rounded text-xs text-neutral-2 focus:outline-none focus:border-purple-5 resize-y"
          placeholder={t(
            'pages.workflowEditor.properties.descriptionPlaceholder',
          )}
        />
      </div>

      <div className="flex flex-col gap-1">
        <div className="text-xs text-neutral-7">
          {t('pages.workflowEditor.properties.ousLabel')}
        </div>
        {scopeNames.length > 0 ? (
          <div className="flex flex-col gap-0.5">
            {scopeNames.map((name) => (
              <div key={name} className="text-sm text-neutral-2">
                {name}
              </div>
            ))}
          </div>
        ) : (
          <div className="text-sm text-neutral-2">
            {t('pages.workflowEditor.properties.allNodes')}
          </div>
        )}
        <div className="text-xs text-neutral-6">
          {scopeNames.length > 0
            ? t('pages.workflowEditor.properties.scopedHint')
            : t('pages.workflowEditor.properties.unscopedHint')}
        </div>
      </div>
    </div>
  )
}
