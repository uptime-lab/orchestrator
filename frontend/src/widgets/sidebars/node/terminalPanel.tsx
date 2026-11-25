import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import { FC, useCallback, useEffect, useReducer, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import '@xterm/xterm/css/xterm.css'

import { Button } from '@/shared/ui/button/button.tsx'
import { ensureFreshAccessToken, getApiUrl } from '@/shared/utils/apiConfig'

import { ActionRow } from './actionRow.tsx'
import { SubViewHeader } from './subViewHeader.tsx'
import { splitInputFrames } from './terminalInput'
import {
  initialTerminalView,
  sessionWanted,
  terminalViewReducer,
} from './terminalView'

const IconTerminal = () => (
  <svg
    className="w-4 h-4"
    fill="none"
    stroke="currentColor"
    strokeWidth={1.8}
    viewBox="0 0 24 24"
  >
    <rect x="3" y="4" width="18" height="16" rx="1.5" />
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M7 9l3 3-3 3M13 15h4"
    />
  </svg>
)

// Build the terminal WebSocket URL following the same base convention as the
// REST client (axios baseURL `/api` + path `/api/v1/...`), so it works behind
// the same Traefik prefix handling.
const terminalWsUrl = (nodeId: string): string => {
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
  // getApiUrl() is a bare path ('/api') when API_URL is relative but an absolute
  // URL ('https://host/api') when API_URL is a full URL. Take just the path
  // so we build a same-origin wss URL instead of concatenating a full URL after
  // wss://host (which yields an invalid URL and an instant connection failure).
  let apiPath: string
  try {
    apiPath = new URL(getApiUrl(), window.location.origin).pathname
  } catch {
    apiPath = getApiUrl()
  }
  return `${scheme}://${window.location.host}${apiPath}/api/v1/nodes/${nodeId}/terminal`
}

export const TerminalPanel: FC<{
  nodeId: string
  online: boolean
  // Embedded inside the Actions tab as a slide-in sub-view: auto-opens and the
  // close/back control returns to the action list instead of collapsing a row.
  embedded?: boolean
  onBack?: () => void
}> = ({ nodeId, online, embedded = false, onBack }) => {
  const { t } = useTranslation()
  const [view, dispatch] = useReducer(
    terminalViewReducer,
    embedded,
    initialTerminalView,
  )
  const { open, error, attempt } = view
  const live = sessionWanted(view)
  const containerRef = useRef<HTMLDivElement>(null)

  // Leaving the view is the only path that calls onBack; a refusal ends the
  // session but keeps the message on screen (see terminalView.ts).
  const close = useCallback(() => {
    dispatch({ type: 'close' })
    onBack?.()
  }, [onBack])
  const retry = useCallback(() => dispatch({ type: 'retry' }), [])

  useEffect(() => {
    if (!live || !containerRef.current) return

    const term = new Terminal({
      cursorBlink: true,
      fontFamily:
        'JetBrains Mono, ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      theme: { background: '#0b0e14', foreground: '#f1f1f2' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    fit.fit()

    const ws = new WebSocket(terminalWsUrl(nodeId))
    ws.binaryType = 'arraybuffer'

    // If the device never responds after auth (e.g. an older agent without
    // terminal support), don't hang silently — surface a clear error.
    let firstByte = false
    let waitTimer: ReturnType<typeof setTimeout> | undefined
    const refuse = (message: string) => dispatch({ type: 'refuse', message })

    ws.onopen = async () => {
      // The token is sent as the first message; refresh it if stale so an expired
      // access token doesn't get the terminal rejected.
      let token: string | null
      try {
        token = await ensureFreshAccessToken()
      } catch {
        // The refresh failed without ending the session (rate limit, server
        // error, network); the panel's retry is the way on.
        refuse(t('widgets.sidebars.node.terminalPanel.connectionFailed'))
        return
      }
      if (!token) {
        refuse(t('widgets.sidebars.node.terminalPanel.notAuthorized'))
        return
      }
      if (ws.readyState !== WebSocket.OPEN) return
      ws.send(
        JSON.stringify({
          token,
          cols: term.cols,
          rows: term.rows,
        }),
      )
      term.write(
        `\x1b[90m${t('widgets.sidebars.node.terminalPanel.connecting')}\x1b[0m\r\n`,
      )
      waitTimer = setTimeout(() => {
        if (!firstByte) {
          refuse(t('widgets.sidebars.node.terminalPanel.noResponse'))
        }
      }, 12000)
    }
    ws.onmessage = (ev) => {
      firstByte = true
      if (waitTimer) clearTimeout(waitTimer)
      if (typeof ev.data === 'string') {
        // Control frame: error or closed.
        try {
          const msg = JSON.parse(ev.data)
          if (msg.type === 'error') {
            refuse(
              msg.message ||
                t('widgets.sidebars.node.terminalPanel.terminalError'),
            )
          } else if (msg.type === 'closed') {
            term.write(
              `\r\n\x1b[90m[${t('widgets.sidebars.node.terminalPanel.sessionClosed')}]\x1b[0m\r\n`,
            )
          }
        } catch {
          term.write(ev.data)
        }
        return
      }
      term.write(new Uint8Array(ev.data as ArrayBuffer))
    }
    ws.onclose = () => {
      term.write(
        `\r\n\x1b[90m[${t('widgets.sidebars.node.terminalPanel.disconnected')}]\x1b[0m\r\n`,
      )
    }
    // Only a failure BEFORE the session produced any output is a real connection
    // error. Once bytes have flowed, a later error/close is a normal disconnect
    // (the onclose handler already prints "[disconnected]") — surfacing
    // "connection failed" then is spurious noise over a working session.
    ws.onerror = () => {
      if (!firstByte)
        refuse(t('widgets.sidebars.node.terminalPanel.connectionFailed'))
    }

    // A paste arrives as one onData call; the server bounds each frame, so a
    // large one is split (see terminalInput.ts) rather than refused.
    const dataSub = term.onData((data) => {
      if (ws.readyState !== WebSocket.OPEN) return
      for (const chunk of splitInputFrames(data)) {
        ws.send(JSON.stringify({ type: 'input', data: chunk }))
      }
    })
    const resizeSub = term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols, rows }))
      }
    })
    const onWinResize = () => fit.fit()
    window.addEventListener('resize', onWinResize)

    return () => {
      if (waitTimer) clearTimeout(waitTimer)
      window.removeEventListener('resize', onWinResize)
      dataSub.dispose()
      resizeSub.dispose()
      // Detach the handlers first: the close event arrives after the xterm
      // instance below is disposed, and must not write into it or report a
      // refusal for a session that is already gone.
      ws.onopen = null
      ws.onmessage = null
      ws.onclose = null
      ws.onerror = null
      ws.close()
      term.dispose()
    }
  }, [live, attempt, nodeId, t])

  const errorBox = (className: string) =>
    error ? (
      <div
        className={`${className} text-xs text-red-6 bg-red-6/10 border border-red-6/20 rounded px-2 py-1.5`}
      >
        {error}
      </div>
    ) : null

  // A refused session: the message and a retry in place of the terminal. The
  // close control stays where it is in each layout.
  const refusedActions = (
    <div className="flex gap-2">
      <Button kind="secondary" onClick={retry} className="text-xs">
        {t('widgets.sidebars.node.terminalPanel.retry')}
      </Button>
    </div>
  )

  // Embedded sub-view: a "← Back" header + the terminal, opened immediately.
  if (embedded) {
    return (
      <div className="flex flex-col gap-4">
        <SubViewHeader
          title={t('widgets.sidebars.node.terminalPanel.title')}
          onBack={close}
        />
        {errorBox('')}
        {live ? (
          <div
            ref={containerRef}
            className="w-full h-80 bg-neutral-15 rounded overflow-hidden"
          />
        ) : (
          refusedActions
        )}
      </div>
    )
  }

  // Closed: a single full-width row (icon + label) matching the other Actions
  // entries — the row itself is the open control. Open: a header with a Close
  // button and the terminal below.
  if (!open) {
    return (
      <div className="flex flex-col gap-2">
        <ActionRow
          icon={<IconTerminal />}
          label={t('widgets.sidebars.node.terminalPanel.title')}
          onClick={() => dispatch({ type: 'open' })}
          disabled={!online}
          title={
            online
              ? t('widgets.sidebars.node.terminalPanel.openTitle')
              : t('widgets.sidebars.node.terminalPanel.nodeMustBeOnline')
          }
        />
        {!online && (
          <div className="px-3 text-xs text-neutral-6">
            {t('widgets.sidebars.node.terminalPanel.offlineNote')}
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between px-3">
        <div className="text-sm text-neutral-3">
          {t('widgets.sidebars.node.terminalPanel.title')}
        </div>
        <Button
          variant="destructive"
          kind="secondary"
          onClick={close}
          className="text-xs"
        >
          {t('widgets.sidebars.node.terminalPanel.close')}
        </Button>
      </div>
      {errorBox('mx-3')}
      {live ? (
        <div
          ref={containerRef}
          className="w-full h-72 bg-neutral-15 rounded overflow-hidden"
        />
      ) : (
        <div className="px-3">{refusedActions}</div>
      )}
    </div>
  )
}
