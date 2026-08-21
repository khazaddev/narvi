import { describe, expect, it } from 'vitest'

import type { WorkflowStepDefinition } from '@narvi/contracts/rest-dtos'

import { autoPositionForOrder, edgeTargetsForStep, stepOrders, stepsToEdges, stepsToNodes, usedOnStatusesForStep } from '../workflowGraphModel'

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

describe('autoPositionForOrder', () => {
  it('lays steps out left to right by order, matching the engine\'s own default advance-in-order semantics', () => {
    const p1 = autoPositionForOrder(1)
    const p2 = autoPositionForOrder(2)
    const p3 = autoPositionForOrder(3)
    expect(p1.x).toBeLessThan(p2.x)
    expect(p2.x).toBeLessThan(p3.x)
    expect(p1.y).toBe(p2.y)
    expect(p2.y).toBe(p3.y)
  })
})

describe('stepsToNodes', () => {
  it('uses a step\'s own canvasPosition when present', () => {
    const step = baseStep({ canvasPosition: { x: 500, y: 250 } })
    const [node] = stepsToNodes([step], null, false)
    expect(node.position).toEqual({ x: 500, y: 250 })
  })

  it('falls back to autoPositionForOrder when canvasPosition is null (never saved) -- WITHOUT writing anything back onto the step', () => {
    const step = baseStep({ order: 2, canvasPosition: null })
    const [node] = stepsToNodes([step], null, false)
    expect(node.position).toEqual(autoPositionForOrder(2))
    expect(step.canvasPosition).toBeNull()
  })

  it('marks the node selected/draggable/readOnly from the caller\'s own flags', () => {
    const step = baseStep({ id: 'sX' })
    const [selected] = stepsToNodes([step], 'sX', false)
    expect(selected.data.selected).toBe(true)
    expect(selected.draggable).toBe(true)

    const [readOnly] = stepsToNodes([step], null, true)
    expect(readOnly.data.selected).toBe(false)
    expect(readOnly.data.readOnly).toBe(true)
    expect(readOnly.draggable).toBe(false)
  })
})

describe('stepsToEdges -- the Member.identities/WorkflowStepDefinition.edges DTO trap', () => {
  it('never throws when a step\'s own edges field is null (a Go nil slice marshaled as `null`, despite the schema requiring a non-nullable array)', () => {
    // Cast through unknown: the generated TS type says `edges: WorkflowEdge[]`
    // (never null), but the wire trap this guards against is precisely a
    // server response that violates its own schema -- see this module's own
    // top comment.
    const step = { ...baseStep(), edges: null } as unknown as WorkflowStepDefinition
    expect(() => stepsToEdges([step])).not.toThrow()
    expect(stepsToEdges([step])).toEqual([])
  })

  it('renders one edge per (fromStep, onStatus, toStep), colored by onStatus', () => {
    const step = baseStep({ id: 's1', edges: [{ fromStepId: 's1', onStatus: 'ok', toStepId: 's2' }] })
    const [edge] = stepsToEdges([step])
    expect(edge.id).toBe('s1:ok:s2')
    expect(edge.source).toBe('s1')
    expect(edge.target).toBe('s2')
    expect(edge.label).toBe('ok')
  })
})

describe('stepOrders', () => {
  it('extracts every step\'s own order value, in array order', () => {
    expect(stepOrders([baseStep({ order: 3 }), baseStep({ order: 1 })])).toEqual([3, 1])
  })
})

describe('edgeTargetsForStep', () => {
  it('returns every step in this definition, sorted by order -- including the step itself (a same-step retry loop is a legal target)', () => {
    const steps = [baseStep({ id: 'a', order: 2 }), baseStep({ id: 'b', order: 1 })]
    const targets = edgeTargetsForStep(steps)
    expect(targets.map((t) => t.id)).toEqual(['b', 'a'])
  })
})

describe('usedOnStatusesForStep', () => {
  it('is empty for a step with no explicit edges', () => {
    expect(usedOnStatusesForStep(baseStep())).toEqual(new Set())
  })

  it('collects every onStatus this step already has an outgoing edge for', () => {
    const step = baseStep({
      edges: [
        { fromStepId: 's1', onStatus: 'ok', toStepId: 's2' },
        { fromStepId: 's1', onStatus: 'blocked', toStepId: 's3' },
      ],
    })
    expect(usedOnStatusesForStep(step)).toEqual(new Set(['ok', 'blocked']))
  })

  it('never throws when edges is null (the same DTO trap stepsToEdges guards against)', () => {
    const step = { ...baseStep(), edges: null } as unknown as WorkflowStepDefinition
    expect(() => usedOnStatusesForStep(step)).not.toThrow()
    expect(usedOnStatusesForStep(step)).toEqual(new Set())
  })
})
