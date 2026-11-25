import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const ensureFreshAccessToken = vi.fn<() => Promise<string | null>>()

vi.mock('@/shared/utils/apiConfig.ts', () => ({
  ensureFreshAccessToken: () => ensureFreshAccessToken(),
  getWebsocketUrl: () => 'ws://test',
}))

class FakeSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: FakeSocket[] = []

  readyState = FakeSocket.CONNECTING
  onopen: (() => unknown) | null = null
  onclose: ((e: { code: number; reason: string }) => unknown) | null = null
  onerror: ((e: { type: string }) => unknown) | null = null
  onmessage: ((e: { data: string }) => unknown) | null = null
  sent: string[] = []

  constructor(public url: string) {
    FakeSocket.instances.push(this)
  }

  // A real socket closed while still connecting fails the connection, which
  // fires `error` and then `close`.
  close() {
    if (this.readyState === FakeSocket.CLOSED) return
    const wasConnecting = this.readyState === FakeSocket.CONNECTING
    this.readyState = FakeSocket.CLOSED
    if (wasConnecting) this.onerror?.({ type: 'error' })
    this.onclose?.({ code: 1006, reason: '' })
  }

  send(data: string) {
    this.sent.push(data)
  }

  // What the browser does when the handshake fails: error, then close.
  failHandshake() {
    this.readyState = FakeSocket.CLOSED
    this.onerror?.({ type: 'error' })
    this.onclose?.({ code: 1006, reason: '' })
  }
}

describe('userCommonWebSocket', () => {
  beforeEach(() => {
    FakeSocket.instances = []
    ensureFreshAccessToken.mockReset()
    vi.stubGlobal('WebSocket', FakeSocket)
    vi.spyOn(console, 'log').mockImplementation(() => {})
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  const open = async () => {
    const { userCommonWebSocket } = await import('./userCommonWebSocket')
    const statuses: string[] = []
    const result = await userCommonWebSocket(
      () => {},
      (s) => statuses.push(s),
    )
    return {
      result,
      statuses,
      socket: FakeSocket.instances[FakeSocket.instances.length - 1],
    }
  }

  it('reports a failed handshake as closed exactly once', async () => {
    const { statuses, socket } = await open()
    socket.failHandshake()
    expect(statuses.filter((s) => s === 'closed')).toHaveLength(1)
  })

  it('reports nothing but closed-by-plan for a socket torn down on purpose', async () => {
    const { result, statuses } = await open()
    result.disconnect()
    expect(statuses).toEqual(['closed-by-plan'])
  })

  it('closes as a retryable failure when the refresh fails transiently', async () => {
    ensureFreshAccessToken.mockRejectedValue(new Error('503'))
    const { statuses, socket } = await open()
    socket.readyState = FakeSocket.OPEN
    await socket.onopen?.()
    expect(statuses).toEqual(['closed'])
  })

  it('reports connected only once the server authorizes the socket', async () => {
    ensureFreshAccessToken.mockResolvedValue('token')
    const { statuses, socket } = await open()
    socket.readyState = FakeSocket.OPEN
    await socket.onopen?.()
    expect(statuses).toEqual([])
    expect(socket.sent).toHaveLength(1)
    socket.onmessage?.({
      data: JSON.stringify({
        type: 'authorization_status',
        payload: { status: 'ok' },
      }),
    })
    expect(statuses).toEqual(['connected'])
  })

  it('treats a refused authorization as a closed socket, not a live one', async () => {
    ensureFreshAccessToken.mockResolvedValue('token')
    const { statuses, socket } = await open()
    socket.readyState = FakeSocket.OPEN
    await socket.onopen?.()
    socket.onmessage?.({
      data: JSON.stringify({
        type: 'authorization_status',
        payload: { status: 'failed', error: 'membership_required' },
      }),
    })
    expect(statuses).toEqual(['closed'])
    expect(socket.readyState).toBe(FakeSocket.CLOSED)
  })

  it('stops for good when the session is over', async () => {
    ensureFreshAccessToken.mockResolvedValue(null)
    const { statuses, socket } = await open()
    socket.readyState = FakeSocket.OPEN
    await socket.onopen?.()
    expect(statuses).toEqual(['closed-by-plan'])
  })
})
