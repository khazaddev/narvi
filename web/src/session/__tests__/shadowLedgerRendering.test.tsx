// shadowLedgerRendering.test.tsx -- ShadowLedgerCard's own defining risk,
// proven at the RENDER boundary: category/operation/target are every one
// of them attacker/customer-influenceable text (a branch name, a file
// path, an HTTP path from the transport gate) reaching this screen from
// shadow_scm_writes/outbox rows this platform itself suppressed rather
// than delivered -- exactly the kind of content §30's own guarantee
// exists around, and this card is the first UI surface that ever
// displays it. Mirrors repoSettingsRendering.test.tsx/
// decisionInboxRendering.test.tsx's own established pattern:
// renderToStaticMarkup, no jsdom needed, proving React's default escaping
// holds on ShadowLedgerEntryRow -- the one piece of ShadowLedgerCard this
// file exports specifically for this test (see that component's own doc
// comment for why sessionId is left null here: a real one renders a
// TanStack Router <Link>, which needs a router context this codebase has
// no precedent for unit-rendering outside the real app).
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import type { ShadowLedgerEntry } from '@narvi/contracts/rest-dtos'

import { ShadowLedgerEntryRow } from '../RepoSettingsView'

const XSS_IMG = '<img src=x onerror=alert(1)>'
const XSS_SCRIPT = '<script>alert(document.cookie)</script>'

function baseEntry(overrides: Partial<ShadowLedgerEntry> = {}): ShadowLedgerEntry {
  return {
    source: 'scm_write',
    operation: 'create_pr',
    category: 'Pull requests',
    target: null,
    sessionId: null,
    createdAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function renderRow(entry: ShadowLedgerEntry): string {
  return renderToStaticMarkup(
    <table>
      <tbody>
        <ShadowLedgerEntryRow entry={entry} />
      </tbody>
    </table>,
  )
}

describe('ShadowLedgerEntryRow -- adversarial category stays text, never markup', () => {
  it('a hostile category renders as text', () => {
    const html = renderRow(baseEntry({ category: `Other suppressed writes ${XSS_IMG}` }))
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })
})

describe('ShadowLedgerEntryRow -- adversarial operation stays text', () => {
  it('a hostile operation string renders as text', () => {
    const html = renderRow(baseEntry({ operation: `http_post${XSS_SCRIPT}` }))
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })
})

describe('ShadowLedgerEntryRow -- adversarial target (a customer-chosen branch/path) stays text', () => {
  it('a hostile branch name renders as text', () => {
    const html = renderRow(baseEntry({ target: `feature/${XSS_IMG}` }))
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a hostile file path renders as text', () => {
    const html = renderRow(baseEntry({ operation: 'update_file_content', category: 'File content writes', target: `src/${XSS_SCRIPT}.go` }))
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a null target renders the placeholder, never throws', () => {
    const html = renderRow(baseEntry({ target: null }))
    expect(html).toContain('—')
  })
})

describe('ShadowLedgerEntryRow -- an unrecognized source value stays text, not thrown', () => {
  it('a future/unknown source value (bypassing the closed wire enum) still renders every text field safely', () => {
    // source is a closed enum today ("scm_write" | "outbox"), but nothing
    // in ShadowLedgerEntryRow branches display logic on it in a way that
    // could reach an unescaped path -- this pins that a version-skewed
    // server value does not change that.
    const entry = baseEntry({ source: 'future_source' as unknown as ShadowLedgerEntry['source'], category: `weird ${XSS_IMG}` })
    const html = renderRow(entry)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })
})
