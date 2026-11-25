import { useEffect } from 'react'

// Warns the user before leaving the page while a long-running action
// (e.g. an OS image upload) is still in progress.
export const useUnloadGuard = (active: boolean) => {
  useEffect(() => {
    if (!active) {
      return
    }
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [active])
}
