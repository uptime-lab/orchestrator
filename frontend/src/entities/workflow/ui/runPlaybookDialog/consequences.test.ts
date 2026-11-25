import { describe, expect, it } from 'vitest'

import { runConsequences, runGate, type BlockConsequence } from './consequences'

const catalog = (...blocks: BlockConsequence[]) =>
  Object.fromEntries(blocks.map((b) => [b.type, b]))

const block = (
  type: string,
  name: string,
  dangerous = false,
  reboots_node = false,
): BlockConsequence => ({ type, name, dangerous, reboots_node })

describe('runConsequences', () => {
  it('names the destructive blocks by NAME, not by slug', () => {
    const result = runConsequences(
      ['command.erase_drive', 'command.reboot'],
      catalog(
        block('command.erase_drive', 'Erase drive', true, false),
        block('command.reboot', 'Reboot', false, true),
      ),
    )
    expect(result.dangerous).toEqual(['Erase drive'])
    expect(result.reboots).toEqual(['Reboot'])
  })

  it('reports each block once however many times the graph uses it', () => {
    const result = runConsequences(
      ['command.reboot', 'command.reboot', 'command.reboot'],
      catalog(block('command.reboot', 'Reboot', false, true)),
    )
    expect(result.reboots).toEqual(['Reboot'])
  })

  it('says nothing about an ordinary playbook', () => {
    const result = runConsequences(
      ['control.wait', 'command.status_check'],
      catalog(
        block('control.wait', 'Wait'),
        block('command.status_check', 'Status check'),
      ),
    )
    expect(result).toEqual({ dangerous: [], reboots: [] })
  })

  // A block the registry does not know is skipped rather than guessed at: the
  // catalog is the source of truth for what a block does, and inventing a
  // consequence would be a warning about nothing.
  it('skips a block the catalog does not describe', () => {
    const result = runConsequences(['command.from_the_future'], catalog())
    expect(result).toEqual({ dangerous: [], reboots: [] })
  })
})

describe('runGate', () => {
  const none = { dangerous: [], reboots: [] }
  const erase = { dangerous: ['Erase drive'], reboots: [] }

  // The consequences are read off the block registry. Before it has loaded,
  // a destructive playbook looked harmless and ran with no acknowledgement.
  it('holds the run until the block registry has loaded', () => {
    expect(runGate(false, none, false)).toBe('loading')
    expect(runGate(false, none, true)).toBe('loading')
  })

  it('asks for the acknowledgement of a destructive playbook', () => {
    expect(runGate(true, erase, false)).toBe('needs-acknowledgement')
    expect(runGate(true, erase, true)).toBe('ready')
    expect(runGate(true, none, false)).toBe('ready')
  })
})
