import clsx from 'clsx'
import { t } from 'i18next'
import { FC, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { InfoHint } from '@/shared/ui/infoHint'

import { apiWorkflows } from '../../api'
import type { SchedulePreview } from '../../api/postSchedulePreview'
import {
  buildCron,
  CronMode,
  CronShape,
  INTERVAL_CHOICES,
  parseCron,
  weekdayLabels,
} from './cronShapes'

const modeLabels = (): Array<{ value: CronMode; label: string }> => [
  { value: 'interval', label: t('entities.workflow.cronField.modeInterval') },
  { value: 'hourly', label: t('entities.workflow.cronField.modeHourly') },
  { value: 'daily', label: t('entities.workflow.cronField.modeDaily') },
  { value: 'weekly', label: t('entities.workflow.cronField.modeWeekly') },
  { value: 'monthly', label: t('entities.workflow.cronField.modeMonthly') },
  { value: 'custom', label: t('entities.workflow.cronField.modeCustom') },
]

// localTime renders a UTC instant in the viewer's own zone. The stored
// expression is UTC and stays UTC — this is a reading aid, not a conversion:
// 03:00 UTC is not 03:00 for the operator, and a schedule nobody can place in
// their own day is one nobody can sanity-check.
const localTime = (iso: string) =>
  new Date(iso).toLocaleString(undefined, {
    weekday: 'short',
    day: '2-digit',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  })

const utcTime = (iso: string) =>
  new Date(iso).toLocaleString('en-GB', {
    timeZone: 'UTC',
    weekday: 'short',
    day: '2-digit',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  })

const pad = (n: number) => String(n).padStart(2, '0')
const range = (from: number, to: number) =>
  Array.from({ length: to - from + 1 }, (_, i) => from + i)

const selectClass =
  'bg-neutral-13 border border-neutral-11 rounded px-1.5 py-1 text-xs text-neutral-2 focus:outline-none focus:border-purple-5'

export type CronFieldProps = {
  label: string
  value: string
  description?: string
  // Longer, read-once background shown behind an info hint, never inline.
  details?: string
  placeholder?: string
  inputClassName: string
  onChange: (value: string) => void
}

// CronField edits a schedule through the shape an operator thinks in — every
// week on Tuesday — while the cron expression stays the stored value and the
// only source of truth.
//
// The builder's state is DERIVED from the expression on every render rather
// than held beside it. That is what makes it impossible for the two to drift,
// and it means a shape the builder does not recognise (`1-30/2`, a restricted
// month, both day fields set) simply lands in Custom with the expression
// untouched, instead of being re-emitted as something the operator did not
// write.
export const CronField: FC<CronFieldProps> = ({
  label,
  value,
  description,
  details,
  placeholder,
  inputClassName,
  onChange,
}) => {
  const { t } = useTranslation()
  const [preview, setPreview] = useState<SchedulePreview | null>(null)
  const [loading, setLoading] = useState(false)
  const requestIdRef = useRef(0)

  const shape = useMemo(() => parseCron(value), [value])
  // Choosing Custom changes no expression, so it cannot be read back out of one
  // — `0 3 1 * *` is a perfectly good monthly schedule AND a thing somebody may
  // want to hand-edit. "Which editor am I using" is therefore the one piece of
  // genuinely local state here, and without it the select snapped straight back
  // to the derived mode and Custom looked like a disabled option.
  const [manualCustom, setManualCustom] = useState(false)
  const mode: CronMode = manualCustom ? 'custom' : shape.mode
  const [rawOpen, setRawOpen] = useState(false)
  const showRaw = rawOpen || mode === 'custom'

  const emit = (next: CronShape) => {
    const expression = buildCron(next)
    if (expression !== null) onChange(expression)
  }

  const handleMode = (nextMode: CronMode) => {
    if (nextMode === 'custom') {
      // Leave the expression exactly as it is: switching to Custom is a request
      // to edit it by hand, not a request to change it.
      setManualCustom(true)
      setRawOpen(true)
      return
    }
    setManualCustom(false)
    // Carry the current time of day across a mode change — someone who set
    // 03:00 daily and switches to weekly means 03:00 on those days.
    emit({ ...shape, mode: nextMode })
  }

  const toggleWeekday = (day: number) => {
    const next = shape.weekdays.includes(day)
      ? shape.weekdays.filter((d) => d !== day)
      : [...shape.weekdays, day]
    // Refuse to clear the last one: an empty day list has no cron expression,
    // so the alternative is emitting nothing and leaving the UI lying about
    // what is stored.
    if (next.length === 0) return
    emit({ ...shape, mode: 'weekly', weekdays: next })
  }

  const toggleMonthday = (day: number) => {
    const next = shape.monthdays.includes(day)
      ? shape.monthdays.filter((d) => d !== day)
      : [...shape.monthdays, day]
    if (next.length === 0) return
    emit({ ...shape, mode: 'monthly', monthdays: next })
  }

  useEffect(() => {
    const expression = value.trim()
    if (!expression) {
      setPreview(null)
      return
    }
    // Debounced: this fires per keystroke in the raw field, and a half-typed
    // expression is meaningless anyway.
    const requestId = ++requestIdRef.current
    setLoading(true)
    const timer = setTimeout(() => {
      apiWorkflows
        .postSchedulePreview(expression, 3)
        .then((result) => {
          // Ignore a response overtaken by a later edit, or the panel flickers
          // back to an older expression's answer.
          if (requestIdRef.current === requestId) setPreview(result)
        })
        .catch(() => {
          if (requestIdRef.current === requestId) setPreview(null)
        })
        .finally(() => {
          if (requestIdRef.current === requestId) setLoading(false)
        })
    }, 350)
    return () => clearTimeout(timer)
  }, [value])

  const timeOfDay = (
    <div className="flex items-center gap-1">
      <span className="text-xs text-neutral-7 w-8">
        {t('entities.workflow.cronField.at')}
      </span>
      <select
        value={shape.hour}
        onChange={(e) => emit({ ...shape, hour: Number(e.target.value) })}
        className={selectClass}
      >
        {range(0, 23).map((h) => (
          <option key={h} value={h}>
            {pad(h)}
          </option>
        ))}
      </select>
      <span className="text-xs text-neutral-6">:</span>
      <select
        value={shape.minute}
        onChange={(e) => emit({ ...shape, minute: Number(e.target.value) })}
        className={selectClass}
      >
        {range(0, 59).map((m) => (
          <option key={m} value={m}>
            {pad(m)}
          </option>
        ))}
      </select>
      <span className="text-xs text-neutral-7">
        {t('entities.workflow.cronField.utc')}
      </span>
    </div>
  )

  return (
    <div className="flex flex-col gap-2">
      <label className="text-xs text-neutral-7">
        {label}
        {details && <InfoHint text={details} className="ml-1" />}
      </label>

      <select
        value={mode}
        onChange={(e) => handleMode(e.target.value as CronMode)}
        className={inputClassName}
      >
        {modeLabels().map((mode) => (
          <option key={mode.value} value={mode.value}>
            {mode.label}
          </option>
        ))}
      </select>

      {mode === 'interval' && (
        <div className="flex items-center gap-1">
          <span className="text-xs text-neutral-7">
            {t('entities.workflow.cronField.every')}
          </span>
          <select
            value={shape.intervalMinutes}
            onChange={(e) =>
              emit({ ...shape, intervalMinutes: Number(e.target.value) })
            }
            className={selectClass}
          >
            {INTERVAL_CHOICES.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
          <span className="text-xs text-neutral-7">
            {t('entities.workflow.cronField.minutes')}
          </span>
        </div>
      )}

      {mode === 'hourly' && (
        <div className="flex items-center gap-1">
          <span className="text-xs text-neutral-7 w-8">
            {t('entities.workflow.cronField.at')}
          </span>
          <span className="text-xs text-neutral-6">:</span>
          <select
            value={shape.minute}
            onChange={(e) => emit({ ...shape, minute: Number(e.target.value) })}
            className={selectClass}
          >
            {range(0, 59).map((m) => (
              <option key={m} value={m}>
                {pad(m)}
              </option>
            ))}
          </select>
          <span className="text-xs text-neutral-7">
            {t('entities.workflow.cronField.pastTheHour')}
          </span>
        </div>
      )}

      {mode === 'daily' && timeOfDay}

      {mode === 'weekly' && (
        <div className="flex flex-col gap-2">
          <div className="flex flex-wrap gap-1">
            {weekdayLabels().map((day) => (
              <button
                key={day.value}
                type="button"
                onClick={() => toggleWeekday(day.value)}
                title={day.long}
                className={clsx(
                  'w-8 py-1 text-[11px] rounded border transition-colors',
                  shape.weekdays.includes(day.value)
                    ? 'border-lime-6/50 text-lime-6 bg-lime-6/10'
                    : 'border-neutral-11 text-neutral-6 hover:text-neutral-2',
                )}
              >
                {day.short}
              </button>
            ))}
          </div>
          {timeOfDay}
        </div>
      )}

      {mode === 'monthly' && (
        <div className="flex flex-col gap-2">
          <div className="grid grid-cols-7 gap-1">
            {range(1, 31).map((day) => (
              <button
                key={day}
                type="button"
                onClick={() => toggleMonthday(day)}
                className={clsx(
                  'py-0.5 text-[11px] rounded border transition-colors',
                  shape.monthdays.includes(day)
                    ? 'border-lime-6/50 text-lime-6 bg-lime-6/10'
                    : 'border-neutral-11 text-neutral-6 hover:text-neutral-2',
                )}
              >
                {day}
              </button>
            ))}
          </div>
          {/* A month without the chosen day simply does not fire that month —
              the 31st is the one people pick without meaning it, and the run
              list below shows the gaps rather than explaining them. */}
          {shape.monthdays.some((d) => d > 28) && (
            <span className="text-xs text-yellow-5">
              {t('entities.workflow.cronField.shortMonthsWarning')}
            </span>
          )}
          {timeOfDay}
        </div>
      )}

      {showRaw ? (
        // No caption: the param's own label above the builder already names
        // this field, and the word was landing on screen four times in a panel
        // this narrow.
        <div className="flex flex-col gap-1">
          <input
            type="text"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            className={clsx(inputClassName, 'font-mono')}
            placeholder={placeholder || '0 3 * * 1'}
            spellCheck={false}
          />
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setRawOpen(true)}
          className="self-start text-xs text-neutral-6 hover:text-neutral-3 transition-colors font-mono"
        >
          ▸ {value.trim() || t('entities.workflow.cronField.cronPlaceholder')}
        </button>
      )}

      {value.trim() !== '' && (
        <div className="text-xs">
          {loading && !preview && (
            <span className="text-neutral-6">
              {t('entities.workflow.cronField.checking')}
            </span>
          )}
          {preview && !preview.valid && (
            <span className="text-red-6 break-words">{preview.error}</span>
          )}
          {preview && preview.valid && (
            <div className="flex flex-col gap-1">
              <span className="text-neutral-2">{preview.description}</span>
              {preview.never_fires ? (
                // Valid and inert. Worth its own line in red: nothing else in
                // the product would ever tell the operator, and the expression
                // itself looks entirely reasonable.
                <span className="text-red-6">
                  {t('entities.workflow.cronField.neverRuns')}
                </span>
              ) : (
                <div className="flex flex-col gap-0.5 text-neutral-6">
                  <span className="uppercase text-[10px] text-neutral-7">
                    {t('entities.workflow.cronField.nextRuns')}
                  </span>
                  {preview.next_runs.map((run) => (
                    <span key={run} className="font-mono">
                      {t('entities.workflow.cronField.utcTime', {
                        time: utcTime(run),
                      })}
                      <span className="text-neutral-8">
                        {' '}
                        ·{' '}
                        {t('entities.workflow.cronField.localTime', {
                          time: localTime(run),
                        })}
                      </span>
                    </span>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {description && (
        <span className="text-xs text-neutral-6">{description}</span>
      )}
    </div>
  )
}
