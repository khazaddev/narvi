import { describe, expect, it } from 'vitest'

import type { WorkflowBinding, WorkflowDefinition, WorkflowStepDefinition } from '@narvi/contracts/rest-dtos'

import { edgeStatusColorVar, edgeStatusTone, laneLabel, nextStepOrder, structuralRefusalFor, summarizeBindingsForDefinition } from '../workflowFormat'

function baseStep(overrides: Partial<WorkflowStepDefinition> = {}): WorkflowStepDefinition {
  return {
    id: 's1',
    order: 1,
    kind: 'agent',
    modelId: null,
    effort: null,
    promptTemplate: 'do the thing',
    executionScope: 'same_session',
    conversationContinuity: 'continue',
    hitlBefore: false,
    hitlAfter: false,
    edges: [],
    ...overrides,
  }
}

function baseDefinition(overrides: Partial<WorkflowDefinition> = {}): WorkflowDefinition {
  return {
    id: 'd1',
    lane: 'request',
    name: 'My workflow',
    isBuiltIn: false,
    version: 1,
    steps: [baseStep()],
    createdAt: '2026-08-20T00:00:00Z',
    updatedAt: '2026-08-20T00:00:00Z',
    ...overrides,
  }
}

function baseBinding(overrides: Partial<WorkflowBinding> = {}): WorkflowBinding {
  return {
    id: 'b1',
    lane: 'request',
    repoFullName: null,
    workflowDefinitionId: 'd1',
    definitionVersion: 1,
    createdAt: '2026-08-20T00:00:00Z',
    updatedAt: '2026-08-20T00:00:00Z',
    ...overrides,
  }
}

describe('laneLabel', () => {
  it('capitalizes the 3 closed lane values, and passes through anything else unchanged', () => {
    expect(laneLabel('review')).toBe('Review')
    expect(laneLabel('request')).toBe('Request')
    expect(laneLabel('plan')).toBe('Plan')
    expect(laneLabel('mystery')).toBe('mystery')
  })
})

describe('edgeStatusTone / edgeStatusColorVar', () => {
  it('maps ok/needs_fix/blocked to the established chip-tone vocabulary and their backing tokens', () => {
    expect(edgeStatusTone('ok')).toBe('ok')
    expect(edgeStatusTone('needs_fix')).toBe('warn')
    expect(edgeStatusTone('blocked')).toBe('crit')
    expect(edgeStatusTone('unknown')).toBe('neutral')

    expect(edgeStatusColorVar('ok')).toBe('var(--ok)')
    expect(edgeStatusColorVar('needs_fix')).toBe('var(--warn)')
    expect(edgeStatusColorVar('blocked')).toBe('var(--crit)')
    expect(edgeStatusColorVar('unknown')).toBe('var(--faint)')
  })
})

describe('summarizeBindingsForDefinition', () => {
  it('reports global=false and no repos when nothing binds this definition', () => {
    const summary = summarizeBindingsForDefinition([baseBinding({ workflowDefinitionId: 'other' })], 'd1')
    expect(summary).toEqual({ global: false, repos: [] })
  })

  it('reports global=true when a repoFullName:null binding targets this definition', () => {
    const summary = summarizeBindingsForDefinition([baseBinding({ repoFullName: null, workflowDefinitionId: 'd1' })], 'd1')
    expect(summary.global).toBe(true)
    expect(summary.repos).toEqual([])
  })

  it('collects every repo override binding targeting this definition, ignoring bindings for other definitions', () => {
    const bindings = [
      baseBinding({ id: 'b1', repoFullName: 'acme/widgets', workflowDefinitionId: 'd1' }),
      baseBinding({ id: 'b2', repoFullName: 'acme/storefront', workflowDefinitionId: 'd1' }),
      baseBinding({ id: 'b3', repoFullName: 'acme/other', workflowDefinitionId: 'd2' }),
    ]
    const summary = summarizeBindingsForDefinition(bindings, 'd1')
    expect(summary.global).toBe(false)
    expect(summary.repos.sort()).toEqual(['acme/storefront', 'acme/widgets'])
  })
})

describe('structuralRefusalFor', () => {
  it('refuses a built-in definition regardless of any binding state', () => {
    const refusal = structuralRefusalFor(baseDefinition({ isBuiltIn: true }), [])
    expect(refusal?.kind).toBe('built_in')
    expect(refusal?.message).toMatch(/built-in/i)
    expect(refusal?.message).toMatch(/duplicate/i)
  })

  it('refuses a non-built-in definition bound globally, naming the global binding', () => {
    const def = baseDefinition({ id: 'd1', isBuiltIn: false })
    const refusal = structuralRefusalFor(def, [baseBinding({ repoFullName: null, workflowDefinitionId: 'd1' })])
    expect(refusal?.kind).toBe('bound')
    expect(refusal?.message).toMatch(/global binding/i)
  })

  it('refuses a non-built-in definition bound by a repo override, naming the repo', () => {
    const def = baseDefinition({ id: 'd1', isBuiltIn: false })
    const refusal = structuralRefusalFor(def, [baseBinding({ repoFullName: 'acme/widgets', workflowDefinitionId: 'd1' })])
    expect(refusal?.kind).toBe('bound')
    expect(refusal?.message).toContain('acme/widgets')
  })

  it('returns null for an unbound, non-built-in definition -- editable as far as this screen can tell', () => {
    const def = baseDefinition({ id: 'd1', isBuiltIn: false })
    expect(structuralRefusalFor(def, [baseBinding({ workflowDefinitionId: 'other-definition' })])).toBeNull()
  })
})

describe('nextStepOrder', () => {
  it('returns 1 for an empty definition', () => {
    expect(nextStepOrder([])).toBe(1)
  })

  it('returns one past the current maximum order, regardless of array order', () => {
    expect(nextStepOrder([3, 1, 2])).toBe(4)
    expect(nextStepOrder([5])).toBe(6)
  })
})
