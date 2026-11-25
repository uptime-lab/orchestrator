import { clsx } from 'clsx'
import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'

type InfoHintProps = {
  text: string
  className?: string
}

type Placement = {
  top: number
  left: number
  // Arrow offset from the popover's left edge, so the arrow keeps pointing
  // at the trigger even when the popover is clamped to the viewport edge.
  arrowX: number
  below: boolean
}

const EDGE_GAP = 8

export const InfoHint = ({ text, className }: InfoHintProps) => {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [placement, setPlacement] = useState<Placement | null>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const tooltipRef = useRef<HTMLDivElement>(null)
  // A hover- or focus-triggered open is followed by a click on mouse/touch;
  // that click must not immediately toggle the popover closed again.
  const suppressNextClickRef = useRef(false)
  const tooltipId = useId()

  // Measures the rendered popover and clamps it into the viewport. Without
  // the clamp a fixed-position popover near the right edge lays out at its
  // shrink-to-fit width and degrades into a one-word-per-line column.
  const place = useCallback(() => {
    const button = buttonRef.current
    const tooltip = tooltipRef.current
    if (!button || !tooltip) return
    const rect = button.getBoundingClientRect()
    const width = tooltip.offsetWidth
    const height = tooltip.offsetHeight
    const center = rect.left + rect.width / 2
    const left = Math.min(
      Math.max(center - width / 2, EDGE_GAP),
      Math.max(EDGE_GAP, window.innerWidth - width - EDGE_GAP),
    )
    const below =
      rect.bottom + EDGE_GAP + height <= window.innerHeight - EDGE_GAP
    const top = below
      ? rect.bottom + EDGE_GAP
      : Math.max(EDGE_GAP, rect.top - height - EDGE_GAP)
    setPlacement({
      top,
      left,
      arrowX: Math.min(Math.max(center - left, 12), width - 12),
      below,
    })
  }, [])

  useLayoutEffect(() => {
    if (open) place()
    else setPlacement(null)
  }, [open, place, text])

  useEffect(() => {
    if (!open) return
    window.addEventListener('scroll', place, true)
    window.addEventListener('resize', place)
    return () => {
      window.removeEventListener('scroll', place, true)
      window.removeEventListener('resize', place)
    }
  }, [open, place])

  useEffect(() => {
    if (!open) return
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false)
      }
    }
    const handleClickOutside = (event: MouseEvent) => {
      if (
        tooltipRef.current &&
        !tooltipRef.current.contains(event.target as Node) &&
        buttonRef.current &&
        !buttonRef.current.contains(event.target as Node)
      ) {
        setOpen(false)
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    document.addEventListener('mousedown', handleClickOutside)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [open])

  const openHint = () => {
    if (!open) {
      suppressNextClickRef.current = true
      setOpen(true)
    }
  }

  const closeHint = () => {
    suppressNextClickRef.current = false
    setOpen(false)
  }

  const handleClick = () => {
    if (suppressNextClickRef.current) {
      suppressNextClickRef.current = false
      return
    }
    setOpen((prev) => !prev)
  }

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        aria-label={t('shared.infoHint.moreInfo')}
        aria-describedby={open ? tooltipId : undefined}
        className={clsx(
          'inline-flex items-center justify-center text-neutral-6 hover:text-neutral-2 focus:text-neutral-2',
          className,
        )}
        onMouseEnter={openHint}
        onMouseLeave={closeHint}
        onFocus={openHint}
        onClick={handleClick}
        onBlur={closeHint}
      >
        <svg
          width="14"
          height="14"
          viewBox="0 0 16 16"
          fill="none"
          aria-hidden="true"
        >
          <circle cx="8" cy="8" r="7" stroke="currentColor" strokeWidth="1.5" />
          <circle cx="8" cy="4.75" r="1" fill="currentColor" />
          <path
            d="M8 7.5v4"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      </button>
      {open
        ? createPortal(
            <div
              ref={tooltipRef}
              id={tooltipId}
              role="tooltip"
              className="fixed z-50"
              style={{
                top: `${placement?.top ?? 0}px`,
                left: `${placement?.left ?? 0}px`,
                width: 'max-content',
                // The first paint happens unplaced so it can be measured;
                // showing it there would flash the popover at the corner.
                visibility: placement ? 'visible' : 'hidden',
              }}
            >
              <div className="bg-neutral-14 border border-neutral-11 rounded px-3 py-2 shadow-lg max-w-[320px] text-xs leading-relaxed text-neutral-2 whitespace-normal break-words">
                {text}
              </div>
              {placement ? (
                <div
                  className={clsx(
                    'absolute w-0 h-0 border-l-[6px] border-r-[6px] border-l-transparent border-r-transparent',
                    placement.below
                      ? 'border-b-[6px] border-b-neutral-11'
                      : 'border-t-[6px] border-t-neutral-11',
                  )}
                  style={{
                    left: `${placement.arrowX - 6}px`,
                    ...(placement.below ? { top: '-5px' } : { bottom: '-5px' }),
                  }}
                />
              ) : null}
            </div>,
            document.body,
          )
        : null}
    </>
  )
}
