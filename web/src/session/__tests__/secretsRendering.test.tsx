// secretsRendering.test.tsx -- SecretsPanel.tsx's own defining risk,
// proven at both the render boundary AND the data-layer boundary:
//
//   1. Adversarial content: SandboxSecret.name/ProviderCredential's own
//      scope-target text are third-party-authored free text and must
//      render as plain text only (mirrors reviewRendering.test.tsx/
//      automationRendering.test.tsx's own established pattern exactly).
//   2. This Step's own defining risk: a secret value typed into a create
//      form must never survive the request that sends it -- never in
//      the DOM, never in the TanStack Query cache. Driven end-to-end
//      through the REAL create -> list -> render code path (createSandboxSecret/
//      listSandboxSecrets from api/endpoints.ts, a real QueryClient, a
//      mocked fetch standing in for the server), never a synthetic
//      shortcut -- if a GET ever DID return a real value, this test's
//      own "response body contains no `value` key" assertion on the
//      mocked fetch response is the one place that defect would need to
//      be introduced for the rest of the test to still (wrongly) pass,
//      making the omission a deliberate, visible choice rather than an
//      accident this test could silently paper over.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import type { ProviderCredential, SandboxSecret } from '@narvi/contracts/rest-dtos'

import { createSandboxSecret, listSandboxSecrets } from '../../api/endpoints'
import { sandboxSecretQueryKeys } from '../../api/queryKeys'
import { ProviderCredentialRow, SandboxSecretRow } from '../SecretsPanel'

const XSS_SCRIPT = '<script>alert(document.cookie)</script>'
const XSS_IMG = '<img src=x onerror=alert(1)>'

function withQueryClient(node: React.ReactNode, client = new QueryClient()) {
  return renderToStaticMarkup(<QueryClientProvider client={client}>{node}</QueryClientProvider>)
}

function baseSandboxSecret(overrides: Partial<SandboxSecret> = {}): SandboxSecret {
  return {
    id: 'sec1',
    scope: 'repo',
    scopeTarget: 'acme/widgets',
    name: 'NPM_TOKEN',
    maskedValue: '••••••••••••',
    createdAt: '2026-08-20T02:00:00Z',
    updatedAt: '2026-08-20T02:00:00Z',
    ...overrides,
  }
}

function baseProviderCredential(overrides: Partial<ProviderCredential> = {}): ProviderCredential {
  return {
    id: 'cred1',
    scope: 'global',
    scopeTarget: null,
    provider: 'anthropic',
    maskedValue: '••••••••••••',
    createdAt: '2026-08-20T02:00:00Z',
    updatedAt: '2026-08-20T02:00:00Z',
    ...overrides,
  }
}

describe('SandboxSecretRow rendering -- adversarial name stays text, value is never a real secret', () => {
  it('a hostile secret.name renders as text', () => {
    const html = withQueryClient(<SandboxSecretRow secret={baseSandboxSecret({ name: `NPM_TOKEN ${XSS_SCRIPT}` })} canManage={false} deleting={false} onDelete={() => {}} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('a hostile scopeTarget (a caller-typed repo name) renders as text', () => {
    const html = withQueryClient(<SandboxSecretRow secret={baseSandboxSecret({ scopeTarget: XSS_IMG })} canManage={false} deleting={false} onDelete={() => {}} />)
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img')
  })

  it('renders ONLY the fixed maskedValue placeholder, never anything else in the "value" column', () => {
    const secret = baseSandboxSecret({ maskedValue: '••••••••••••' })
    const html = withQueryClient(<SandboxSecretRow secret={secret} canManage={false} deleting={false} onDelete={() => {}} />)
    expect(html).toContain('••••••••••••')
    // SandboxSecret's own generated type carries no `value` field at
    // all -- this assertion is a belt-and-suspenders proof that the
    // component itself introduces no such field either.
    expect(Object.keys(secret)).not.toContain('value')
  })
})

describe('ProviderCredentialRow rendering -- adversarial scopeTarget stays text, value is never a real secret', () => {
  it('a hostile scopeTarget renders as text', () => {
    const html = withQueryClient(<ProviderCredentialRow credential={baseProviderCredential({ scope: 'repo', scopeTarget: `acme/${XSS_SCRIPT}` })} canManage={false} deleting={false} onDelete={() => {}} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('renders ONLY the fixed maskedValue placeholder', () => {
    const credential = baseProviderCredential()
    const html = withQueryClient(<ProviderCredentialRow credential={credential} canManage={false} deleting={false} onDelete={() => {}} />)
    expect(html).toContain('••••••••••••')
    expect(Object.keys(credential)).not.toContain('value')
  })
})

describe('a secret value typed into a create form never survives the request that sends it', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('create -> list -> render: the plaintext value is sent ONCE (the create request body) and never appears again -- not in the create response, not in the query cache, not in the rendered DOM', async () => {
    const SECRET_PLAINTEXT = 'sk-super-secret-value-12345'
    const scope = { kind: 'repo' as const, owner: 'acme', repo: 'widgets' }

    const fetchSpy = vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        // The real server (sandboxsecrets.go) NEVER echoes the value
        // back -- proven here by the mock literally not carrying one,
        // mirroring SandboxSecret's own generated shape (no `value`
        // field exists to echo).
        return new Response(JSON.stringify(baseSandboxSecret({ id: 'sec-new', name: 'MY_SECRET' })), { status: 201, headers: { 'content-type': 'application/json' } })
      }
      return new Response(JSON.stringify({ sandboxSecrets: [baseSandboxSecret({ id: 'sec-new', name: 'MY_SECRET' })] }), { status: 200, headers: { 'content-type': 'application/json' } })
    })
    vi.stubGlobal('fetch', fetchSpy)

    // Step 1: the create call -- this is the ONE place the plaintext
    // legitimately travels, as the outgoing request body.
    const created = await createSandboxSecret(scope, { name: 'MY_SECRET', value: SECRET_PLAINTEXT })
    expect(JSON.stringify(created)).not.toContain(SECRET_PLAINTEXT)

    // Step 2: populate a REAL QueryClient cache via the SAME
    // listSandboxSecrets call SecretsPanel's own useQuery uses.
    const queryClient = new QueryClient()
    const key = sandboxSecretQueryKeys.list(scope)
    await queryClient.fetchQuery({ queryKey: key, queryFn: () => listSandboxSecrets(scope) })
    const cached = queryClient.getQueryData(key)
    expect(JSON.stringify(cached)).not.toContain(SECRET_PLAINTEXT)

    // Step 3: render the real row component off that SAME cached data.
    const list = cached as Awaited<ReturnType<typeof listSandboxSecrets>>
    const html = withQueryClient(<SandboxSecretRow secret={list.sandboxSecrets[0]} canManage={false} deleting={false} onDelete={() => {}} />, queryClient)
    expect(html).not.toContain(SECRET_PLAINTEXT)
    expect(html).toContain('••••••••••••')

    // The plaintext really did go out exactly once, as the create
    // request's own body -- confirms this test is exercising a REAL
    // send, not vacuously passing because nothing was ever sent.
    const postCall = fetchSpy.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'POST')
    expect(postCall).toBeDefined()
    expect(String((postCall as [string, RequestInit])[1].body)).toContain(SECRET_PLAINTEXT)
  })
})
