import { describe, expect, it } from 'vitest'

import type { WorkflowRun, WorkflowStepRun } from '@narvi/contracts/rest-dtos'

import { canActOnPlan } from '../planFormat'
import {
  buildStepRunSequence,
  canActOnWorkflowStep,
  decidableStepRun,
  decisionLabel,
  edgeLabel,
  edgeToNext,
  featuredRun,
  formatStepCost,
  NEEDS_REVIEW_EXPLANATION,
  outcomeStatusLabel,
  outcomeStatusTone,
  runStatusLabel,
  runStatusTone,
  stepRunStatusLabel,
  stepRunStatusTone,
  totalKnownCost,
} from '../workflowRunFormat'

// Fixtures deliberately shaped exactly like what the server actually sends
// (workflowStepRunToDTO/workflowRunToDTO, internal/adapters/inbound/httpapi/
// workflowruns.go) -- including a NULL modelId/costUsd/outcomeStatus/
// decision by default, never an invented shape the wire never produces
// (this Step's own explicit fixture-fidelity requirement).
function baseRun(overrides: Partial<WorkflowRun> = {}): WorkflowRun {
  return {
    id: 'run-1',
    sessionId: 'sess-1',
    lane: 'request',
    workflowDefinitionId: 'def-1',
    definitionVersion: 1,
    status: 'running',
    createdAt: '2026-08-20T00:00:00Z',
    updatedAt: '2026-08-20T00:00:00Z',
    finishedAt: null,
    ...overrides,
  }
}

function baseStepRun(overrides: Partial<WorkflowStepRun> = {}): WorkflowStepRun {
  return {
    id: 'sr-1',
    workflowRunId: 'run-1',
    stepDefinitionId: 'step-a',
    turnId: null,
    status: 'running',
    outcomeStatus: null,
    outcomeSummary: null,
    decision: null,
    decidedAt: null,
    decidedBy: null,
    createdAt: '2026-08-20T00:00:00Z',
    finishedAt: null,
    modelId: null,
    costUsd: null,
    ...overrides,
  }
}

describe('runStatusTone / runStatusLabel', () => {
  it('maps every workflow_run_status value to a tone and a label', () => {
    expect(runStatusTone('running')).toBe('run')
    expect(runStatusTone('completed')).toBe('ok')
    expect(runStatusTone('needs_review')).toBe('warn')
    expect(runStatusTone('failed')).toBe('crit')
    expect(runStatusTone('cancelled')).toBe('neutral')

    expect(runStatusLabel('running')).toBe('running')
    expect(runStatusLabel('completed')).toBe('completed')
    expect(runStatusLabel('needs_review')).toBe('needs review')
    expect(runStatusLabel('failed')).toBe('failed')
    expect(runStatusLabel('cancelled')).toBe('cancelled')
  })
})

describe('stepRunStatusTone / stepRunStatusLabel', () => {
  it('maps every workflow_step_run_status value to a tone and a label', () => {
    expect(stepRunStatusTone('awaiting_decision')).toBe('warn')
    expect(stepRunStatusTone('running')).toBe('run')
    expect(stepRunStatusTone('completed')).toBe('ok')
    expect(stepRunStatusTone('failed')).toBe('crit')
    expect(stepRunStatusTone('cancelled')).toBe('neutral')

    expect(stepRunStatusLabel('awaiting_decision')).toBe('awaiting decision')
  })
})

describe('outcomeStatusTone / outcomeStatusLabel', () => {
  it('maps the 3-value outcome enum, reusing workflowFormat.ts own edge tone vocabulary', () => {
    expect(outcomeStatusTone('ok')).toBe('ok')
    expect(outcomeStatusTone('needs_fix')).toBe('warn')
    expect(outcomeStatusTone('blocked')).toBe('crit')

    expect(outcomeStatusLabel('ok')).toBe('ok')
    expect(outcomeStatusLabel('needs_fix')).toBe('needs fix')
    expect(outcomeStatusLabel('blocked')).toBe('blocked')
  })
})

describe('decisionLabel', () => {
  it('maps the 3-value decision enum to operator language', () => {
    expect(decisionLabel('approve')).toBe('approved')
    expect(decisionLabel('reject')).toBe('rejected')
    expect(decisionLabel('revise')).toBe('revision requested')
  })
})

describe('formatStepCost -- null must never render as a fabricated $0.00', () => {
  it('renders null as an em dash, never $0.00', () => {
    expect(formatStepCost(null)).toBe('—')
  })
  it('renders a genuine 0 as $0.00 -- a real, distinct value from "unknown yet"', () => {
    expect(formatStepCost(0)).toBe('$0.00')
  })
  it('renders a normal value to 2 decimal places', () => {
    expect(formatStepCost(1.5)).toBe('$1.50')
    expect(formatStepCost(12)).toBe('$12.00')
  })
})

describe('totalKnownCost', () => {
  it('returns null (never a fabricated 0) when no attempt has reported a cost yet', () => {
    expect(totalKnownCost([])).toBeNull()
    expect(totalKnownCost([baseStepRun({ costUsd: null }), baseStepRun({ costUsd: null })])).toBeNull()
  })
  it('sums only the non-null attempts, treating a still-null one as "not yet known" rather than 0', () => {
    const total = totalKnownCost([baseStepRun({ costUsd: 1.5 }), baseStepRun({ costUsd: null }), baseStepRun({ costUsd: 2.25 })])
    expect(total).toBe(3.75)
  })
  it('a genuine 0 attempt still counts as "known" (sawAny), distinguishing it from an all-null run', () => {
    expect(totalKnownCost([baseStepRun({ costUsd: 0 })])).toBe(0)
  })
})

describe('featuredRun', () => {
  it('returns null for an empty list', () => {
    expect(featuredRun([])).toBeNull()
  })
  it('prefers a running run over a completed one', () => {
    const runs = [baseRun({ id: 'r1', status: 'completed' }), baseRun({ id: 'r2', status: 'running' })]
    expect(featuredRun(runs)?.id).toBe('r2')
  })
  it('prefers a needs_review run over a completed one -- it is still waiting on a human', () => {
    const runs = [baseRun({ id: 'r1', status: 'completed' }), baseRun({ id: 'r2', status: 'needs_review' })]
    expect(featuredRun(runs)?.id).toBe('r2')
  })
  it('falls back to the FIRST entry (server-sorted newest-first) when nothing is active, without re-sorting', () => {
    const runs = [baseRun({ id: 'r1', status: 'completed', createdAt: '2026-08-20T02:00:00Z' }), baseRun({ id: 'r2', status: 'failed', createdAt: '2026-08-20T01:00:00Z' })]
    expect(featuredRun(runs)?.id).toBe('r1')
  })
})

describe('buildStepRunSequence', () => {
  it('assigns stepIndex 1/attemptNumber 1 to a run\'s single attempt', () => {
    const stepRun = baseStepRun({ id: 'sr1', stepDefinitionId: 'a' })
    const seq = buildStepRunSequence([stepRun])
    expect(seq).toEqual([{ stepRun, stepIndex: 1, attemptNumber: 1 }])
  })

  it('assigns increasing stepIndex to distinct steps encountered in order, each at attempt 1', () => {
    const seq = buildStepRunSequence([baseStepRun({ id: 'sr1', stepDefinitionId: 'a' }), baseStepRun({ id: 'sr2', stepDefinitionId: 'b' })])
    expect(seq.map((s) => [s.stepIndex, s.attemptNumber])).toEqual([
      [1, 1],
      [2, 1],
    ])
  })

  it('an immediate same-step retry increments attemptNumber, keeping the SAME stepIndex', () => {
    const seq = buildStepRunSequence([baseStepRun({ id: 'sr1', stepDefinitionId: 'a' }), baseStepRun({ id: 'sr2', stepDefinitionId: 'a' })])
    expect(seq.map((s) => [s.stepIndex, s.attemptNumber])).toEqual([
      [1, 1],
      [1, 2],
    ])
  })

  it('a loop-back retry (a -> b -> a again) keeps step a\'s ORIGINAL stepIndex rather than assigning it a new one, and counts its second attempt correctly', () => {
    const seq = buildStepRunSequence([baseStepRun({ id: 'sr1', stepDefinitionId: 'a' }), baseStepRun({ id: 'sr2', stepDefinitionId: 'b' }), baseStepRun({ id: 'sr3', stepDefinitionId: 'a' })])
    expect(seq.map((s) => [s.stepIndex, s.attemptNumber])).toEqual([
      [1, 1],
      [2, 1],
      [1, 2],
    ])
  })
})

describe('edgeToNext', () => {
  it('is "retry" when the next attempt targets the SAME step and there was no revise decision', () => {
    const from = baseStepRun({ stepDefinitionId: 'a', outcomeStatus: 'needs_fix' })
    const to = baseStepRun({ stepDefinitionId: 'a' })
    expect(edgeToNext(from, to)).toEqual({ kind: 'retry', onStatus: 'needs_fix' })
  })

  it('is "advance" when the next attempt targets a DIFFERENT step', () => {
    const from = baseStepRun({ stepDefinitionId: 'a', outcomeStatus: 'ok' })
    const to = baseStepRun({ stepDefinitionId: 'b' })
    expect(edgeToNext(from, to)).toEqual({ kind: 'advance', onStatus: 'ok' })
  })

  it('is "revise" whenever `from.decision` is revise, EVEN THOUGH the target is the same step -- decision must win over the same-step-id check, not just happen to agree with it', () => {
    const from = baseStepRun({ stepDefinitionId: 'a', outcomeStatus: 'ok', decision: 'revise' })
    const to = baseStepRun({ stepDefinitionId: 'a' })
    expect(edgeToNext(from, to)).toEqual({ kind: 'revise', onStatus: 'ok' })
  })

  it('preserves a null onStatus rather than fabricating one', () => {
    const from = baseStepRun({ stepDefinitionId: 'a', outcomeStatus: null })
    const to = baseStepRun({ stepDefinitionId: 'b' })
    expect(edgeToNext(from, to).onStatus).toBeNull()
  })
})

describe('edgeLabel', () => {
  it('renders each edge kind distinctly, naming the real destination step position', () => {
    expect(edgeLabel({ kind: 'advance', onStatus: 'ok' }, 2)).toBe('ok → step 2')
    expect(edgeLabel({ kind: 'retry', onStatus: 'needs_fix' }, 1)).toBe('needs fix → retrying step 1')
    expect(edgeLabel({ kind: 'revise', onStatus: 'ok' }, 1)).toBe('revision requested → re-running step 1')
  })
  it('never fabricates an outcome word when onStatus is null', () => {
    expect(edgeLabel({ kind: 'advance', onStatus: null }, 2)).toBe('no outcome reported → step 2')
  })
})

describe('decidableStepRun', () => {
  it('returns null when no attempt is awaiting_decision', () => {
    expect(decidableStepRun([baseStepRun({ status: 'completed' }), baseStepRun({ status: 'running' })])).toBeNull()
  })
  it('finds the one attempt actually parked awaiting_decision', () => {
    const target = baseStepRun({ id: 'sr2', status: 'awaiting_decision' })
    expect(decidableStepRun([baseStepRun({ id: 'sr1', status: 'completed' }), target])).toBe(target)
  })
})

describe('NEEDS_REVIEW_EXPLANATION', () => {
  it('is operator-facing copy: no § section citation, since an operator has no access to the technical plan', () => {
    expect(NEEDS_REVIEW_EXPLANATION).not.toMatch(/§/)
  })
  it('does not imply a retry action exists here', () => {
    expect(NEEDS_REVIEW_EXPLANATION.toLowerCase()).toContain('no retry')
  })
})

describe('canActOnWorkflowStep', () => {
  it('is the SAME function as canActOnPlan -- §25.11 states ActionDecideWorkflowStep is the identical matrix row as ActionApprovePlan, so this must be a reuse, never an independently-maintained copy that could silently drift', () => {
    expect(canActOnWorkflowStep).toBe(canActOnPlan)
  })
})
