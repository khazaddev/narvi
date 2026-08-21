// integrationsRendering.test.tsx -- IntegrationsPanel.tsx's own defining
// risk: Integration.lastOutboundError is genuinely free text (an
// upstream/internal error message, restdtos.Integration's own doc
// comment), and ChatGPTLinkStatus.userCode/verificationUrl are
// server-supplied strings this client did not originate. Mirrors
// membersRendering.test.tsx/environmentsRendering.test.tsx's own
// established pattern exactly: assert on the SPECIFIC visible text, never
// the whole rendered HTML string (a fixture id or title attribute makes
// two renderings differ even when the thing under test collapsed).
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'

import type { ChatGPTLinkStatus, Integration } from '@narvi/contracts/rest-dtos'

import { ChatGPTLinkCard, IntegrationRow } from '../IntegrationsPanel'

const XSS_SCRIPT = '<script>alert(document.cookie)</script>'
const XSS_IMG = '<img src=x onerror=alert(1)>'

function baseIntegration(overrides: Partial<Integration> = {}): Integration {
  return {
    surface: 'slack',
    configured: true,
    lastInboundAt: '2026-08-20T02:00:00Z',
    lastOutboundAt: '2026-08-20T02:05:00Z',
    lastOutboundStatus: 'delivered',
    lastOutboundError: null,
    ...overrides,
  }
}

function baseChatGPTStatus(overrides: Partial<ChatGPTLinkStatus> = {}): ChatGPTLinkStatus {
  return { status: 'unlinked', ...overrides }
}

function renderRow(integration: Integration): string {
  return renderToStaticMarkup(
    <table>
      <tbody>
        <IntegrationRow integration={integration} />
      </tbody>
    </table>,
  )
}

const noop = () => {}

describe('IntegrationRow rendering -- adversarial lastOutboundError stays text, never markup', () => {
  it('a hostile lastOutboundError renders as text', () => {
    const html = renderRow(baseIntegration({ lastOutboundStatus: 'dead_letter', lastOutboundError: `delivery failed: ${XSS_SCRIPT}` }))
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a null lastOutboundError renders a dash, never "null" as text', () => {
    const html = renderRow(baseIntegration({ lastOutboundError: null }))
    expect(html).toContain('—')
    expect(html).not.toContain('>null<')
  })

  it('a null lastOutboundAt renders "no delivery attempt recorded", not a fabricated timestamp', () => {
    const html = renderRow(baseIntegration({ lastOutboundAt: null, lastOutboundStatus: null, lastOutboundError: null }))
    expect(html).toContain('no delivery attempt recorded')
  })

  it('a null lastInboundAt renders "never received"', () => {
    const html = renderRow(baseIntegration({ lastInboundAt: null }))
    expect(html).toContain('never received')
  })

  it('configured=false renders "not configured", and no bare "configured" chip survives beside it', () => {
    const html = renderRow(baseIntegration({ configured: false }))
    expect(html).toContain('not configured')
    // The name used to promise this half and the body never checked it. A
    // substring test cannot: "configured" is inside "not configured". Strip
    // the negated form first, then nothing may still claim the positive one.
    expect(html.replace(/not configured/g, '')).not.toContain('configured')
  })
})

describe('ChatGPTLinkCard rendering -- adversarial userCode/verificationUrl stay text, unsafe hrefs are never emitted', () => {
  it('a hostile userCode renders as text while pending', () => {
    const html = renderToStaticMarkup(
      <ChatGPTLinkCard
        status={baseChatGPTStatus({ status: 'pending', verificationUrl: 'https://auth.openai.com/codex/device', userCode: `AB12 ${XSS_IMG}`, expiresAt: '2026-08-20T02:15:00Z' })}
        onStart={noop}
        onUnlink={noop}
        starting={false}
        unlinking={false}
      />,
    )
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('a safe https verificationUrl renders as a real, clickable anchor', () => {
    const html = renderToStaticMarkup(
      <ChatGPTLinkCard
        status={baseChatGPTStatus({ status: 'pending', verificationUrl: 'https://auth.openai.com/codex/device', userCode: 'AB12-CD34', expiresAt: '2026-08-20T02:15:00Z' })}
        onStart={noop}
        onUnlink={noop}
        starting={false}
        unlinking={false}
      />,
    )
    expect(html).toContain('href="https://auth.openai.com/codex/device"')
  })

  it('a hostile javascript: verificationUrl never becomes an href -- rendered as plain text instead', () => {
    const html = renderToStaticMarkup(
      <ChatGPTLinkCard
        status={baseChatGPTStatus({ status: 'pending', verificationUrl: 'javascript:alert(1)', userCode: 'AB12-CD34', expiresAt: '2026-08-20T02:15:00Z' })}
        onStart={noop}
        onUnlink={noop}
        starting={false}
        unlinking={false}
      />,
    )
    expect(html).not.toContain('href="javascript:')
    expect(html).toContain('javascript:alert(1)')
  })

  it('needs_relink shows the refresh-pump explanation and the crit tone, distinct from pending', () => {
    const html = renderToStaticMarkup(<ChatGPTLinkCard status={baseChatGPTStatus({ status: 'needs_relink' })} onStart={noop} onUnlink={noop} starting={false} unlinking={false} />)
    expect(html).toContain('chip crit')
    expect(html).toContain('refresh pump')
    expect(html).toContain('Reconnect ChatGPT account')
  })

  it('linked shows a Disconnect action, never a Connect one', () => {
    const html = renderToStaticMarkup(<ChatGPTLinkCard status={baseChatGPTStatus({ status: 'linked' })} onStart={noop} onUnlink={noop} starting={false} unlinking={false} />)
    expect(html).toContain('Disconnect')
    expect(html).not.toContain('Connect ChatGPT account')
  })
})
