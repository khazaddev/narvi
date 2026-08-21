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
    editRefusal: null,
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
  // The rule lives on the server (refusalReasonForMutation); this maps its
  // verdict to copy. So what is worth pinning here is that each reason keeps
  // its OWN remedy -- duplicating and unbinding are different actions, and
  // §25.10 forbids collapsing them into one message.
  it('renders the built-in refusal, saying it holds for admins too', () => {
    const refusal = structuralRefusalFor(baseDefinition({ editRefusal: 'built_in' }))
    expect(refusal?.kind).toBe('built_in')
    expect(refusal?.message).toMatch(/built-in/i)
    expect(refusal?.message).toMatch(/duplicate/i)
    expect(refusal?.message).toMatch(/admin/i)
  })

  it('renders the bound refusal, naming BOTH remedies', () => {
    const refusal = structuralRefusalFor(baseDefinition({ editRefusal: 'bound' }))
    expect(refusal?.kind).toBe('bound')
    expect(refusal?.message).toMatch(/duplicate/i)
    expect(refusal?.message).toMatch(/unbind/i)
  })

  it('renders the run-history refusal, explaining the freeze rather than just asserting it', () => {
    const refusal = structuralRefusalFor(baseDefinition({ editRefusal: 'has_runs' }))
    expect(refusal?.kind).toBe('has_runs')
    expect(refusal?.message).toMatch(/duplicate/i)
    // The surprising part is WHY a single run freezes a draft; a refusal that
    // only asserts is one an operator works around rather than understands.
    expect(refusal?.message).toMatch(/run/i)
  })

  it('gives the three reasons three DIFFERENT messages', () => {
    const messages = (['built_in', 'bound', 'has_runs'] as const).map((r) => structuralRefusalFor(baseDefinition({ editRefusal: r }))?.message)
    expect(new Set(messages).size).toBe(3)
  })

  it('returns null when the server says the definition is editable', () => {
    expect(structuralRefusalFor(baseDefinition({ editRefusal: null }))).toBeNull()
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
