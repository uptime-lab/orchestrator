import { beforeEach, describe, expect, it, vi } from 'vitest'

// The skip reasons are worded from the locale file, so the bundle has to be
// initialised before the first t() call.
import '@/i18n'

const postBatchNodeCommand = vi.fn()
const postClearBootFailsafe = vi.fn()
const provisionEEPROM = vi.fn()
const approveNode = vi.fn()
const applyNodeUtilities = vi.fn()
const applyDeviceProfile = vi.fn()
const rebootNode = vi.fn()
const reapplyNodeAccess = vi.fn()

vi.mock('@/entities/node/api', () => ({
  apiNodes: {
    postBatchNodeCommand: (...a: unknown[]) => postBatchNodeCommand(...a),
    postClearBootFailsafe: (...a: unknown[]) => postClearBootFailsafe(...a),
    provisionEEPROM: (...a: unknown[]) => provisionEEPROM(...a),
    approveNode: (...a: unknown[]) => approveNode(...a),
    rebootNode: (...a: unknown[]) => rebootNode(...a),
  },
}))
vi.mock('@/entities/utility/api', () => ({
  applyNodeUtilities: (...a: unknown[]) => applyNodeUtilities(...a),
}))
vi.mock('@/entities/deviceProfile/api', () => ({
  applyDeviceProfile: (...a: unknown[]) => applyDeviceProfile(...a),
}))
vi.mock('@/entities/access/api', () => ({
  reapplyNodeAccess: (...a: unknown[]) => reapplyNodeAccess(...a),
}))

const { remedyFor, remedyForItem, DISPATCH_REMEDIES } = await import(
  './remedies'
)

const targets = [
  { nodeId: 'n1', nodeName: 'alpha' },
  { nodeId: 'n2', nodeName: 'beta' },
  { nodeId: 'n3', nodeName: 'gamma' },
]

beforeEach(() => {
  vi.clearAllMocks()
})

describe('the agent-update remedy words a skip', () => {
  it('names a node already on the requested version', async () => {
    postBatchNodeCommand.mockResolvedValue({
      results: [
        {
          node_id: 'n1',
          success: false,
          skipped: true,
          skip_reason: 'agent_already_current',
          error: 'node n1 already runs agent 1.0.0',
        },
      ],
      total_count: 1,
    })
    const out = await remedyFor('agent_outdated')!.run!(targets)
    expect(out[0].state).toBe('skipped')
    expect(out[0].reason).toBe('already on this agent version')
  })
})

// The batch endpoint answers HTTP 200 with per-node outcomes, and three of
// them are not success. Collapsing any of these into "sent" is how a queued
// command — accepted, not applied — reads as a finished one.
describe('the agent-update remedy maps every batch outcome', () => {
  it('separates dispatched, queued, skipped and failed', async () => {
    postBatchNodeCommand.mockResolvedValue({
      results: [
        { node_id: 'n1', success: true, command_id: 'c1' },
        { node_id: 'n2', success: true, queued: true },
        {
          node_id: 'n3',
          success: false,
          skipped: true,
          skip_reason: 'agent_too_old',
        },
        { node_id: 'n4', success: false, error: 'node is a watcher' },
      ],
      total_count: 4,
    })
    const remedy = remedyFor('agent_outdated')!
    const out = await remedy.run!(targets)

    expect(out.map((o) => o.state)).toEqual([
      'dispatched',
      'queued',
      'skipped',
      'failed',
    ])
    expect(out[2].reason).toBe('the agent is too old for this command')
    expect(out[3].reason).toBe('node is a watcher')
    // A node the response names but the selection did not is labelled by id
    // rather than rendered blank.
    expect(out[3].nodeName).toBe('n4')
    expect(out[0].nodeName).toBe('alpha')
  })

  it('sends one request for the whole selection', async () => {
    postBatchNodeCommand.mockResolvedValue({ results: [], total_count: 0 })
    await remedyFor('agent_outdated')!.run!(targets)
    expect(postBatchNodeCommand).toHaveBeenCalledTimes(1)
    expect(postBatchNodeCommand).toHaveBeenCalledWith({
      node_ids: ['n1', 'n2', 'n3'],
      command: 'update_agent',
      params: { agent_version: 'latest', allow_downgrade: false },
    })
  })
})

// The per-node remedies fan out over endpoints that have no batch form. One
// node refusing must not cost the others their attempt.
describe('a per-node remedy', () => {
  it('records a failure per node and still runs the rest', async () => {
    postClearBootFailsafe.mockImplementation((nodeId: string) =>
      nodeId === 'n2'
        ? Promise.reject(new Error('node not found'))
        : Promise.resolve(),
    )
    const out = await remedyFor('boot_failsafe')!.run!(targets)

    expect(postClearBootFailsafe).toHaveBeenCalledTimes(3)
    expect(out.map((o) => [o.nodeId, o.state])).toEqual([
      ['n1', 'dispatched'],
      ['n2', 'failed'],
      ['n3', 'dispatched'],
    ])
    expect(out[1].reason).toBe('node not found')
  })

  it('words a device-limit refusal of an approval from its code', async () => {
    approveNode.mockImplementation((nodeId: string) =>
      Promise.reject(
        nodeId === 'n1'
          ? {
              response: {
                status: 503,
                data: {
                  code: 'device_cap_unverified',
                  message: 'The device limit could not be checked.',
                },
              },
            }
          : nodeId === 'n2'
            ? {
                response: {
                  status: 409,
                  data: {
                    code: 'device_cap_reached',
                    message: 'device cap reached',
                    data: { instance_operator: false },
                  },
                },
              }
            : new Error('node not found'),
      ),
    )
    const out = await remedyFor('pending_approval')!.run!(targets)

    expect(out.map((o) => o.state)).toEqual(['failed', 'failed', 'failed'])
    expect(out[0].reason).toBe(
      'The device limit could not be checked, so nothing was added. Try again in a moment.',
    )
    expect(out[1].reason).toMatch(/^This install's device limit is reached/)
    expect(out[2].reason).toBe('node not found')
  })

  it('keeps the results in the order of the selection', async () => {
    // Resolve out of order on purpose: the results dialog lists nodes, and a
    // list whose order depends on which server answered first is unreadable.
    const delays: Record<string, number> = { n1: 20, n2: 0, n3: 10 }
    approveNode.mockImplementation(
      (nodeId: string) =>
        new Promise((resolve) => setTimeout(resolve, delays[nodeId])),
    )
    const out = await remedyFor('pending_approval')!.run!(targets)
    expect(out.map((o) => o.nodeId)).toEqual(['n1', 'n2', 'n3'])
  })

  it('reads the utility apply result rather than assuming success', async () => {
    applyNodeUtilities.mockImplementation((nodeId: string) =>
      Promise.resolve(
        nodeId === 'n1'
          ? {
              node_id: nodeId,
              node_name: 'alpha',
              queued: false,
              skipped: false,
            }
          : nodeId === 'n2'
            ? {
                node_id: nodeId,
                node_name: 'beta',
                queued: true,
                skipped: false,
              }
            : {
                node_id: nodeId,
                node_name: 'gamma',
                queued: false,
                skipped: true,
                reason: 'watcher node',
              },
      ),
    )
    const out = await remedyFor('utility_drift')!.run!(targets)
    expect(out.map((o) => o.state)).toEqual(['dispatched', 'queued', 'skipped'])
    expect(out[2].reason).toBe('watcher node')
  })

  it('reports a queued device-profile apply as queued', async () => {
    applyDeviceProfile.mockResolvedValue({ queued: true })
    const out = await remedyFor('device_profile_pending')!.run!([targets[0]])
    expect(out[0].state).toBe('queued')
  })
})

// The confirmation reads these flags to decide what to say and whether to
// demand a tick. Both were wrong once: the bootloader flash was marked as a
// reboot it does not perform, which is worse than saying nothing, because the
// operator then believes the update is finished.
describe('the reboot and acknowledgement flags', () => {
  it('marks a reboot only where the device actually goes down', () => {
    // The flash writes firmware and releases the node. The agent's own words:
    // "Reboot required for the new boot order to take effect."
    expect(remedyFor('bootloader_outdated')?.reboots).toBeFalsy()
    expect(remedyFor('bootloader_reboot_pending')?.reboots).toBe(true)
    expect(remedyFor('pending_approval')?.reboots).toBeFalsy()
    expect(remedyFor('agent_outdated')?.reboots).toBeFalsy()
    expect(remedyFor('utility_drift')?.reboots).toBeFalsy()
    expect(remedyFor('boot_failsafe')?.reboots).toBeFalsy()
  })

  it('demands an acknowledgement for writing firmware and for rebooting', () => {
    expect(remedyFor('bootloader_outdated')?.acknowledge).toBe(true)
    expect(remedyFor('bootloader_reboot_pending')?.acknowledge).toBe(true)
    // The cheap, reversible ones stay one click; a tick on everything is a
    // tick nobody reads.
    expect(remedyFor('agent_outdated')?.acknowledge).toBeFalsy()
    expect(remedyFor('boot_failsafe')?.acknowledge).toBeFalsy()
    expect(remedyFor('utility_drift')?.acknowledge).toBeFalsy()
  })

  it('marks the remedies the platform will not dispatch to an absent node', () => {
    // reboot and update_boot_order are not queueable, and the dispatch gate
    // refuses anything non-queueable unless the node is online or in
    // maintenance — "node is not available (current status: restarting)". A
    // Reboot offered to a node already restarting is the case this pins.
    expect(remedyFor('bootloader_reboot_pending')?.requiresConnected).toBe(true)
    expect(remedyFor('bootloader_outdated')?.requiresConnected).toBe(true)
    // Queueable ones must NOT carry it: queuing an agent update for a node
    // that is away is the feature.
    expect(remedyFor('agent_outdated')?.requiresConnected).toBeFalsy()
    expect(remedyFor('device_profile_pending')?.requiresConnected).toBeFalsy()
    expect(remedyFor('utility_drift')?.requiresConnected).toBeFalsy()
    // And the two that dispatch nothing at all.
    expect(remedyFor('boot_failsafe')?.requiresConnected).toBeFalsy()
    expect(remedyFor('pending_approval')?.requiresConnected).toBeFalsy()
  })

  it('offers the reboot for both states that end in one', () => {
    // Firmware and hardware settings both come up on the next boot, and the
    // platform performs neither reboot by itself.
    for (const kind of [
      'bootloader_reboot_pending',
      'device_profile_reboot_pending',
    ]) {
      const remedy = remedyFor(kind)
      expect(remedy?.reboots).toBe(true)
      expect(remedy?.acknowledge).toBe(true)
      expect(remedy?.requiresConnected).toBe(true)
    }
  })

  it('separates a command held in recovery from one refused there', () => {
    // Held: queued until the node boots its OS.
    expect(remedyFor('agent_outdated')?.managedOSOnly).toBe(true)
    expect(remedyFor('agent_outdated')?.refusedInRecovery).toBeFalsy()
    // Refused: the ROM applies recovery.bin only from SD or USB, so a
    // netbooted device with no card cannot reflash its own EEPROM at all.
    expect(remedyFor('bootloader_outdated')?.refusedInRecovery).toBe(true)
    expect(remedyFor('bootloader_outdated')?.managedOSOnly).toBeFalsy()
  })
})

describe('the reboot remedy', () => {
  it('reboots each selected node, from either pending state', async () => {
    for (const kind of [
      'bootloader_reboot_pending',
      'device_profile_reboot_pending',
    ]) {
      rebootNode.mockClear()
      rebootNode.mockResolvedValue(undefined)
      const out = await remedyFor(kind)!.run!(targets)
      expect(rebootNode).toHaveBeenCalledTimes(3)
      expect(out.every((o) => o.state === 'dispatched')).toBe(true)
    }
  })
})

// A condition with no honest one-click answer must have no button.
describe('conditions with no remedy', () => {
  it('offers nothing for the ones a click cannot fix', () => {
    for (const kind of [
      'offline',
      'dangling_ou',
      'watcher_unrecoverable',
      'token_unreadable',
      'in_recovery',
      'awaiting_return',
    ]) {
      expect(remedyFor(kind)).toBeUndefined()
    }
  })
})

// The two flags the Attention tab reads before it draws a button. A remedy
// missing managedOSOnly is offered on a netbooted node, where the platform
// holds the command: it sits registered with no delivery attempt, and the
// operator sees a click that did nothing. That is the defect this pins: a
// netbooted node must never be handed such a command.
describe('the managed-OS flag', () => {
  it('is set on every remedy the platform holds in recovery', () => {
    expect(remedyFor('agent_outdated')?.managedOSOnly).toBe(true)
    expect(remedyFor('device_profile_pending')?.managedOSOnly).toBe(true)
    expect(remedyFor('utility_drift')?.managedOSOnly).toBe(true)
  })

  it('is absent from the ones recovery does not hold', () => {
    // An EEPROM flash is a recovery-capable operation, and clearing a failsafe
    // or approving a node touches platform state only — none is held.
    expect(remedyFor('bootloader_outdated')?.managedOSOnly).toBeFalsy()
    expect(remedyFor('boot_failsafe')?.managedOSOnly).toBeFalsy()
    expect(remedyFor('pending_approval')?.managedOSOnly).toBeFalsy()
  })
})

// A row claims a failure by matching the server's slug, so a remedy that
// dispatches a command must name the one it sends. Get this wrong and the
// failure attaches to the wrong condition, or to none.
describe('the dispatched slug', () => {
  it('names the command each remedy sends', () => {
    expect(remedyFor('agent_outdated')?.slug).toBe('update_agent')
    expect(remedyFor('bootloader_outdated')?.slug).toBe('update_boot_order')
    expect(remedyFor('device_profile_pending')?.slug).toBe(
      'apply_device_profile',
    )
    expect(remedyFor('utility_drift')?.slug).toBe('apply_utility_config')
    expect(remedyFor('bootloader_reboot_pending')?.slug).toBe('reboot')
    expect(remedyFor('device_profile_reboot_pending')?.slug).toBe('reboot')
  })

  it('is absent where the remedy sends the device nothing', () => {
    // Both are platform writes: no command is dispatched, so none can fail.
    expect(remedyFor('boot_failsafe')?.slug).toBeUndefined()
    expect(remedyFor('pending_approval')?.slug).toBeUndefined()
  })
})

describe('the access re-apply remedy', () => {
  it('reports a queued apply as queued, not as sent', async () => {
    // apply_access_config is managed-OS-only, so an offline or netbooted node
    // queues it. Calling that "sent" is how a revocation that has not landed
    // reads as one that has.
    reapplyNodeAccess.mockResolvedValue({ queued: true })
    const out = await remedyFor('access_apply_failed')!.run!([targets[0]])
    expect(out[0].state).toBe('queued')

    reapplyNodeAccess.mockResolvedValue({ queued: false })
    const sent = await remedyFor('access_apply_failed')!.run!([targets[0]])
    expect(sent[0].state).toBe('dispatched')
  })
})

// The device cap is lifted on the Subscription tab, not per node: the row
// links there, dispatches nothing, and never joins a bulk selection.
describe('the device-cap remedy', () => {
  it('opens the Subscription tab and is not a dispatch', () => {
    const remedy = remedyFor('device_cap_reached')
    expect(remedy).toBeDefined()
    expect(remedy!.opens).toEqual({
      to: '/account',
      search: { tab: 'subscription', sidebar: undefined },
    })
    expect(remedy!.run).toBeUndefined()
    expect(remedy!.action).toBe('entitlement.manage')
    expect(
      DISPATCH_REMEDIES().some((r) => r.kind === 'device_cap_reached'),
    ).toBe(false)
  })

  it('links only the operator, and an older backend as before', () => {
    expect(
      remedyForItem({ kind: 'device_cap_reached', instance_operator: true })
        ?.id,
    ).toBe('openSubscription')
    expect(remedyForItem({ kind: 'device_cap_reached' })?.id).toBe(
      'openSubscription',
    )
    expect(
      remedyForItem({ kind: 'device_cap_reached', instance_operator: false }),
    ).toBeUndefined()
    expect(remedyForItem({ kind: 'agent_outdated' })).toBe(
      remedyFor('agent_outdated'),
    )
  })
})
