// workflowRunRendering.test.tsx -- WorkflowRunsView.tsx's own defining
// risk, proven at the render boundary: outcomeSummary (an agent's own
// advisory free text), modelId (an opaque, Narvi-unvalidated
// provider/model passthrough string) and decidedBy are all attacker-
// reachable fields that must render as plain text only, never markup.
// Mirrors planRendering.test.tsx/workflowRendering.test.tsx's own
// established pattern exactly: renderToStaticMarkup, no jsdom needed,
// proving React's default escaping is actually in effect. Also covers
// this Step's own defining cost-nullability risk (§25.15): a null costUsd
// must render as an honest "not yet reported" marker, never a fabricated
// "$0.00".
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import type { WorkflowStepRun } from '@narvi/contracts/rest-dtos'

import { EdgeConnector, StepRunCard } from '../WorkflowRunsView'
import type { StepRunAttempt } from '../workflowRunFormat'

const XSS_IMG = '<img src=x onerror=alert(1)>'
const XSS_SCRIPT = '<script>alert(document.cookie)</script>'

// Shaped exactly like what the server actually sends
// (workflowStepRunToDTO) -- null model/cost/outcome/decision by default,
// never an invented shape the wire never produces.
function baseStepRun(overrides: Partial<WorkflowStepRun> = {}): WorkflowStepRun {
  return {
    id: 'sr1',
    workflowRunId: 'run1',
    stepDefinitionId: 'step-a',
    turnId: null,
    status: 'running',
    outcomeStatus: null,
    outcomeSummary: null,
    decision: null,
    decidedAt: null,
    decidedBy: null,
    createdAt: '2026-08-20T15:06:00Z',
    finishedAt: null,
    modelId: null,
    costUsd: null,
    ...overrides,
  }
}

function attempt(overrides: Partial<WorkflowStepRun> = {}, stepIndex = 1, attemptNumber = 1): StepRunAttempt {
  return { stepRun: baseStepRun(overrides), stepIndex, attemptNumber }
}

describe('StepRunCard -- adversarial step-run content stays text, never markup', () => {
  it('a hostile outcomeSummary renders as text', () => {
    const html = renderToStaticMarkup(<StepRunCard attempt={attempt({ outcomeStatus: 'ok', outcomeSummary: `looks good\n${XSS_SCRIPT}` })} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a hostile modelId renders as text', () => {
    const html = renderToStaticMarkup(<StepRunCard attempt={attempt({ modelId: `anthropic/${XSS_IMG}` })} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a hostile decidedBy renders as text', () => {
    const html = renderToStaticMarkup(<StepRunCard attempt={attempt({ decision: 'approve', decidedAt: '2026-08-20T15:10:00Z', decidedBy: XSS_IMG })} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('does not hang or break layout on a 50KB outcome summary', () => {
    const start = Date.now()
    let html = ''
    expect(() => {
      html = renderToStaticMarkup(<StepRunCard attempt={attempt({ outcomeStatus: 'ok', outcomeSummary: 'x'.repeat(50_000) })} />)
    }).not.toThrow()
    expect(Date.now() - start).toBeLessThan(2000)
    expect(html.length).toBeLessThan(50_000)
  })

  describe('cost nullability -- the whole subject of the previous Step, and easy to silently undo here', () => {
    it('a null costUsd renders the honest "not yet reported" marker, never $0.00', () => {
      const html = renderToStaticMarkup(<StepRunCard attempt={attempt({ costUsd: null })} />)
      expect(html).not.toContain('$0.00')
      expect(html).toContain('—')
    })

    it('a genuine 0 costUsd renders as $0.00 -- a real, distinct value from "unknown yet"', () => {
      const html = renderToStaticMarkup(<StepRunCard attempt={attempt({ costUsd: 0 })} />)
      expect(html).toContain('$0.00')
    })

    it('a normal costUsd renders to 2 decimal places', () => {
      const html = renderToStaticMarkup(<StepRunCard attempt={attempt({ costUsd: 1.5 })} />)
      expect(html).toContain('$1.50')
    })
  })

  it('a null modelId renders the honest "default" label, never a blank or fabricated model name', () => {
    const html = renderToStaticMarkup(<StepRunCard attempt={attempt({ modelId: null })} />)
    expect(html).toContain('default')
  })

  it('shows the attempt number only for a retry (attemptNumber > 1), not on a step\'s first attempt', () => {
    const first = renderToStaticMarkup(<StepRunCard attempt={attempt({}, 1, 1)} />)
    expect(first).not.toContain('attempt')
    const retry = renderToStaticMarkup(<StepRunCard attempt={attempt({}, 1, 2)} />)
    expect(retry).toContain('attempt 2')
  })
})

describe('EdgeConnector -- inputs are closed-enum/numeric only, still proven safe', () => {
  it('renders an advance edge label', () => {
    const html = renderToStaticMarkup(<EdgeConnector edge={{ kind: 'advance', onStatus: 'ok' }} toStepIndex={2} />)
    expect(html).toContain('step 2')
  })

  it('renders a revise edge label distinctly from a plain retry', () => {
    const revise = renderToStaticMarkup(<EdgeConnector edge={{ kind: 'revise', onStatus: 'ok' }} toStepIndex={1} />)
    const retry = renderToStaticMarkup(<EdgeConnector edge={{ kind: 'retry', onStatus: 'needs_fix' }} toStepIndex={1} />)
    expect(revise).toContain('revision requested')
    expect(retry).not.toContain('revision requested')
  })
})
