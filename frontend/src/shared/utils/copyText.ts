import { useCallback, useEffect, useRef, useState } from 'react'

// What copyText needs from the browser, injectable so the fallback order can be
// tested without one.
export type CopyEnvironment = {
  isSecureContext: boolean
  clipboard?: { writeText: (text: string) => Promise<void> }
  // Copies through a temporary selection; true when the browser reports it
  // copied.
  execCopy: (text: string) => boolean
}

// execCommand('copy') is deprecated but is the only path that works outside a
// secure context, where navigator.clipboard does not exist — which is what an
// install served over plain HTTP is.
const execCopyWithTextarea = (text: string): boolean => {
  if (typeof document === 'undefined') return false
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.top = '0'
  area.style.left = '0'
  area.style.opacity = '0'
  document.body.appendChild(area)
  const previousFocus = document.activeElement as HTMLElement | null
  try {
    area.focus()
    area.select()
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(area)
    previousFocus?.focus?.()
  }
}

const browserEnvironment = (): CopyEnvironment => ({
  isSecureContext:
    typeof window !== 'undefined' && window.isSecureContext === true,
  clipboard: typeof navigator !== 'undefined' ? navigator.clipboard : undefined,
  execCopy: execCopyWithTextarea,
})

// copyText puts text on the clipboard and reports whether it got there. It
// never throws: a caller shows "copied" on true and asks the user to copy by
// hand on false.
export const copyText = async (
  text: string,
  env: CopyEnvironment = browserEnvironment(),
): Promise<boolean> => {
  if (env.isSecureContext && env.clipboard) {
    try {
      await env.clipboard.writeText(text)
      return true
    } catch {
      // Denied permission or a document without focus; try the other path.
    }
  }
  try {
    return env.execCopy(text)
  } catch {
    return false
  }
}

export type CopyState = 'idle' | 'copied' | 'failed'

// useCopyText is the button-side of copyText: the state flips to 'copied' or
// 'failed' and returns to 'idle' after a moment, so the label can say which.
export const useCopyText = (resetAfterMs = 2000) => {
  const [state, setState] = useState<CopyState>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  useEffect(() => () => clearTimeout(timer.current), [])

  const copy = useCallback(
    async (text: string) => {
      const ok = await copyText(text)
      setState(ok ? 'copied' : 'failed')
      clearTimeout(timer.current)
      // A failure stays up longer: the user has to act on it.
      timer.current = setTimeout(
        () => setState('idle'),
        ok ? resetAfterMs : resetAfterMs * 3,
      )
      return ok
    },
    [resetAfterMs],
  )

  return { state, copy }
}
