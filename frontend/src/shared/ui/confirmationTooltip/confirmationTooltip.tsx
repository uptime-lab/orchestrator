import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '../button/button'

type ConfirmationTooltipProps = {
  message: string
  buttonRef: HTMLElement | null
  onConfirm: () => void
  onCancel: () => void
  position?: 'top' | 'bottom' | 'top-left'
  confirmButtonText?: string
}

export const ConfirmationTooltip: React.FC<ConfirmationTooltipProps> = ({
  message,
  buttonRef,
  onConfirm,
  onCancel,
  position: tooltipPosition = 'top',
  confirmButtonText,
}) => {
  const { t } = useTranslation()
  // The default cannot live on the parameter: `t` is only callable once the
  // component is rendering, not at module scope.
  const confirmText =
    confirmButtonText ?? t('shared.confirmationTooltip.confirm')
  const [position, setPosition] = useState<{
    top: number
    left: number
  } | null>(null)
  const tooltipRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (buttonRef) {
      const updatePosition = () => {
        const rect = buttonRef.getBoundingClientRect()
        if (tooltipPosition === 'bottom') {
          setPosition({
            top: rect.bottom + 8,
            left: rect.left + rect.width / 2,
          })
        } else if (tooltipPosition === 'top-left') {
          // Position at button left edge, tooltip will be to the left of button
          setPosition({
            top: rect.top - 8,
            left: rect.left,
          })
        } else {
          // Default 'top' position
          setPosition({
            top: rect.top - 8,
            left: rect.left + rect.width / 2,
          })
        }
      }
      updatePosition()
      window.addEventListener('scroll', updatePosition, true)
      window.addEventListener('resize', updatePosition)
      return () => {
        window.removeEventListener('scroll', updatePosition, true)
        window.removeEventListener('resize', updatePosition)
      }
    }
  }, [buttonRef, tooltipPosition])

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (
        tooltipRef.current &&
        !tooltipRef.current.contains(event.target as Node) &&
        buttonRef &&
        !buttonRef.contains(event.target as Node)
      ) {
        onCancel()
      }
    }

    document.addEventListener('mousedown', handleClickOutside)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [onCancel, buttonRef])

  if (!position || !buttonRef) return null

  const getTransform = () => {
    if (tooltipPosition === 'bottom') {
      return 'translate(-50%, 0)'
    } else if (tooltipPosition === 'top-left') {
      // Translate fully to the left and up, so tooltip appears to the left of button
      return 'translate(-100%, -100%)'
    } else {
      return 'translate(-50%, -100%)'
    }
  }

  return (
    <div
      ref={tooltipRef}
      className="fixed z-50"
      style={{
        top: `${position.top}px`,
        left: `${position.left}px`,
        transform: getTransform(),
      }}
    >
      <div className="bg-neutral-14 border border-neutral-11 rounded p-3 shadow-lg min-w-[280px]">
        <div className="text-sm text-neutral-2 mb-3">{message}</div>
        <div className="flex gap-2 justify-end">
          <Button size="sm" kind="secondary" onClick={onCancel}>
            {t('shared.confirmationTooltip.cancel')}
          </Button>
          <Button size="sm" kind="primary" onClick={onConfirm}>
            {confirmText}
          </Button>
        </div>
      </div>
      {/* Arrow pointing to button */}
      {tooltipPosition === 'bottom' ? (
        <div
          className="absolute left-1/2 transform -translate-x-1/2 w-0 h-0 border-l-[6px] border-r-[6px] border-b-[6px] border-l-transparent border-r-transparent border-b-neutral-11"
          style={{ top: '-5px' }}
        />
      ) : tooltipPosition === 'top-left' ? (
        <div
          className="absolute -bottom-1 w-0 h-0 border-l-[6px] border-r-[6px] border-t-[6px] border-l-transparent border-r-transparent border-t-neutral-11"
          style={{ right: '16px' }}
        />
      ) : (
        <div className="absolute left-1/2 -bottom-1 transform -translate-x-1/2 w-0 h-0 border-l-[6px] border-r-[6px] border-t-[6px] border-l-transparent border-r-transparent border-t-neutral-11" />
      )}
    </div>
  )
}
