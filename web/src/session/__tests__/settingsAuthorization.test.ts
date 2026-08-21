// settingsAuthorization.test.ts -- the same "the affordance is not the
// real gate" proof automationAuthorization.test.ts establishes for
// automations, applied to this Step's own new endpoints: createSandboxSecret/
// updateMemberRole/rotateCloudIdentitySigningKey/getIntegrations/
// startChatGPTLink (api/endpoints.ts) are unconditional network calls with
// no client-side authorization logic of their own. The real gate is
// server-side (authz.ActionManageRepoSecrets/ActionManageMembers/
// ActionManageCloudIdentityKeys/ActionManageIntegrations/
// ActionLinkChatGPTAccount respectively, internal/domain/authz), already
// proven directly against a real Postgres instance by this package's own
// Go integration tests (sandboxsecrets_integration_test.go,
// members_integration_test.go, cloudidentitykeys_integration_test.go,
// integrations_integration_test.go, chatgptlink_integration_test.go).
// This file proves the client half: the request really goes out over the
// wire, with the real method, and a 403 surfaces as a genuine, typed
// error rather than being silently swallowed or treated as success.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createSandboxSecret, getIntegrations, rotateCloudIdentitySigningKey, startChatGPTLink, updateMemberRole } from '../../api/endpoints'
import { ApiError } from '../../api/http'

afterEach(() => {
  vi.unstubAllGlobals()
})

function respondWith(body: unknown, status: number): ReturnType<typeof vi.fn> {
  const spy = vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }))
  vi.stubGlobal('fetch', spy)
  return spy
}

describe('createSandboxSecret -- the client always sends the real request', () => {
  it('calls POST on the repo-scoped sandbox-secrets route unconditionally, regardless of the calling component\'s own role check', async () => {
    const spy = respondWith({ id: 's1', scope: 'repo', scopeTarget: 'acme/widgets', name: 'X', maskedValue: '••••', createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z' }, 201)

    await createSandboxSecret({ kind: 'repo', owner: 'acme', repo: 'widgets' }, { name: 'X', value: 'secret' })

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/repos/acme/widgets/sandbox-secrets')
    expect(init.method).toBe('POST')
  })

  it('a 403 the server returns (a member, not admin/maintainer -- the REAL authz.ActionManageRepoSecrets gate) surfaces as a genuine ApiError, never silently treated as success', async () => {
    respondWith({ error: 'forbidden' }, 403)

    await expect(createSandboxSecret({ kind: 'repo', owner: 'acme', repo: 'widgets' }, { name: 'X', value: 'secret' })).rejects.toMatchObject({ status: 403 })
  })
})

describe('updateMemberRole -- the client always sends the real request', () => {
  it('calls PATCH /api/members/:id/role unconditionally (not PUT -- members.go mounts this as r.Patch)', async () => {
    const spy = respondWith({ id: 'u1', email: 'a@b.invalid', displayName: 'A', role: 'admin', disabled: false, createdAt: '2026-08-20T00:00:00Z', identities: [] }, 200)

    await updateMemberRole('u1', { role: 'admin' })

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/members/u1/role')
    expect(init.method).toBe('PATCH')
  })

  it('a 403 (a non-admin -- the REAL authz.ActionManageMembers gate) surfaces as a genuine ApiError', async () => {
    respondWith({ error: 'forbidden' }, 403)
    await expect(updateMemberRole('u1', { role: 'admin' })).rejects.toBeInstanceOf(ApiError)
  })
})

describe('rotateCloudIdentitySigningKey -- the client always sends the real, destructive-adjacent request', () => {
  it('calls POST /api/cloud-identity/signing-keys/rotate unconditionally -- the confirm-before-rotate UI (IntegrationsPanel.tsx) is a client-side nicety, never the real gate', async () => {
    const spy = respondWith({ activeKid: 'k2', activeCreatedAt: '2026-08-20T00:00:00Z', retiredKid: 'k1', retiredAt: '2026-08-20T00:00:00Z', publishableUntil: '2026-08-21T00:00:00Z' }, 200)

    await rotateCloudIdentitySigningKey()

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/cloud-identity/signing-keys/rotate')
    expect(init.method).toBe('POST')
  })

  it('a 403 (a non-admin -- the REAL authz.ActionManageCloudIdentityKeys gate) surfaces as a genuine ApiError', async () => {
    respondWith({ error: 'forbidden' }, 403)
    await expect(rotateCloudIdentitySigningKey()).rejects.toMatchObject({ status: 403 })
  })

  it('a 503 (the capability unconfigured -- RequireCloudIdentityCapability, fail-closed) surfaces as a genuine ApiError, never treated as "not yet loaded"', async () => {
    respondWith({ error: 'cloud identity federation is not configured' }, 503)
    await expect(rotateCloudIdentitySigningKey()).rejects.toMatchObject({ status: 503 })
  })
})

describe('getIntegrations -- the client always sends the real request', () => {
  it('calls GET /api/integrations unconditionally', async () => {
    const spy = respondWith({ integrations: [] }, 200)

    await getIntegrations()

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit | undefined]
    expect(url).toContain('/api/integrations')
    expect(init?.method ?? 'GET').toBe('GET')
  })

  it('a 403 (a non-admin -- the REAL authz.ActionManageIntegrations gate) surfaces as a genuine ApiError', async () => {
    respondWith({ error: 'forbidden' }, 403)
    await expect(getIntegrations()).rejects.toMatchObject({ status: 403 })
  })
})

describe('startChatGPTLink -- the client always sends the real request', () => {
  it('calls POST /api/me/chatgpt-link unconditionally -- the role-gated Connect button (IntegrationsPanel.tsx) is a client-side nicety, never the real gate', async () => {
    const spy = respondWith({ status: 'pending', verificationUrl: 'https://auth.openai.com/codex/device', userCode: 'ABCD-1234', expiresAt: '2026-08-20T00:15:00Z' }, 200)

    await startChatGPTLink()

    expect(spy).toHaveBeenCalledTimes(1)
    const [url, init] = spy.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/me/chatgpt-link')
    expect(init.method).toBe('POST')
  })

  it('a 403 (a viewer -- the REAL authz.ActionLinkChatGPTAccount gate) surfaces as a genuine ApiError', async () => {
    respondWith({ error: 'forbidden' }, 403)
    await expect(startChatGPTLink()).rejects.toMatchObject({ status: 403 })
  })
})
