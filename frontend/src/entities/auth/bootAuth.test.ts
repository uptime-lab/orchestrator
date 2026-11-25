import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Page-load authentication against a server that fails in different ways. The
// model is imported fresh per test: its stores and timers are module state.

const getCurrentUser = vi.fn()
const postRefreshToken = vi.fn()
const postLogout = vi.fn()

vi.mock('@/entities/user/api', () => ({
  apiUsers: { getCurrentUser: () => getCurrentUser() },
}))
vi.mock('@/entities/auth/api', () => ({
  apiAuth: {
    postRefreshToken: (body: unknown) => postRefreshToken(body),
    postLogout: (token: string) => postLogout(token),
  },
}))

const memoryStorage = (): Storage => {
  const items = new Map<string, string>()
  return {
    get length() {
      return items.size
    },
    clear: () => items.clear(),
    getItem: (key) => items.get(key) ?? null,
    key: (index) => [...items.keys()][index] ?? null,
    removeItem: (key) => {
      items.delete(key)
    },
    setItem: (key, value) => {
      items.set(key, String(value))
    },
  }
}

const axiosFailure = (status?: number) =>
  Object.assign(new Error(status ? `status ${status}` : 'Network Error'), {
    isAxiosError: true,
    code: status ? 'ERR_BAD_RESPONSE' : 'ERR_NETWORK',
    response: status ? { status } : undefined,
  })

const user = { id: 'u1', email: 'a@example.com', name: 'A' }

const loadModel = async () => {
  vi.resetModules()
  return import('./model')
}

const runInit = async (model: Awaited<ReturnType<typeof loadModel>>) => {
  await model.initAuthFx().catch(() => undefined)
}

beforeEach(() => {
  vi.stubGlobal('localStorage', memoryStorage())
  vi.stubGlobal('sessionStorage', memoryStorage())
  vi.stubGlobal('window', {
    location: { pathname: '/nodes', assign: vi.fn() },
    addEventListener: vi.fn(),
  })
  getCurrentUser.mockReset()
  postRefreshToken.mockReset()
  postLogout.mockReset().mockResolvedValue(undefined)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('page-load authentication', () => {
  it.each([
    ['a server error', axiosFailure(503)],
    ['a rate limit', axiosFailure(429)],
    ['a lost request', axiosFailure()],
  ])('keeps the session and offers a retry on %s', async (_name, failure) => {
    localStorage.setItem('access_token', 'access')
    localStorage.setItem('refresh_token', 'refresh')
    getCurrentUser.mockRejectedValue(failure)
    const model = await loadModel()

    await runInit(model)

    expect(postLogout).not.toHaveBeenCalled()
    expect(localStorage.getItem('access_token')).toBe('access')
    expect(localStorage.getItem('refresh_token')).toBe('refresh')
    expect(model.$authState.getState().status).not.toBe('loggedOut')
    expect(model.$bootUnreachable.getState()).toBe(true)
  })

  it('keeps the refresh token when the refresh fails transiently', async () => {
    localStorage.setItem('refresh_token', 'refresh')
    postRefreshToken.mockRejectedValue(axiosFailure(502))
    const model = await loadModel()

    await runInit(model)

    expect(localStorage.getItem('refresh_token')).toBe('refresh')
    expect(postLogout).not.toHaveBeenCalled()
    expect(model.$bootUnreachable.getState()).toBe(true)
  })

  it('drops the refresh token the server refused', async () => {
    localStorage.setItem('refresh_token', 'refresh')
    postRefreshToken.mockRejectedValue(axiosFailure(401))
    const model = await loadModel()

    await runInit(model)

    expect(localStorage.getItem('refresh_token')).toBeNull()
    expect(model.$authState.getState().status).toBe('loggedOut')
    expect(model.$bootUnreachable.getState()).toBe(false)
  })

  it('ends the session when the user is refused', async () => {
    localStorage.setItem('access_token', 'access')
    localStorage.setItem('refresh_token', 'refresh')
    getCurrentUser.mockRejectedValue(axiosFailure(404))
    const model = await loadModel()

    await runInit(model)

    expect(model.$authState.getState().status).toBe('loggedOut')
    expect(localStorage.getItem('access_token')).toBeNull()
  })

  it('tries again on its own and signs in once the server answers', async () => {
    vi.useFakeTimers()
    localStorage.setItem('access_token', 'access')
    localStorage.setItem('refresh_token', 'refresh')
    getCurrentUser
      .mockRejectedValueOnce(axiosFailure(503))
      .mockResolvedValueOnce(user)
    const model = await loadModel()

    await runInit(model)
    expect(getCurrentUser).toHaveBeenCalledTimes(1)

    await vi.runOnlyPendingTimersAsync()

    expect(getCurrentUser).toHaveBeenCalledTimes(2)
    expect(model.$authState.getState().status).toBe('loggedIn')
    expect(model.$bootUnreachable.getState()).toBe(false)
  })

  it('retries at once when asked', async () => {
    vi.useFakeTimers()
    localStorage.setItem('access_token', 'access')
    localStorage.setItem('refresh_token', 'refresh')
    getCurrentUser
      .mockRejectedValueOnce(axiosFailure())
      .mockResolvedValueOnce(user)
    const model = await loadModel()

    await runInit(model)
    model.retryAuthInit()
    await vi.waitFor(() =>
      expect(model.$authState.getState().status).toBe('loggedIn'),
    )
    expect(getCurrentUser).toHaveBeenCalledTimes(2)
  })
})
