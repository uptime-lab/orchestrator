import { createEffect, createEvent, createStore, sample, split } from 'effector'
import { useUnit } from 'effector-react'

import {
  NodeCommand,
  NodeMetricsEvent,
  NodeResult,
} from '@/entities/node/api/types'
import { nodeButtonPressed } from '@/entities/node/buttonPulse'
import { hydrateIdentifyState } from '@/entities/node/identifyBlink'
import { ImageResult } from '@/entities/osImage/api/types'
import {
  WorkflowExecution,
  WorkflowExecutionNode,
  WorkflowTemplate,
} from '@/entities/workflow/api/types'

import { exhaustiveMatchingGuard } from '@/shared/utils/exhaustiveSwitchCase'

import { authModel } from '../auth'
import { UserSocketEventType } from './api/types'
import {
  userCommonWebSocket,
  UserCommonWebSocketObject,
  UserCommonWebSocketStatus,
} from './api/userCommonWebSocket'
import { reconnectDelayMs } from './reconnectBackoff'

const userSocketConnect = createEvent()

// Hydrate the transient identify/button state whenever the app comes live —
// covers both the first load and reconnects after the laptop slept.
userSocketConnect.watch(() => {
  void hydrateIdentifyState()
})
const userSocketDisconnect = createEvent()
const userSocketOnStatusChanged = createEvent<UserCommonWebSocketStatus>()
// Fired when the socket comes back AFTER a drop (a backend deploy, a network
// blip) — NOT on the first connect. The socket delivers no backlog of the
// events it missed while it was down, so a listener must refetch to resync
// on reconnect. The first connect is excluded because the pages fetch on mount
// anyway.
const userSocketReconnected = createEvent()

const userSocketOnHealth = createEvent<{ timestamp: number }>()
const userSocketOnNodeCreated = createEvent<{ payload: NodeResult }>()
const userSocketOnNodeUpdated = createEvent<{ payload: NodeResult }>()
const userSocketOnNodeDeleted = createEvent<{ payload: NodeResult }>()
const userSocketOnImageCreated = createEvent<{ payload: ImageResult }>()
const userSocketOnImageUpdated = createEvent<{ payload: ImageResult }>()
const userSocketOnImageDeleted = createEvent<{ payload: ImageResult }>()
const userSocketOnNodeCommandCreated = createEvent<{ payload: NodeCommand }>()
const userSocketOnNodeCommandUpdated = createEvent<{ payload: NodeCommand }>()
const userSocketOnNodeMetrics = createEvent<{ payload: NodeMetricsEvent }>()
const userSocketOnWorkflowTemplateCreated = createEvent<{
  payload: WorkflowTemplate
}>()
const userSocketOnWorkflowTemplateUpdated = createEvent<{
  payload: WorkflowTemplate
}>()
const userSocketOnWorkflowTemplateDeleted = createEvent<{
  payload: WorkflowTemplate
}>()
const userSocketOnWorkflowExecutionUpdated = createEvent<{
  payload: WorkflowExecution
}>()
const userSocketOnWorkflowExecutionNodeUpdated = createEvent<{
  payload: WorkflowExecutionNode
}>()
const userSocketOnNodeAdoptionRefused = createEvent<{
  payload: { hardware_serial: string; reason: string }
}>()
const userSocketOnNodeButtonPressed = createEvent<{
  payload: { node_id: string; node_name: string; press_kind: string }
}>()

export const events = {
  userSocketConnect,
  userSocketDisconnect,
  userSocketReconnected,
  userSocketOnStatusChanged,
  // --
  userSocketOnHealth,
  userSocketOnNodeCreated,
  userSocketOnNodeUpdated,
  userSocketOnNodeDeleted,
  userSocketOnImageCreated,
  userSocketOnImageUpdated,
  userSocketOnImageDeleted,
  userSocketOnNodeCommandCreated,
  userSocketOnNodeCommandUpdated,
  userSocketOnNodeMetrics,
  userSocketOnWorkflowTemplateCreated,
  userSocketOnWorkflowTemplateUpdated,
  userSocketOnWorkflowTemplateDeleted,
  userSocketOnWorkflowExecutionUpdated,
  userSocketOnWorkflowExecutionNodeUpdated,
  userSocketOnNodeAdoptionRefused,
  userSocketOnNodeButtonPressed,
}

const shouldConnectToSocket = authModel.isSignedIn

sample({
  clock: shouldConnectToSocket,
  filter: (signed) => !signed,
  target: userSocketDisconnect,
})

// Another tab moved the stored session to a different user or account: stop
// streaming the old one's events into a tab that can no longer act on them.
sample({
  clock: authModel.events.sessionSwitchedElsewhere,
  target: userSocketDisconnect,
})

const $socketStatus = createStore<UserCommonWebSocketStatus>('idle')
  .on(userSocketConnect, () => 'pending')
  .on(userSocketOnStatusChanged, (_, status) => status)

split({
  source: authModel.isSignedInString,
  match: (value) => value,
  cases: {
    signed: userSocketConnect,
    unsigned: userSocketDisconnect,
  },
})

let currentSocket: UserCommonWebSocketObject | null
userSocketConnect.watch(async () => {
  if (currentSocket) {
    currentSocket.disconnect()
    currentSocket = null
  }

  currentSocket = await userCommonWebSocket(({ type, payload }) => {
    switch (type) {
      case UserSocketEventType.AuthorizationOpened:
        break
      case UserSocketEventType.Health:
        userSocketOnHealth(payload)
        break
      case UserSocketEventType.NodeCreated:
        userSocketOnNodeCreated({ payload: payload as NodeResult })
        break
      case UserSocketEventType.NodeUpdated:
        userSocketOnNodeUpdated({ payload: payload as NodeResult })
        break
      case UserSocketEventType.NodeDeleted:
        userSocketOnNodeDeleted({ payload: payload as NodeResult })
        break
      case UserSocketEventType.ImageCreated:
        userSocketOnImageCreated({ payload: payload as ImageResult })
        break
      case UserSocketEventType.ImageUpdated:
        userSocketOnImageUpdated({ payload: payload as ImageResult })
        break
      case UserSocketEventType.ImageDeleted:
        userSocketOnImageDeleted({ payload: payload as ImageResult })
        break
      case UserSocketEventType.NodeCommandCreated:
        userSocketOnNodeCommandCreated({ payload: payload as NodeCommand })
        break
      case UserSocketEventType.NodeCommandUpdated:
        userSocketOnNodeCommandUpdated({ payload: payload as NodeCommand })
        break
      case UserSocketEventType.NodeMetrics:
        userSocketOnNodeMetrics({ payload: payload as NodeMetricsEvent })
        break
      case UserSocketEventType.WorkflowExecutionUpdated:
        userSocketOnWorkflowExecutionUpdated({
          payload: payload as WorkflowExecution,
        })
        break
      case UserSocketEventType.WorkflowExecutionNodeUpdated:
        userSocketOnWorkflowExecutionNodeUpdated({
          payload: payload as WorkflowExecutionNode,
        })
        break
      case UserSocketEventType.NodeAdoptionRefused:
        userSocketOnNodeAdoptionRefused({
          payload: payload as { hardware_serial: string; reason: string },
        })
        break
      case UserSocketEventType.NodeButtonPressed: {
        const press = payload as {
          node_id: string
          node_name: string
          press_kind: string
          blink_until?: string
          duration_sec?: string
        }
        userSocketOnNodeButtonPressed({ payload: press })
        nodeButtonPressed(press)
        break
      }
      default:
        exhaustiveMatchingGuard(type)
    }
  }, userSocketOnStatusChanged)
})

// Close the socket on sign-out. userSocketDisconnect had two producers (the
// isSignedIn split and the not-signed filter) and ZERO consumers, so after
// logout the authorized socket kept streaming the tenant's node statuses,
// commands and metrics into the browser until the access token expired.
// This is that missing consumer.
userSocketDisconnect.watch(() => {
  if (currentSocket) {
    currentSocket.disconnect()
    currentSocket = null
  }
})

// Reconnect with exponential backoff only when the connection actually closed
// (not closed-by-plan / signed out). The backoff guards against a tight loop when
// the network is down or a token refresh keeps failing.
const $reconnectAttempt = createStore(0)

const waitBeforeReconnectFx = createEffect(async (attempt: number) => {
  const delay = reconnectDelayMs(attempt)
  await new Promise((resolve) => {
    setTimeout(resolve, delay)
  })
})

// One backoff chain at a time: a 'closed' that arrives while a wait is already
// pending joins it instead of starting a second timer. Parallel timers each
// open a socket, and each of those failures starts more.
sample({
  clock: userSocketOnStatusChanged,
  source: {
    shouldConnect: shouldConnectToSocket,
    attempt: $reconnectAttempt,
    waiting: waitBeforeReconnectFx.pending,
  },
  filter: ({ shouldConnect, waiting }, status) =>
    shouldConnect && !waiting && status === 'closed',
  fn: ({ attempt }) => attempt,
  target: waitBeforeReconnectFx,
})

sample({
  clock: waitBeforeReconnectFx.done,
  // Skip when already connected: the visibility kick below can reconnect while
  // this backoff sleep is still pending, and tearing down that fresh socket to
  // open another would be a pointless churn plus a spurious resync.
  source: $socketStatus,
  filter: (status) => status !== 'connected',
  target: userSocketConnect,
})

// Grow the delay per attempt; reset once we are connected again.
$reconnectAttempt.on(waitBeforeReconnectFx, (attempt) => attempt + 1)

const socketConnected = sample({
  clock: userSocketOnStatusChanged,
  filter: (status) => status === 'connected',
})

// A reconnect always passes through waitBeforeReconnectFx, which increments
// $reconnectAttempt to >=1 BEFORE reconnecting; the first-ever connect leaves it
// at 0. So the attempt count read AT connect time (before the reset below) tells
// a reconnect apart from a first connect. Read as sample source, it is the
// pre-reset value.
sample({
  clock: socketConnected,
  source: $reconnectAttempt,
  filter: (attempt) => attempt > 0,
  target: userSocketReconnected,
})

$reconnectAttempt.reset(socketConnected)
// Sign-out is a clean slate: the next connect is a first connect, not a
// reconnect, so it must not trigger a resync.
$reconnectAttempt.reset(userSocketDisconnect)

// A background tab's timers are throttled, so the backoff sleep gating a
// reconnect can effectively pause while the user is away — they come back to a
// page that looks live and is not, with the badge as the only hint.
// The moment the tab is visible again with the socket down, reconnect
// NOW instead of waiting out a stretched backoff. userSocketConnect tears down
// any half-open socket first, so racing a sleeping backoff timer is harmless.
const tabBecameVisible = createEvent()
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') tabBecameVisible()
  })
}
sample({
  clock: tabBecameVisible,
  source: { shouldConnect: shouldConnectToSocket, status: $socketStatus },
  filter: ({ shouldConnect, status }) => shouldConnect && status === 'closed',
  target: userSocketConnect,
})

export const useUserSocket = () => {
  const status = useUnit($socketStatus)
  return { status }
}
