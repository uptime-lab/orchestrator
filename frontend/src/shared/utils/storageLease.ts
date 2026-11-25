// A cross-tab mutex built on localStorage, for browsers without the Web Locks
// API — which is every page outside a secure context, i.e. an install served
// over plain HTTP.
//
// The lease is a record {owner, expires} under one key. A tab claims it only
// when it is absent or expired, then re-reads it after a short settle: two tabs
// that claimed at the same moment both wrote, the last write won, and only the
// winner sees its own id. The expiry is what frees the lease when its holder
// closes or crashes mid-run.
//
// It is best effort, not a guarantee: past maxWaitMs a waiter runs anyway,
// because a stuck lease must not stop a tab from ever refreshing its session.
// The caller is expected to re-check shared state once it holds the lease.

export type LeaseStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

export type LeaseOptions = {
  storage: LeaseStorage
  key: string
  ownerId: string
  // How long a claim stays valid without its holder releasing it.
  ttlMs: number
  // How long a waiter waits before running without the lease.
  maxWaitMs: number
  settleMs: number
  pollMs: number
  now?: () => number
  sleep?: (ms: number) => Promise<void>
}

type Lease = { owner: string; expires: number }

const defaultSleep = (ms: number) =>
  new Promise<void>((resolve) => {
    setTimeout(resolve, ms)
  })

const readLease = (storage: LeaseStorage, key: string): Lease | null => {
  try {
    const raw = storage.getItem(key)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<Lease>
    if (typeof parsed.owner !== 'string' || typeof parsed.expires !== 'number')
      return null
    return { owner: parsed.owner, expires: parsed.expires }
  } catch {
    return null
  }
}

export const withStorageLease = async <T>(
  opts: LeaseOptions,
  fn: () => Promise<T>,
): Promise<T> => {
  const now = opts.now ?? Date.now
  const sleep = opts.sleep ?? defaultSleep
  const { storage, key, ownerId } = opts
  const deadline = now() + opts.maxWaitMs
  let held = false

  for (;;) {
    const current = readLease(storage, key)
    if (!current || current.expires <= now()) {
      try {
        storage.setItem(
          key,
          JSON.stringify({ owner: ownerId, expires: now() + opts.ttlMs }),
        )
      } catch {
        // Storage refused (quota, privacy mode): there is no lease to take.
        break
      }
      await sleep(opts.settleMs)
      if (readLease(storage, key)?.owner === ownerId) {
        held = true
        break
      }
    }
    if (now() >= deadline) break
    await sleep(opts.pollMs)
  }

  try {
    return await fn()
  } finally {
    if (held && readLease(storage, key)?.owner === ownerId) {
      try {
        storage.removeItem(key)
      } catch {
        // Expiry frees it.
      }
    }
  }
}
