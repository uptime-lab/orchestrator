import { FC, useMemo, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'

const normalizeTag = (raw: string) =>
  raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]/g, '-')

// Generic chip input for tags with a caller-supplied suggestion vocabulary.
// Free entry: Enter/comma/blur adds the current text (normalized to the
// backend charset).
export const TagsInput: FC<{
  value: string[]
  onChange: (tags: string[]) => void
  suggestions: string[]
  disabled?: boolean
}> = ({ value, onChange, suggestions: pool, disabled }) => {
  const { t } = useTranslation()
  const [input, setInput] = useState('')
  const [focused, setFocused] = useState(false)

  const suggestions = useMemo(() => {
    const q = input.trim().toLowerCase()
    return pool
      .filter((tag) => !value.includes(tag))
      .filter((tag) => !q || tag.includes(q))
      .slice(0, 6)
  }, [pool, value, input])

  const addTag = (raw: string) => {
    const tag = normalizeTag(raw)
    if (!tag || value.includes(tag) || value.length >= 10) return
    onChange([...value, tag])
    setInput('')
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter' || event.key === ',') {
      event.preventDefault()
      addTag(input)
    } else if (event.key === 'Backspace' && input === '' && value.length > 0) {
      onChange(value.slice(0, -1))
    }
  }

  return (
    <div className="flex flex-col gap-2">
      <label className="text-sm font-medium text-neutral-7 uppercase">
        {t('widgets.tagsInput.label')}
      </label>
      <div className="relative">
        <div className="flex flex-wrap items-center gap-1.5 bg-neutral-13 rounded px-2.5 py-2 min-h-[2.75rem]">
          {value.map((tag) => (
            <span
              key={tag}
              className="inline-flex items-center gap-1 text-xs font-mono px-1.5 py-0.5 rounded bg-neutral-11 text-neutral-2"
            >
              #{tag}
              <button
                type="button"
                onClick={() => onChange(value.filter((item) => item !== tag))}
                disabled={disabled}
                aria-label={t('widgets.tagsInput.removeTag', { tag })}
                className="text-neutral-6 hover:text-red-6 transition-colors disabled:opacity-50"
              >
                &times;
              </button>
            </span>
          ))}
          <input
            type="text"
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            onFocus={() => setFocused(true)}
            onBlur={() => {
              // Let suggestion clicks land before the dropdown closes.
              setTimeout(() => setFocused(false), 150)
              addTag(input)
            }}
            disabled={disabled}
            placeholder={
              value.length === 0 ? t('widgets.tagsInput.placeholder') : ''
            }
            className="flex-1 min-w-[6rem] bg-transparent text-sm text-white outline-none disabled:opacity-50"
          />
        </div>
        {focused && suggestions.length > 0 && (
          <div className="absolute z-10 left-0 right-0 mt-1 bg-neutral-12 border border-neutral-11 rounded shadow-lg max-h-48 overflow-y-auto">
            {suggestions.map((tag) => (
              <button
                key={tag}
                type="button"
                onMouseDown={(e) => {
                  e.preventDefault()
                  addTag(tag)
                }}
                className="w-full px-3 py-1.5 text-left text-sm font-mono text-neutral-2 hover:bg-neutral-11 transition-colors"
              >
                #{tag}
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="text-xs text-neutral-6">
        {t('widgets.tagsInput.hint')}
      </div>
    </div>
  )
}
