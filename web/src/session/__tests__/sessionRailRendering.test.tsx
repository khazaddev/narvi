// sessionRailRendering.test.tsx -- row 83's own explicit verification
// requirement: "an artifact URL with a `javascript:` scheme is not
// rendered as a link; a filename containing markup renders as text."
// Uses react-dom/server's renderToStaticMarkup, the SAME no-jsdom pattern
// web/src/session/__tests__/timelineRendering.test.tsx already
// establishes for this exact class of proof -- ArtifactRow takes a
// ParsedArtifact as a plain prop (no I/O), so this needs no QueryClient or
// fetch mock, only the component itself.
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import type { ParsedArtifact } from '../artifactPayloads'
import { ArtifactRow } from '../SessionRail'

const XSS_PAYLOAD = '<img src=x onerror=alert(1)>'

function baseArtifact(overrides: Partial<ParsedArtifact>): ParsedArtifact {
  return {
    id: 'a1',
    type: 'upload',
    url: '/api/sessions/s1/uploads/a1/content',
    createdAt: '2026-08-20T10:00:00Z',
    status: 'ready',
    failureReason: null,
    filename: 'report.pdf',
    sizeBytes: 100,
    contentType: 'application/pdf',
    metadata: {},
    ...overrides,
  }
}

describe('ArtifactRow -- javascript: scheme is never rendered as a link', () => {
  // MUTATION TEST: replace this component's own `isSafeHref(artifact.url)`
  // gate with `true` (i.e. bypass urlSafety.ts entirely) and re-run --
  // both assertions below flip: the href appears, and the "link
  // unavailable" fallback text disappears.
  it('an upload artifact with a javascript: url renders no href at all', () => {
    const html = renderToStaticMarkup(<ArtifactRow artifact={baseArtifact({ url: 'javascript:alert(1)' })} />)
    expect(html).not.toContain('href="javascript:')
    expect(html).not.toContain('<a ')
    expect(html).toContain('link unavailable')
  })

  it('a pr artifact with a javascript: url renders no href at all', () => {
    const html = renderToStaticMarkup(<ArtifactRow artifact={baseArtifact({ type: 'pr', url: 'javascript:alert(document.cookie)', metadata: { repo: 'acme/narvi', number: 1 } })} />)
    expect(html).not.toContain('href="javascript:')
    expect(html).toContain('link unavailable')
  })

  it('a preview artifact with a data: url (another unsafe scheme) renders no href at all', () => {
    const html = renderToStaticMarkup(<ArtifactRow artifact={baseArtifact({ type: 'preview', url: 'data:text/html,<script>1</script>', metadata: {} })} />)
    expect(html).not.toContain('href="data:')
    expect(html).toContain('link unavailable')
  })

  it('a genuinely safe https url DOES render as a real link, proving the guard is not just always-false', () => {
    const html = renderToStaticMarkup(<ArtifactRow artifact={baseArtifact({ type: 'pr', url: 'https://github.example/acme/narvi/pull/12', metadata: { repo: 'acme/narvi', number: 12 } })} />)
    expect(html).toContain('href="https://github.example/acme/narvi/pull/12"')
  })

  it('an upload artifact\'s own stable relative content path DOES render as a real link', () => {
    const html = renderToStaticMarkup(<ArtifactRow artifact={baseArtifact({ url: '/api/sessions/s1/uploads/a1/content' })} />)
    expect(html).toContain('href="/api/sessions/s1/uploads/a1/content"')
  })
})

describe('ArtifactRow -- a filename containing markup renders as text, never HTML', () => {
  // MUTATION TEST: change this component's filename rendering from plain
  // JSX text interpolation to dangerouslySetInnerHTML and re-run -- the
  // "not.toContain('<img')" assertion fails immediately (a real <img> tag
  // appears in the static markup).
  it('a hostile filename never becomes a real <img> tag', () => {
    const html = renderToStaticMarkup(<ArtifactRow artifact={baseArtifact({ filename: XSS_PAYLOAD })} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a failed upload\'s failureReason is also escaped', () => {
    const html = renderToStaticMarkup(<ArtifactRow artifact={baseArtifact({ status: 'failed', failureReason: '<script>alert(1)</script>' })} />)
    expect(html).not.toContain('<script>alert')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a null filename renders the honest "(unnamed upload)" fallback rather than crashing or rendering nothing', () => {
    const html = renderToStaticMarkup(<ArtifactRow artifact={baseArtifact({ filename: null })} />)
    expect(html).toContain('(unnamed upload)')
  })
})
