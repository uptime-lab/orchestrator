import { FC, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiNodes } from '@/entities/node/api'
import type { BootOption } from '@/entities/node/api/getBootOptions'
import type { BootOrder } from '@/entities/node/api/types'

import { extractApiErrorMessage } from '@/shared/utils/apiError'

export type BootOrderSelectorProps = {
  value: BootOrder
  onChange: (bootOrder: BootOrder) => void
  disabled?: boolean
  className?: string
}

export const BootOrderSelector: FC<BootOrderSelectorProps> = ({
  value,
  onChange,
  disabled = false,
  className = '',
}) => {
  const { t } = useTranslation()
  const [availableOptions, setAvailableOptions] = useState<BootOption[]>([])
  const [_enabledOptions, setEnabledOptions] = useState<Set<string>>(new Set())
  const [isLoadingOptions, setIsLoadingOptions] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Fetch boot options on mount
  useEffect(() => {
    const fetchBootOptions = async () => {
      setIsLoadingOptions(true)
      setError(null)
      try {
        const options = await apiNodes.getBootOptions()
        setAvailableOptions(options)

        // Initialize enabled options from current value
        const enabled = new Set<string>(value || [])
        setEnabledOptions(enabled)
      } catch (err: unknown) {
        setError(
          extractApiErrorMessage(err, t('shared.bootOrderSelector.loadError')),
        )
      } finally {
        setIsLoadingOptions(false)
      }
    }
    fetchBootOptions()
  }, [])

  // Update enabled options when value changes
  useEffect(() => {
    const enabled = new Set<string>(value || [])
    setEnabledOptions(enabled)
  }, [value, availableOptions])

  const moveUp = (index: number) => {
    if (index === 0 || disabled) return
    const newOrder = [...value]
    ;[newOrder[index - 1], newOrder[index]] = [
      newOrder[index],
      newOrder[index - 1],
    ]
    onChange(newOrder)
  }

  const moveDown = (index: number) => {
    if (index === value.length - 1 || disabled) return
    const newOrder = [...value]
    ;[newOrder[index], newOrder[index + 1]] = [
      newOrder[index + 1],
      newOrder[index],
    ]
    onChange(newOrder)
  }

  const toggleOption = (optionValue: string) => {
    if (disabled) return

    // Check if option is currently in the boot order
    const isInOrder = value.includes(optionValue)

    if (isInOrder) {
      // Disabling: remove from order
      const newOrder = value.filter((v) => v !== optionValue)
      onChange(newOrder)
    } else {
      // Enabling: add to order at the end
      const newOrder = [...value, optionValue]
      onChange(newOrder)
    }
  }

  const getOptionLabel = (value: string): string => {
    const option = availableOptions.find((opt) => opt.value === value)
    return option?.label || value
  }

  const defaultOptions = availableOptions.filter((opt) => opt.is_default)
  const additionalOptions = availableOptions.filter((opt) => !opt.is_default)

  // Options that are not in the current boot order (value)
  // This includes both enabled options not in order, and unenabled options
  const optionsNotInOrder = availableOptions.filter(
    (opt) => !value.includes(opt.value),
  )

  if (isLoadingOptions) {
    return (
      <div className={`text-xs text-neutral-6 ${className}`}>
        {t('shared.bootOrderSelector.loading')}
      </div>
    )
  }

  if (error) {
    return (
      <div
        className={`text-xs text-red-6 bg-red-1/10 border border-red-6/20 rounded px-2 py-1.5 ${className}`}
      >
        {error}
      </div>
    )
  }

  return (
    <div className={`flex flex-col gap-2 ${className}`}>
      {/* Orderable list with checkboxes - show when there are enabled options */}
      {value.length > 0 && (
        <div className="flex flex-col gap-2">
          {value.map((device, index) => {
            return (
              <div
                // Boot order IS positional — the same device may legitimately
                // appear twice and its POSITION is the thing being edited, so
                // the index is the identity here, not a stand-in for one.
                // eslint-disable-next-line react/no-array-index-key
                key={`${device}-${index}`}
                className="flex items-center gap-2 p-2 rounded border border-neutral-12 bg-neutral-13"
              >
                <input
                  type="checkbox"
                  checked={true}
                  disabled={disabled}
                  onChange={() => toggleOption(device)}
                  className="w-4 h-4 text-orange-6 bg-neutral-14 border-neutral-12 rounded focus:ring-orange-6 focus:ring-2 disabled:opacity-50 disabled:cursor-not-allowed"
                />
                <div className="flex-shrink-0 w-5 h-5 flex items-center justify-center text-xs font-semibold text-neutral-7 bg-neutral-8 rounded">
                  {index + 1}
                </div>
                <div className="flex-1 text-sm text-neutral-2">
                  {getOptionLabel(device)}
                </div>
                <div className="flex gap-1">
                  <button
                    type="button"
                    onClick={() => moveUp(index)}
                    disabled={index === 0 || disabled}
                    className="px-1.5 py-0.5 text-xs text-neutral-4 hover:text-neutral-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    onClick={() => moveDown(index)}
                    disabled={index === value.length - 1 || disabled}
                    className="px-1.5 py-0.5 text-xs text-neutral-4 hover:text-neutral-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    ↓
                  </button>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {/* Available Options - show all options not in the current order */}
      {optionsNotInOrder.length > 0 && (
        <div className="flex flex-col gap-2">
          <div className="text-xs font-medium text-neutral-6 uppercase tracking-wide">
            {t('shared.bootOrderSelector.availableTitle')}
          </div>
          <div className="flex flex-col gap-1.5">
            {/* Show default options first */}
            {defaultOptions
              .filter((opt) => !value.includes(opt.value))
              .map((option) => (
                <label
                  key={option.value}
                  className="flex items-center gap-2 cursor-pointer hover:bg-neutral-13/50 p-1.5 rounded"
                >
                  <input
                    type="checkbox"
                    checked={false}
                    onChange={() => toggleOption(option.value)}
                    disabled={disabled}
                    className="w-4 h-4 text-orange-6 bg-neutral-14 border-neutral-12 rounded focus:ring-orange-6 focus:ring-2"
                  />
                  <div className="flex-1">
                    <div className="text-sm text-neutral-2">{option.label}</div>
                    {option.description && (
                      <div className="text-xs text-neutral-6">
                        {option.description}
                      </div>
                    )}
                  </div>
                </label>
              ))}
            {/* Then show additional options */}
            {additionalOptions
              .filter((opt) => !value.includes(opt.value))
              .map((option) => (
                <label
                  key={option.value}
                  className="flex items-center gap-2 cursor-pointer hover:bg-neutral-13/50 p-1.5 rounded"
                >
                  <input
                    type="checkbox"
                    checked={false}
                    onChange={() => toggleOption(option.value)}
                    disabled={disabled}
                    className="w-4 h-4 text-orange-6 bg-neutral-14 border-neutral-12 rounded focus:ring-orange-6 focus:ring-2"
                  />
                  <div className="flex-1">
                    <div className="text-sm text-neutral-2">{option.label}</div>
                    {option.description && (
                      <div className="text-xs text-neutral-6">
                        {option.description}
                      </div>
                    )}
                  </div>
                </label>
              ))}
          </div>
        </div>
      )}

      {value.length === 0 && optionsNotInOrder.length === 0 && (
        <div className="text-xs text-neutral-6">
          {t('shared.bootOrderSelector.none')}
        </div>
      )}
    </div>
  )
}
