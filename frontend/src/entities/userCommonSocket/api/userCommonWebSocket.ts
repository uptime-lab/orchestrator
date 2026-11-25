import { NodeCommand, NodeResult } from '@/entities/node/api/types.ts'

import {
  ensureFreshAccessToken,
  getWebsocketUrl,
} from '@/shared/utils/apiConfig.ts'

import { UserSocketEventType, WEBSOCKET_SHUTDOWN_CODE } from './types.ts'

type EventPayloadKnown =
  | {
      type: UserSocketEventType.Health
      payload: { timestamp: number }
    }
  | {
      type: UserSocketEventType.NodeCreated | UserSocketEventType.NodeUpdated
      payload: NodeResult
    }
  | {
      type: UserSocketEventType.NodeDeleted
      payload: NodeResult
    }
  | {
      type:
        | UserSocketEventType.NodeCommandCreated
        | UserSocketEventType.NodeCommandUpdated
      payload: NodeCommand
    }
type EventPayloadUnknown = {
  type: Exclude<UserSocketEventType, EventPayloadKnown['type']>
  payload: unknown
}
export type UserSocketEventPayload = EventPayloadKnown | EventPayloadUnknown

export type UserCommonWebSocketStatus =
  'idle' | 'pending' | 'connected' | 'closed' | 'closed-by-plan'

export type UserCommonWebSocketObject<T extends WebSocket = WebSocket> = {
  socket: T
  disconnect: () => void
  // sometimes, when we try to close event with our code, it is closed with a different one.
  // we need to track it using this variable.
  closingIntent?: boolean
}

export const userCommonWebSocket = async (
  onEvent: (payload: UserSocketEventPayload) => unknown,
  onStatusChange: (status: UserCommonWebSocketStatus) => unknown,
): Promise<UserCommonWebSocketObject<WebSocket>> => {
  const connect = (
    socketResultToReconnect?: UserCommonWebSocketObject<WebSocket>,
  ) => {
    const socket = new WebSocket(`${getWebsocketUrl()}/ws`)

    const currentSocketResult =
      socketResultToReconnect ||
      ({} as Partial<UserCommonWebSocketObject<WebSocket>>)
    currentSocketResult.socket = socket
    currentSocketResult.closingIntent = false
    // A failing socket fires both `error` and `close`, and each 'closed' starts
    // a reconnect timer, so reporting both doubled the timers on every failure
    // and the reconnects multiplied instead of backing off. One report per
    // socket, and none for a socket closed on purpose.
    let closeReported = false
    const reportClosed = () => {
      if (closeReported || currentSocketResult.closingIntent) return
      closeReported = true
      onStatusChange('closed')
    }
    currentSocketResult.disconnect = () => {
      currentSocketResult.closingIntent = true
      currentSocketResult.socket?.close(WEBSOCKET_SHUTDOWN_CODE, 'please-exit')
      onStatusChange('closed-by-plan')
    }

    currentSocketResult.socket.onopen = async () => {
      // The backend binds the socket's lifetime to the access token's expiry, so
      // a reconnect after the token expired must authorize with a *fresh* token —
      // otherwise it fails with "invalid token" and live updates stall until a
      // full page reload. Refresh here if needed before authorizing.
      let token: string | null
      try {
        token = await ensureFreshAccessToken()
      } catch {
        // The refresh failed without saying the session is over (rate limit,
        // server error, network). Close as an ordinary failure so the
        // reconnect backoff tries again.
        socket.close()
        reportClosed()
        return
      }
      if (!token) {
        // No valid token (refresh failed / signed out): stop instead of looping
        // on a dead token. 'closed-by-plan' is excluded from the reconnect rule.
        currentSocketResult.closingIntent = true
        socket.close(WEBSOCKET_SHUTDOWN_CODE, 'no-valid-token')
        onStatusChange('closed-by-plan')
        return
      }
      // The socket may have been closed while we were awaiting the refresh.
      if (socket.readyState !== WebSocket.OPEN) return
      // Not 'connected' yet: an open socket the server has not authorized
      // delivers nothing. 'connected' waits for the authorization answer
      // below, so a refused authorization cannot pass for a live connection
      // (and reset the reconnect backoff) every time it is retried.
      socket.send(
        JSON.stringify({
          type: 'authorization',
          payload: { token },
        }),
      )
    }
    currentSocketResult.socket.onclose = (e) => {
      if (!currentSocketResult.closingIntent) {
        // eslint-disable-next-line no-console
        console.log(
          '[User] Socket is closed without intention.',
          e.code,
          e.reason,
        )
        reportClosed()
      }
    }
    currentSocketResult.socket.onmessage = (socketEvent) => {
      try {
        const message = JSON.parse(socketEvent.data)
        if (message?.type === UserSocketEventType.AuthorizationOpened) {
          if (message.payload?.status === 'ok') {
            onStatusChange('connected')
          } else {
            // Refused (an expired token, no membership, two-step sign-in
            // still to set up). The server would drop the socket later
            // anyway; closing now lets the backoff grow instead of looking
            // connected for the seconds in between.
            currentSocketResult.socket?.close()
            reportClosed()
          }
        }
        onEvent(message)
      } catch (e) {
        // eslint-disable-next-line no-console
        console.error('[User] Error while parsing socket response: ', e)
      }
    }
    currentSocketResult.socket.onerror = (err) => {
      // eslint-disable-next-line no-console
      console.error(
        '[User] Socket encountered error: ',
        err.type,
        'Closing socket',
      )
      currentSocketResult.socket?.close()
      reportClosed()
    }

    return currentSocketResult as UserCommonWebSocketObject<WebSocket>
  }

  return connect()
}
