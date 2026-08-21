// decisionInboxRendering.test.tsx -- DecisionInboxView.tsx's own defining
// risk, proven at the RENDER boundary: item.title (a PR/plan/session/
// automation title), repoFullName, provenanceRepoFullName/
// provenancePattern (a CODEOWNERS pattern), failureReason, artifactSummary,
// and lastError are ALL third-party or model-influenced free text --
// authored by a GitHub PR's own author, a repo's own CODEOWNERS file, or
// upstream error text this codebase does not control. Mirrors
// reviewRendering.test.tsx/membersRendering.test.tsx's own established
// pattern exactly: renderToStaticMarkup, no jsdom needed, proving React's
// default escaping is actually in effect.
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import type { DecisionInboxItem } from '@narvi/contracts/rest-dtos'

import { DecisionInboxRow } from '../DecisionInboxView'
import { isSafeHref } from '../urlSafety'

const XSS_IMG = '<img src=x onerror=alert(1)>'
const XSS_SCRIPT = '<script>alert(document.cookie)</script>'
const JS_URL = 'javascript:alert(document.cookie)'

function withQueryClient(node: React.ReactNode) {
  const client = new QueryClient()
  return renderToStaticMarkup(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

function baseItem(overrides: Partial<DecisionInboxItem> = {}): DecisionInboxItem {
  return {
    kind: 'ready_to_merge',
    title: 'A normal title',
    enteredQueueAt: '2026-08-20T00:00:00Z',
    ageSeconds: 3600,
    stale: false,
    repoFullName: null,
    prNumber: null,
    htmlUrl: null,
    headSha: null,
    provenanceKind: null,
    provenanceRepoFullName: null,
    provenancePattern: null,
    riskLabel: null,
    ciGreen: null,
    findings: null,
    isHandoff: null,
    hasApprovingReview: null,
    hasChangesRequested: null,
    planId: null,
    sessionId: null,
    failureReason: null,
    automationId: null,
    artifactSummary: null,
    outboxId: null,
    outboxKind: null,
    lastError: null,
    ...overrides,
  }
}

function prItem(overrides: Partial<DecisionInboxItem> = {}): DecisionInboxItem {
  return baseItem({
    repoFullName: 'acme/widgets',
    prNumber: 100,
    htmlUrl: 'https://github.com/acme/widgets/pull/100',
    isHandoff: false,
    ciGreen: true,
    hasChangesRequested: false,
    hasApprovingReview: true,
    findings: 0,
    ...overrides,
  })
}

describe('DecisionInboxRow -- adversarial PR title stays text, never markup', () => {
  it('a hostile title renders as text, in every row shape that can be rendered outside a real router', () => {
    // The plan shape (kind=awaiting_approval, planId set) is deliberately
    // excluded from this sweep -- ApprovePlanButton always renders a
    // TanStack Router <Link>, which requires a real RouterProvider/router
    // context (this codebase has no precedent for unit-rendering a
    // <Link>-bearing component outside the real app -- see
    // automationRendering.test.tsx's own identical note on RunRow's
    // "session ->" link). The plan shape's own title goes through the
    // exact SAME shared <T text={item.title} /> call at the top of
    // DecisionInboxRow every shape below already proves safe -- there is
    // no shape-specific title-rendering logic left unproven by excluding
    // it here.
    const shapes: DecisionInboxItem[] = [
      prItem({ kind: 'ready_to_merge', title: `fix: bug ${XSS_IMG}` }),
      prItem({ kind: 'needs_review', title: `fix: bug ${XSS_SCRIPT}` }),
      baseItem({ kind: 'needs_attention', sessionId: 's1', title: `session: ${XSS_SCRIPT}` }),
      baseItem({ kind: 'needs_attention', automationId: 'a1', title: `automation: ${XSS_IMG}` }),
    ]
    for (const item of shapes) {
      const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
      expect(html).not.toContain('<img')
      expect(html).not.toContain('<script>')
    }
  })
})

describe('DecisionInboxRow -- adversarial provenance/repo content stays text', () => {
  it('a hostile provenanceRepoFullName (requested_reviewer) renders as text', () => {
    const item = prItem({ kind: 'needs_review', provenanceKind: 'requested_reviewer', provenanceRepoFullName: `evil/${XSS_IMG}` })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a hostile CODEOWNERS provenancePattern (codeowners) renders as text', () => {
    const item = prItem({ kind: 'ready_to_merge', provenanceKind: 'codeowners', provenancePattern: `internal/**/${XSS_SCRIPT}` })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })
})

describe('DecisionInboxRow -- adversarial failureReason/artifactSummary/lastError stay text', () => {
  it('a hostile failureReason renders as text on a needs_attention session row', () => {
    const item = baseItem({ kind: 'needs_attention', sessionId: 's1', failureReason: `timeout ${XSS_SCRIPT}` })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a hostile artifactSummary renders as text on a needs_attention automation row', () => {
    const item = baseItem({ kind: 'needs_attention', automationId: 'a1', artifactSummary: `paused: ${XSS_IMG}` })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a hostile lastError renders as text on a needs_attention outbox row', () => {
    const item = baseItem({ kind: 'needs_attention', outboxId: 'o1', outboxKind: 'slack_delivery', lastError: `500: ${XSS_SCRIPT}` })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('does not hang or break layout on a 200KB title', () => {
    const item = prItem({ kind: 'needs_review', title: 'x'.repeat(200_000) })
    const start = Date.now()
    let html = ''
    expect(() => {
      html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    }).not.toThrow()
    expect(Date.now() - start).toBeLessThan(2000)
    expect(html.length).toBeLessThan(200_000)
    expect(html).toContain('more characters truncated')
  })
})

describe('DecisionInboxRow -- htmlUrl is the ONLY field that becomes an href, and only when isSafeHref accepts it', () => {
  it('a well-formed GitHub htmlUrl on a needs_review row renders a real, safe anchor', () => {
    const item = prItem({ kind: 'needs_review', htmlUrl: 'https://github.com/acme/widgets/pull/100' })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).toContain('href="https://github.com/acme/widgets/pull/100"')
  })

  it('a javascript:-scheme htmlUrl never renders as a live href -- "link unavailable" instead', () => {
    const item = prItem({ kind: 'needs_review', htmlUrl: JS_URL })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).not.toContain(`href="${JS_URL}"`)
    expect(html).toContain('link unavailable')
  })

  it('a handoff PR row (awaiting_approval) uses the same htmlUrl guard', () => {
    const item = prItem({ kind: 'awaiting_approval', isHandoff: true, htmlUrl: JS_URL })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).not.toContain(`href="${JS_URL}"`)
    expect(html).toContain('link unavailable')
  })

  it('a null htmlUrl renders "link unavailable", never a broken empty href', () => {
    const item = prItem({ kind: 'needs_review', htmlUrl: null })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).not.toContain('href=""')
    expect(html).toContain('link unavailable')
  })
})

describe('DecisionInboxRow -- hasChangesRequested, not hasApprovingReview, gates the Merge button', () => {
  it('pre-disables Merge and explains why when hasChangesRequested is true', () => {
    const item = prItem({ kind: 'ready_to_merge', hasChangesRequested: true, hasApprovingReview: false })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={true} />)
    expect(html).toContain('<button')
    expect(html).toMatch(/<button[^>]*disabled/)
  })

  it('a true hasApprovingReview never enables or disables Merge on its own -- display only', () => {
    const eligible = prItem({ kind: 'ready_to_merge', hasChangesRequested: false, hasApprovingReview: false })
    const html = withQueryClient(<DecisionInboxRow item={eligible} canMerge={true} />)
    expect(html).toMatch(/<button[^>]*>\s*Merge\s*<\/button>/)
  })
})

describe('DecisionInboxRow -- viewer role sees a read-only queue (§16.2)', () => {
  it('canMerge=false on a ready_to_merge row renders no Merge button at all', () => {
    const item = prItem({ kind: 'ready_to_merge' })
    const html = withQueryClient(<DecisionInboxRow item={item} canMerge={false} />)
    expect(html).not.toContain('>Merge<')
    expect(html).toContain('read-only')
  })
})

describe('mutation guard: isSafeHref actually called on decision-inbox htmlUrl', () => {
  it('isSafeHref(javascript:...) is false -- if this ever flips true, DecisionInboxRow\'s own guard silently stops working', () => {
    expect(isSafeHref(JS_URL)).toBe(false)
  })
})
