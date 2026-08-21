// planRendering.test.tsx -- PlanModeView.tsx's own defining risk, proven
// at the render boundary: plan.content is model-authored, freeform prose
// (Plan's own schema doc comment: "no structured plan schema anywhere in
// this codebase... render as plain text only, never markdown-parsed").
// Mirrors reviewRendering.test.tsx's own established pattern exactly:
// renderToStaticMarkup, no jsdom needed, proving React's default escaping
// is actually in effect.
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import type { Plan } from '@narvi/contracts/rest-dtos'

import { PlanCard } from '../PlanModeView'
import { latestPlan } from '../planFormat'

const XSS_IMG = '<img src=x onerror=alert(1)>'
const XSS_SCRIPT = '<script>alert(document.cookie)</script>'
const JS_URL = 'javascript:alert(document.cookie)'

function basePlan(overrides: Partial<Plan> = {}): Plan {
  return {
    id: 'p1',
    sessionId: 's1',
    version: 1,
    status: 'awaiting_approval',
    planModelId: 'anthropic/claude-opus-4-8',
    createdAt: '2026-08-20T15:06:00Z',
    decidedAt: null,
    decidedBy: null,
    content: 'A normal plan.',
    ...overrides,
  }
}

describe('PlanCard rendering -- adversarial plan content stays text, never markup', () => {
  it('a plan.content containing a script tag renders as text', () => {
    const html = renderToStaticMarkup(<PlanCard plan={basePlan({ content: `1. Add a table.\n${XSS_SCRIPT}` })} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a plan.content containing an <img onerror=...> renders as text', () => {
    const html = renderToStaticMarkup(<PlanCard plan={basePlan({ content: XSS_IMG })} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a plan.content that is literally structured like a numbered plan (the mockup\'s own .planlist shape) still renders as plain preformatted text, never parsed into <ol>/<li> markup -- this view deliberately adds no markdown renderer (see PlanModeView.tsx\'s own top doc comment)', () => {
    const html = renderToStaticMarkup(<PlanCard plan={basePlan({ content: '1. **Add automation_secrets table**\n   New migration.\n2. **Extend SESSION_CONFIG assembly**' })} />)
    expect(html).not.toContain('<ol')
    expect(html).not.toContain('<li')
    // The literal markdown-looking asterisks pass through verbatim as text
    // (never interpreted as bold) -- proof no markdown parser is involved.
    expect(html).toContain('**Add automation_secrets table**')
  })

  it('a plan.content containing a javascript: URL as plain text never becomes a clickable link -- this view builds no href from plan content at all', () => {
    const html = renderToStaticMarkup(<PlanCard plan={basePlan({ content: `Click here: ${JS_URL}` })} />)
    expect(html).not.toContain('<a ')
    expect(html).not.toContain(`href="${JS_URL}"`)
    // The scheme string itself is still present, but only as inert text content.
    expect(html).toContain(JS_URL)
  })

  it('a hostile plan_model_id (attacker-controlled only in the sense that it is a free-text catalog id, never Narvi-validated against an allowlist per §29.8) still renders as text in the header context, proven via latestPlan + PlanCard together', () => {
    const plans = [basePlan({ id: 'p1', version: 1, status: 'superseded' }), basePlan({ id: 'p2', version: 2, status: 'awaiting_approval', content: XSS_SCRIPT })]
    const featured = latestPlan(plans)
    expect(featured?.id).toBe('p2')
    const html = renderToStaticMarkup(<PlanCard plan={featured!} />)
    expect(html).not.toContain('<script>')
  })
})

describe('latestPlan -- pure selection logic', () => {
  it('picks the awaiting_approval version even when it is not the highest-numbered one listed (defensive -- in practice it always is, per the DB\'s own partial unique index)', () => {
    const plans = [basePlan({ id: 'p1', version: 1, status: 'superseded' }), basePlan({ id: 'p2', version: 2, status: 'awaiting_approval' })]
    expect(latestPlan(plans)?.id).toBe('p2')
  })
  it('falls back to the highest version when nothing is awaiting approval', () => {
    const plans = [basePlan({ id: 'p1', version: 1, status: 'rejected' }), basePlan({ id: 'p2', version: 2, status: 'superseded' }), basePlan({ id: 'p3', version: 3, status: 'approved' })]
    expect(latestPlan(plans)?.id).toBe('p3')
  })
  it('returns null for an empty list', () => {
    expect(latestPlan([])).toBeNull()
  })
})
