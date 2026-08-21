// environmentsRendering.test.tsx -- EnvironmentsPanel.tsx's own defining
// risk: environments has no name column at all (that file's own top doc
// comment), so pathScope glob patterns and contractsPath are the two
// free-ish-text fields it carries. Both are server-syntax-validated
// (internal/domain/environment.ValidatePathScope) but never
// HTML-safety-validated, and both come from repo-relative config a
// caller supplies -- adversarial content, same discipline as every other
// third-party-authored field in this Step. Mirrors
// reviewRendering.test.tsx's own established pattern exactly.
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import type { Environment } from '@narvi/contracts/rest-dtos'

import { EnvironmentCardHeader } from '../EnvironmentsPanel'

const XSS_SCRIPT = '<script>alert(document.cookie)</script>'
const XSS_IMG = '<img src=x onerror=alert(1)>'

function baseEnvironment(overrides: Partial<Environment> = {}): Environment {
  return {
    id: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',
    pathScope: ['web/**'],
    mockConfigured: false,
    contractsPath: null,
    dockerRequired: false,
    egressPolicyMode: null,
    egressPolicyAllowlist: null,
    createdAt: '2026-08-20T02:00:00Z',
    ...overrides,
  }
}

describe('EnvironmentCardHeader rendering -- adversarial contractsPath/pathScope stays text, never markup', () => {
  it('a hostile contractsPath renders as text', () => {
    const html = renderToStaticMarkup(<EnvironmentCardHeader env={baseEnvironment({ contractsPath: XSS_SCRIPT })} expanded={false} onToggle={() => {}} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a hostile pathScope pattern renders as text', () => {
    const html = renderToStaticMarkup(<EnvironmentCardHeader env={baseEnvironment({ pathScope: [XSS_IMG] })} expanded={false} onToggle={() => {}} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a null contractsPath renders no contracts line at all (never "null" as text)', () => {
    const html = renderToStaticMarkup(<EnvironmentCardHeader env={baseEnvironment({ contractsPath: null })} expanded={false} onToggle={() => {}} />)
    expect(html).not.toContain('>null<')
  })
})
