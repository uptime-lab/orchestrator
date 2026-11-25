import { describe, expect, it } from 'vitest'

import type { OU } from '@/entities/ou'
import type { WorkflowTemplate } from '@/entities/workflow/api/types'
import { isWorkflowAvailableForOU } from '@/entities/workflow/lib/scope'

import { replacementUnitId } from './replaceNodeScope'

const ou = (id: string, parent_id: string | null = null): OU =>
  ({ id, parent_id }) as OU

const playbook = (id: string, ou_ids: string[]): WorkflowTemplate =>
  ({ id, name: id, ou_ids }) as unknown as WorkflowTemplate

describe('replacementUnitId', () => {
  const ous = [ou('site-a'), ou('spares')]
  const siteA = playbook('site-a-playbook', ['site-a'])
  const spares = playbook('spares-playbook', ['spares'])

  it('is the source unit when the source has one', () => {
    expect(replacementUnitId({ ou_id: 'site-a' }, { ou_id: 'spares' })).toBe(
      'site-a',
    )
  })

  it('offers what the server would run on the target after the move', () => {
    const unit = replacementUnitId({ ou_id: 'site-a' }, { ou_id: 'spares' })
    expect(isWorkflowAvailableForOU(siteA, unit, ous)).toBe(true)
    expect(isWorkflowAvailableForOU(spares, unit, ous)).toBe(false)
  })

  it('keeps the target unit when the source has none', () => {
    expect(replacementUnitId({ ou_id: undefined }, { ou_id: 'spares' })).toBe(
      'spares',
    )
    expect(replacementUnitId({ ou_id: '' }, { ou_id: 'spares' })).toBe('spares')
  })

  it('is the source unit before a target is picked', () => {
    expect(replacementUnitId({ ou_id: 'site-a' }, undefined)).toBe('site-a')
  })

  it('is no unit when neither node has one', () => {
    expect(replacementUnitId({}, {})).toBeUndefined()
    expect(replacementUnitId(undefined, undefined)).toBeUndefined()
  })
})
