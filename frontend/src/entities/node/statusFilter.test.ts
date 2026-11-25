import { describe, expect, it } from 'vitest'

import type { DeletedNode } from '@/entities/node/api/deletedNodes'
import type { NodeData } from '@/entities/node/api/types.ts'

import {
  deletedNodeMatchesQuery,
  matchesStatusFilter,
  NODE_STATUS_FILTERS,
  nodeStatusBucket,
  statusFilterShowsDeleted,
} from './statusFilter'

const node = (status: string, over: Partial<NodeData> = {}): NodeData =>
  ({
    id: 'n1',
    name: 'node',
    status,
    ...over,
  }) as unknown as NodeData

const netbooted = (status: string): NodeData =>
  node(status, {
    system_info: {
      hardware: { boot: { current_boot_device: 'http' } },
    },
  } as unknown as Partial<NodeData>)

describe('nodeStatusBucket', () => {
  it('puts a plain online node under online', () => {
    expect(nodeStatusBucket(node('online'))).toBe('online')
  })

  it('puts offline, unreachable and shutdown under offline', () => {
    for (const s of ['offline', 'unreachable', 'shutdown']) {
      expect(nodeStatusBucket(node(s))).toBe('offline')
    }
  })

  it('treats a missing or unknown status as offline, like the row does', () => {
    expect(nodeStatusBucket(node(''))).toBe('offline')
    expect(nodeStatusBucket(node('no-such-status'))).toBe('offline')
  })

  it('puts a disk-booted node in maintenance under maintenance', () => {
    expect(nodeStatusBucket(node('maintenance'))).toBe('maintenance')
  })

  it('lets recovery win over maintenance for a netbooted node', () => {
    // The row labels this node Recovery; listing it under Maintenance would
    // show a violet row under a filter that never mentions recovery.
    expect(nodeStatusBucket(netbooted('maintenance'))).toBe('recovery')
    expect(nodeStatusBucket(netbooted('online'))).toBe('recovery')
  })

  it('keeps transitional and adoption states reachable through online', () => {
    for (const s of [
      'restarting',
      'busy',
      'os_installing',
      'standby',
      'ready_for_adoption',
      'adopting',
      'flashing',
    ]) {
      expect(nodeStatusBucket(node(s))).toBe('online')
    }
  })
})

describe('matchesStatusFilter', () => {
  it('lists every live node under all and none under deleted', () => {
    for (const s of ['online', 'offline', 'maintenance']) {
      expect(matchesStatusFilter(node(s), 'all')).toBe(true)
      expect(matchesStatusFilter(node(s), 'deleted')).toBe(false)
    }
  })

  it('partitions the fleet: every node matches exactly one non-all option', () => {
    const fleet = [
      node('online'),
      node('offline'),
      node('maintenance'),
      netbooted('maintenance'),
      node('restarting'),
      node('unreachable'),
    ]
    const options = NODE_STATUS_FILTERS.filter((f) => f !== 'all')
    for (const n of fleet) {
      const hits = options.filter((f) => matchesStatusFilter(n, f))
      expect(hits).toHaveLength(1)
    }
  })
})

describe('statusFilterShowsDeleted', () => {
  it('shows tombstones under all and deleted only', () => {
    expect(statusFilterShowsDeleted('all')).toBe(true)
    expect(statusFilterShowsDeleted('deleted')).toBe(true)
    for (const f of ['online', 'recovery', 'maintenance', 'offline'] as const) {
      expect(statusFilterShowsDeleted(f)).toBe(false)
    }
  })
})

describe('deletedNodeMatchesQuery', () => {
  const row = {
    id: 'n1',
    name: 'Node-abcd0001',
    hostname: 'rack3-blade',
    hardware_serial: '10000000abcd0001',
  } as DeletedNode

  it('matches name, hostname and serial, case-insensitively', () => {
    expect(deletedNodeMatchesQuery(row, 'abcd')).toBe(true)
    expect(deletedNodeMatchesQuery(row, 'rack3')).toBe(true)
    expect(deletedNodeMatchesQuery(row, '10000000')).toBe(true)
  })

  it('matches everything on an empty query and nothing on a miss', () => {
    expect(deletedNodeMatchesQuery(row, '')).toBe(true)
    expect(deletedNodeMatchesQuery(row, 'ubuntu')).toBe(false)
  })
})
