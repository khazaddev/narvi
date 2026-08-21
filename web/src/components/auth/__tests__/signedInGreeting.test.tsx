// Proves SignedInGreeting renders a hostile, third-party-controlled
// display name (a GitHub profile's own free-text `name` field) as TEXT,
// never as markup -- "a third-party display name containing markup
// renders as text" -- §13.1's own requirement. Uses
// react-dom/server's renderToStaticMarkup (a real render through React's
// own escaping, not a hand-rolled string check) -- no jsdom, no
// @testing-library/react needed for a static-markup proof.
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import type { Member } from '@narvi/contracts/rest-dtos'

import { SignedInGreeting } from '../SignedInGreeting'

function makeMember(overrides: Partial<Member>): Member {
  return {
    id: 'user-1',
    email: 'octocat@example.com',
    displayName: 'Octocat',
    role: 'member',
    disabled: false,
    createdAt: '2026-01-01T00:00:00Z',
    identities: [],
    ...overrides,
  }
}

describe('SignedInGreeting', () => {
  it('renders a plain display name as-is', () => {
    const html = renderToStaticMarkup(<SignedInGreeting member={makeMember({ displayName: 'Octocat' })} />)
    expect(html).toContain('Octocat')
  })

  it('renders a hostile display name (an <img>-tag-shaped GitHub profile name) as escaped text, never as markup', () => {
    const hostileName = '<img src=x onerror=alert(1)>'
    const html = renderToStaticMarkup(<SignedInGreeting member={makeMember({ displayName: hostileName })} />)

    // The literal tag must NEVER appear unescaped in the output -- if it
    // did, a browser parsing this HTML would actually create a real
    // <img> element (with its onerror handler live) instead of showing
    // the string as visible text. "onerror=" as plain escaped TEXT is
    // fine -- the structural check is that no real "<img" TAG exists.
    expect(html).not.toContain('<img ')
    // React's own escaping renders it as HTML entities instead --
    // proving the string reached the DOM as TEXT, not as an injected tag.
    expect(html).toContain('&lt;img src=x onerror=alert(1)&gt;')
  })

  it('renders a hostile display name containing a script tag as escaped text', () => {
    const hostileName = '<script>alert(document.cookie)</script>'
    const html = renderToStaticMarkup(<SignedInGreeting member={makeMember({ displayName: hostileName })} />)

    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('renders a hostile email the same way', () => {
    const hostileEmail = '"><img src=x onerror=alert(1)>@example.com'
    const html = renderToStaticMarkup(<SignedInGreeting member={makeMember({ email: hostileEmail })} />)

    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })
})
