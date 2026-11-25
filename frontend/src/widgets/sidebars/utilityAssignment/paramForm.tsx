import { t } from 'i18next'
import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import type { UtilityParam } from '@/entities/utility'

import { InfoHint } from '@/shared/ui/infoHint'

// Local generated form for utility params. The workflow editor's CommandInputs
// renders the same JSON shape, but it is bound to the block registry store
// (fetches /workflow-blocks on mount) and to CommandSpec — reusing it here
// would drag the workflow model into the utilities page and it has no
// min/max support. The catalog's param set (string, text, int, bool, enum)
// is small enough that a local renderer is the smaller coupling.

const inputClassName =
  'px-2 py-1.5 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 placeholder:text-neutral-7 focus:outline-none focus:border-lime-6/50'

const asString = (value: unknown): string =>
  typeof value === 'string' ? value : ''
const asNumber = (value: unknown): number | '' =>
  typeof value === 'number' ? value : ''
const asBool = (value: unknown): boolean => value === true

type ParamFieldProps = {
  param: UtilityParam
  value: unknown
  disabled: boolean
  onChange: (value: unknown) => void
  // Removes the key entirely — an omitted param means "use the default".
  onClear: () => void
}

const defaultPlaceholder = (param: UtilityParam): string | undefined => {
  if (param.placeholder) return param.placeholder
  if (param.default !== undefined && param.default !== null) {
    return t(
      'widgets.sidebars.utilityAssignment.paramForm.defaultPlaceholder',
      { value: String(param.default) },
    )
  }
  return undefined
}

export const UtilityParamField: FC<ParamFieldProps> = ({
  param,
  value,
  disabled,
  onChange,
  onClear,
}) => {
  const { t } = useTranslation()
  const label = param.required ? `${param.label} *` : param.label
  const detailsHint = param.details ? (
    <InfoHint text={param.details} className="ml-1" />
  ) : null
  // String values may carry {{secrets.<name>}} / {{node.attributes.<key>}}
  // references, resolved per node at apply — plain inputs pass them through
  // untouched; the hint just says they exist.
  const refsHint = (
    <span className="text-xxs text-neutral-7">
      {/* The reference syntax is passed as interpolation values so the literal
          {{...}} tokens render instead of being swallowed by i18next. */}
      {t('pages.utilities.form.refsHint', {
        secretsRef: '{{secrets.<name>}}',
        attributeRef: '{{node.attributes.<key>}}',
      })}
    </span>
  )

  switch (param.type) {
    case 'bool':
      return (
        <label className="flex items-center gap-2 text-sm text-neutral-3 cursor-pointer select-none">
          <input
            type="checkbox"
            checked={
              value === undefined ? param.default === true : asBool(value)
            }
            disabled={disabled}
            onChange={(e) => onChange(e.target.checked)}
          />
          <span>{param.label}</span>
          {detailsHint}
        </label>
      )

    case 'int':
      return (
        <div className="flex flex-col gap-1">
          <label className="text-xs text-neutral-7">
            {label}
            {detailsHint}
          </label>
          <input
            type="number"
            value={asNumber(value)}
            disabled={disabled}
            min={param.min}
            max={param.max}
            onChange={(e) => {
              if (e.target.value === '') {
                onClear()
                return
              }
              const parsed = parseInt(e.target.value, 10)
              onChange(Number.isNaN(parsed) ? 0 : parsed)
            }}
            className={inputClassName}
            placeholder={defaultPlaceholder(param)}
          />
          {(param.min !== undefined || param.max !== undefined) && (
            <span className="text-xxs text-neutral-7">
              {param.min !== undefined && param.max !== undefined
                ? t('widgets.sidebars.utilityAssignment.paramForm.range', {
                    min: param.min,
                    max: param.max,
                  })
                : param.min !== undefined
                  ? t('widgets.sidebars.utilityAssignment.paramForm.min', {
                      value: param.min,
                    })
                  : t('widgets.sidebars.utilityAssignment.paramForm.max', {
                      value: param.max,
                    })}
            </span>
          )}
          {param.description && (
            <span className="text-xs text-neutral-6">{param.description}</span>
          )}
        </div>
      )

    case 'text':
      return (
        <div className="flex flex-col gap-1">
          <label className="text-xs text-neutral-7">
            {label}
            {detailsHint}
          </label>
          <textarea
            value={asString(value)}
            disabled={disabled}
            onChange={(e) =>
              e.target.value === '' ? onClear() : onChange(e.target.value)
            }
            className={`${inputClassName} resize-none font-mono`}
            placeholder={defaultPlaceholder(param)}
            rows={4}
          />
          {param.description && (
            <span className="text-xs text-neutral-6">{param.description}</span>
          )}
          {refsHint}
        </div>
      )

    case 'enum':
      return (
        <div className="flex flex-col gap-1">
          <label className="text-xs text-neutral-7">
            {label}
            {detailsHint}
          </label>
          <select
            value={asString(value)}
            disabled={disabled}
            onChange={(e) =>
              e.target.value === '' ? onClear() : onChange(e.target.value)
            }
            className={inputClassName}
          >
            <option value="">
              {param.default !== undefined && param.default !== null
                ? t(
                    'widgets.sidebars.utilityAssignment.paramForm.defaultOption',
                    { value: String(param.default) },
                  )
                : t(
                    'widgets.sidebars.utilityAssignment.paramForm.selectPlaceholder',
                  )}
            </option>
            {(param.enum_values ?? []).map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
          {param.description && (
            <span className="text-xs text-neutral-6">{param.description}</span>
          )}
        </div>
      )

    // 'string' and anything the catalog grows later degrades to a plain
    // string input rather than an invisible field.
    default:
      return (
        <div className="flex flex-col gap-1">
          <label className="text-xs text-neutral-7">
            {label}
            {detailsHint}
          </label>
          <input
            type="text"
            value={asString(value)}
            disabled={disabled}
            onChange={(e) =>
              e.target.value === '' ? onClear() : onChange(e.target.value)
            }
            className={inputClassName}
            placeholder={defaultPlaceholder(param)}
          />
          {param.description && (
            <span className="text-xs text-neutral-6">{param.description}</span>
          )}
          {refsHint}
        </div>
      )
  }
}
